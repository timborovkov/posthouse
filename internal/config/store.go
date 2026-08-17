package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/timborovkov/posthouse/internal/filelock"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/safeio"
	"github.com/zalando/go-keyring"
)

const (
	currentVersion  = 2
	keyringService  = "posthouse"
	defaultMaxBytes = int64(2 << 30)
)

var (
	keyringGet        = keyring.Get
	keyringSet        = keyring.Set
	keyringDelete     = keyring.Delete
	defaultSecretsDir string
)

type Store struct {
	path string
}

func New(path string) (*Store, error) {
	if path == "" {
		path = os.Getenv("POSTHOUSE_CONFIG")
	}
	if path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(root, "posthouse", "config.json")
	}
	store := &Store{path: path}
	defaultSecretsDir = filepath.Join(filepath.Dir(path), "secrets")
	return store, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (model.Config, error) {
	var cfg model.Config
	err := s.withConfigLock(func() error {
		var err error
		cfg, err = s.loadUnlocked()
		return err
	})
	return cfg, err
}

func (s *Store) loadUnlocked() (model.Config, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		cfg := model.Config{Version: currentVersion}
		applyDefaults(&cfg)
		return cfg, nil
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg model.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return model.Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Version == 1 {
		migrateV1(&cfg)
		if err := s.backupV1(data); err != nil {
			return model.Config{}, err
		}
		if err := s.saveUnlocked(cfg); err != nil {
			return model.Config{}, fmt.Errorf("save migrated config: %w", err)
		}
	}
	if cfg.Version != currentVersion {
		return model.Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	cfg, err = Normalize(cfg)
	if err != nil {
		return model.Config{}, err
	}
	return cfg, nil
}

func (s *Store) Save(cfg model.Config) error {
	return s.withConfigLock(func() error { return s.saveUnlocked(cfg) })
}

// Update serializes a complete load-modify-save transaction across goroutines
// and processes using the same configuration path.
func (s *Store) Update(change func(model.Config) (model.Config, error)) error {
	return s.withConfigLock(func() error {
		cfg, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		cfg, err = change(cfg)
		if err != nil {
			return err
		}
		return s.saveUnlocked(cfg)
	})
}

func (s *Store) withConfigLock(action func() error) error {
	return filelock.Exclusive(s.path+".lock", action)
}

func (s *Store) saveUnlocked(cfg model.Config) error {
	var err error
	cfg, err = Normalize(cfg)
	if err != nil {
		return err
	}
	sort.Slice(cfg.Connections, func(i, j int) bool { return cfg.Connections[i].ID < cfg.Connections[j].ID })
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(s.path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	removeTemporary = false
	return nil
}

// Normalize returns the canonical configuration representation used for
// validation, persistence, and provider-identity comparisons.
func Normalize(cfg model.Config) (model.Config, error) {
	cfg.Version = currentVersion
	normalizeLegacyRefs(&cfg)
	applyDefaults(&cfg)
	for index := range cfg.Connections {
		if cfg.Connections[index].Calendar != nil && len(cfg.Connections[index].Calendar.Collections) == 0 {
			cfg.Connections[index].Calendar.Collections = nil
		}
	}
	if err := Validate(cfg); err != nil {
		return model.Config{}, err
	}
	return cfg, nil
}

func (s *Store) backupV1(data []byte) error {
	backup := s.path + ".v1.bak"
	if _, err := os.Stat(backup); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config migration backup: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
		return fmt.Errorf("create config migration directory: %w", err)
	}
	file, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create config migration backup: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write config migration backup: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync config migration backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close config migration backup: %w", err)
	}
	return nil
}

func migrateV1(cfg *model.Config) {
	cfg.Version = currentVersion
	for i := range cfg.Connections {
		connection := &cfg.Connections[i]
		if connection.Mail != nil {
			if connection.Mail.Secret.Env == "" && connection.Mail.SecretEnv != "" {
				connection.Mail.Secret.Env = connection.Mail.SecretEnv
			}
			connection.Mail.SecretEnv = ""
		}
		if connection.Calendar != nil {
			cal := connection.Calendar
			if cal.Kind == "" {
				cal.Kind = "feed"
			}
			if cal.URLSecret.Env == "" && cal.URLSecretEnv != "" {
				cal.URLSecret.Env = cal.URLSecretEnv
			}
			cal.URLSecretEnv = ""
		}
	}
	applyDefaults(cfg)
}

func normalizeLegacyRefs(cfg *model.Config) {
	for i := range cfg.Connections {
		connection := &cfg.Connections[i]
		if connection.Mail != nil && connection.Mail.Secret.Env == "" && connection.Mail.Secret.Keychain == "" && connection.Mail.SecretEnv != "" {
			connection.Mail.Secret.Env = connection.Mail.SecretEnv
			connection.Mail.SecretEnv = ""
		}
		if connection.Calendar != nil && connection.Calendar.URLSecret.Env == "" && connection.Calendar.URLSecret.Keychain == "" && connection.Calendar.URLSecretEnv != "" {
			connection.Calendar.URLSecret.Env = connection.Calendar.URLSecretEnv
			connection.Calendar.URLSecretEnv = ""
		}
	}
}

func applyDefaults(cfg *model.Config) {
	if cfg.Cache.MaxBytes == 0 {
		cfg.Cache.MaxBytes = defaultMaxBytes
	}
	if cfg.Cache.MessageMetadataDays == 0 {
		cfg.Cache.MessageMetadataDays = 90
	}
	if cfg.Cache.MessageBodyDays == 0 {
		cfg.Cache.MessageBodyDays = 30
	}
	if cfg.Cache.EventPastDays == 0 {
		cfg.Cache.EventPastDays = 90
	}
	if cfg.Cache.EventFutureDays == 0 {
		cfg.Cache.EventFutureDays = 365
	}
	for i := range cfg.Connections {
		connection := &cfg.Connections[i]
		connection.Capabilities = capabilities(*connection)
		if connection.Mail != nil && connection.Mail.SentCopy == "" {
			connection.Mail.SentCopy = "provider-managed"
		}
		if connection.Calendar != nil && connection.Calendar.Kind == "" {
			connection.Calendar.Kind = "feed"
		}
	}
}

func capabilities(connection model.Connection) []string {
	var result []string
	if connection.Mail != nil {
		switch MailKind(connection.Mail) {
		case MailKindGmail, MailKindMicrosoft:
			result = append(result, "mail.read", "mail.send")
		default:
			if connection.Mail.IMAP.Address != "" {
				result = append(result, "mail.read")
			}
			if connection.Mail.SMTP.Address != "" {
				result = append(result, "mail.send")
			}
		}
	}
	if connection.Calendar != nil {
		result = append(result, "calendar.read")
		switch CalendarKind(connection.Calendar) {
		case CalendarKindCalDAV, CalendarKindGmail, CalendarKindMicrosoft:
			result = append(result, "calendar.write")
		}
	}
	return result
}

func Validate(cfg model.Config) error {
	if cfg.Cache.MaxBytes < 0 {
		return fmt.Errorf("cache.max_bytes must be positive")
	}
	for name, days := range map[string]int{
		"message_metadata_days": cfg.Cache.MessageMetadataDays,
		"message_body_days":     cfg.Cache.MessageBodyDays,
		"event_past_days":       cfg.Cache.EventPastDays,
		"event_future_days":     cfg.Cache.EventFutureDays,
	} {
		if days < 0 {
			return fmt.Errorf("cache.%s must be positive", name)
		}
	}
	seen := make(map[string]struct{}, len(cfg.Connections))
	for index, connection := range cfg.Connections {
		prefix := fmt.Sprintf("connections[%d]", index)
		if strings.TrimSpace(connection.ID) == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if connection.ID != strings.TrimSpace(connection.ID) {
			return fmt.Errorf("%s.id must not have surrounding whitespace", prefix)
		}
		if strings.TrimSpace(connection.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		canonicalID := strings.ToLower(strings.TrimSpace(connection.ID))
		if _, ok := seen[canonicalID]; ok {
			return fmt.Errorf("duplicate connection id %q", connection.ID)
		}
		seen[canonicalID] = struct{}{}
		if connection.Mail == nil && connection.Calendar == nil {
			return fmt.Errorf("%s needs at least one capability", prefix)
		}
		if connection.Mail != nil {
			if err := validateMailConfig(prefix, connection.Mail); err != nil {
				return err
			}
		}
		if connection.Calendar != nil {
			if err := validateCalendarConfig(prefix, connection); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateMailConfig(prefix string, mail *model.MailConfig) error {
	kind := MailKind(mail)
	switch kind {
	case MailKindGmail, MailKindMicrosoft:
		if mail.IMAP.Address != "" || mail.SMTP.Address != "" {
			return fmt.Errorf("%s.mail %s cannot set imap or smtp addresses", prefix, kind)
		}
		if err := optionalSecretRef(prefix+".mail.secret", mail.Secret, mail.SecretEnv); err != nil {
			return err
		}
		switch mail.SentCopy {
		case "", "never", "provider-managed":
		case "always":
			return fmt.Errorf("%s.mail.sent_copy always is not valid for %s; the provider manages sent copies", prefix, kind)
		default:
			return fmt.Errorf("%s.mail.sent_copy must be always, never, or provider-managed", prefix)
		}
		return nil
	case MailKindIMAP:
	default:
		return fmt.Errorf("%s.mail.kind must be empty, gmail, or microsoft", prefix)
	}
	if mail.Username == "" || !(validSecretRef(mail.Secret) || (mail.SecretEnv != "" && mail.Secret.Env == "" && mail.Secret.Keychain == "")) {
		return fmt.Errorf("%s.mail username and exactly one secret env or keychain reference are required", prefix)
	}
	if mail.IMAP.Address == "" && mail.SMTP.Address == "" {
		return fmt.Errorf("%s.mail needs an IMAP or SMTP address", prefix)
	}
	if mail.IMAP.Address != "" {
		if err := validateTransport(prefix+".mail.imap", mail.IMAP.Address, mail.IMAP.TLS, mail.IMAP.StartTLS, mail.IMAP.Insecure); err != nil {
			return err
		}
		host, _, _ := net.SplitHostPort(mail.IMAP.Address)
		if !mail.IMAP.TLS && !mail.IMAP.StartTLS && host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("%s.mail.imap cannot authenticate over remote cleartext IMAP; enable tls or starttls", prefix)
		}
	}
	if mail.SMTP.Address != "" {
		if err := validateTransport(prefix+".mail.smtp", mail.SMTP.Address, mail.SMTP.TLS, mail.SMTP.StartTLS, mail.SMTP.Insecure); err != nil {
			return err
		}
		host, _, _ := net.SplitHostPort(mail.SMTP.Address)
		if mail.SMTP.Insecure && !mail.SMTP.TLS && !mail.SMTP.StartTLS && host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("%s.mail.smtp cannot authenticate over remote cleartext SMTP; enable tls or starttls", prefix)
		}
	}
	switch mail.SentCopy {
	case "", "always", "never", "provider-managed":
	default:
		return fmt.Errorf("%s.mail.sent_copy must be always, never, or provider-managed", prefix)
	}
	if mail.SentCopy == "always" && strings.TrimSpace(mail.Folders.Sent) == "" {
		return fmt.Errorf("%s.mail.folders.sent is required when sent_copy is always", prefix)
	}
	return nil
}

func validateCalendarConfig(prefix string, connection model.Connection) error {
	cal := connection.Calendar
	kind := CalendarKind(cal)
	switch kind {
	case CalendarKindGmail, CalendarKindMicrosoft:
		if strings.TrimSpace(cal.URL) != "" || validSecretRef(cal.URLSecret) || cal.URLSecretEnv != "" {
			return fmt.Errorf("%s.calendar %s cannot set url or url_secret", prefix, kind)
		}
		if len(cal.Collections) > 0 {
			return fmt.Errorf("%s.calendar %s does not use discovered collections", prefix, kind)
		}
		if err := optionalSecretRef(prefix+".calendar.secret", cal.Secret, ""); err != nil {
			return err
		}
		if MailKind(connection.Mail) != "" && MailKind(connection.Mail) != MailKindIMAP && MailKind(connection.Mail) != kind {
			return fmt.Errorf("%s.calendar.kind %s does not match mail.kind %s", prefix, kind, MailKind(connection.Mail))
		}
		return nil
	case CalendarKindFeed, CalendarKindCalDAV:
	default:
		return fmt.Errorf("%s.calendar.kind must be feed, caldav, gmail, or microsoft", prefix)
	}
	collectionIDs := make(map[string]struct{}, len(cal.Collections))
	for collectionIndex, collection := range cal.Collections {
		id := strings.ToLower(strings.TrimSpace(collection.ID))
		if id == "" {
			return fmt.Errorf("%s.calendar.collections[%d].id is required", prefix, collectionIndex)
		}
		if _, exists := collectionIDs[id]; exists {
			return fmt.Errorf("%s.calendar has duplicate collection id %q", prefix, collection.ID)
		}
		collectionIDs[id] = struct{}{}
		if err := validateCalDAVCollectionPath(collection.Path); err != nil {
			return fmt.Errorf("%s.calendar.collections[%d].path %w", prefix, collectionIndex, err)
		}
	}
	hasURL := strings.TrimSpace(cal.URL) != ""
	hasSecretURL := validSecretRef(cal.URLSecret) || (cal.URLSecretEnv != "" && cal.URLSecret.Env == "" && cal.URLSecret.Keychain == "")
	if hasURL == hasSecretURL {
		return fmt.Errorf("%s.calendar requires exactly one of url or url_secret", prefix)
	}
	if hasURL {
		if err := validateCalendarURL(cal.URL); err != nil {
			return fmt.Errorf("%s.calendar.url %w", prefix, err)
		}
	}
	if cal.Insecure && hasSecretURL {
		return fmt.Errorf("%s.calendar.insecure cannot be used with url_secret because loopback cannot be verified", prefix)
	}
	if cal.Insecure && hasURL {
		parsed, err := url.Parse(cal.URL)
		if err != nil || parsed.Host == "" {
			return fmt.Errorf("%s.calendar.url is invalid", prefix)
		}
		if host := parsed.Hostname(); host != "localhost" && host != "127.0.0.1" && host != "::1" {
			return fmt.Errorf("%s.calendar.insecure is allowed only for loopback development endpoints", prefix)
		}
	}
	switch kind {
	case CalendarKindFeed:
		if cal.Username != "" || validSecretRef(cal.Secret) {
			return fmt.Errorf("%s.calendar feed cannot have CalDAV credentials", prefix)
		}
	case CalendarKindCalDAV:
		if cal.Username == "" || !validSecretRef(cal.Secret) {
			return fmt.Errorf("%s.calendar CalDAV username and secret are required", prefix)
		}
	}
	return nil
}

func optionalSecretRef(name string, ref model.SecretRef, legacyEnv string) error {
	if ref.Env == "" && ref.Keychain == "" && legacyEnv == "" {
		return nil
	}
	if validSecretRef(ref) || (legacyEnv != "" && ref.Env == "" && ref.Keychain == "") {
		return nil
	}
	return fmt.Errorf("%s must use exactly one of env or keychain", name)
}

func validateCalendarURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return fmt.Errorf("is invalid")
	}
	if parsed.User != nil {
		return fmt.Errorf("must not contain credentials")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopbackHostname(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("must use HTTPS, except for loopback HTTP development endpoints")
}

func validateCalDAVCollectionPath(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") {
		return fmt.Errorf("must be a nonempty absolute path")
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must not contain an origin, credentials, query, or fragment")
	}
	if pathpkg.Clean(parsed.Path) != strings.TrimSuffix(parsed.Path, "/") || !strings.HasSuffix(parsed.Path, "/") {
		return fmt.Errorf("must be a clean collection directory ending in /")
	}
	return nil
}

func isLoopbackHostname(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validSecretRef(ref model.SecretRef) bool {
	return (strings.TrimSpace(ref.Env) != "") != (strings.TrimSpace(ref.Keychain) != "")
}

func validateTransport(name, address string, implicitTLS, startTLS, insecure bool) error {
	if implicitTLS && startTLS {
		return fmt.Errorf("%s cannot enable both tls and starttls", name)
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s address must use host:port: %w", name, err)
	}
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if !implicitTLS && !startTLS && !insecure && !isLocal {
		return fmt.Errorf("%s must enable tls or starttls; set insecure only for an explicitly trusted development server", name)
	}
	return nil
}

func Secret(environmentVariable string) (string, error) {
	value, ok := os.LookupEnv(environmentVariable)
	if !ok || value == "" {
		return "", fmt.Errorf("required secret environment variable %s is not set", environmentVariable)
	}
	return value, nil
}

func ResolveSecret(ref model.SecretRef) (string, error) {
	if ref.Env != "" {
		return Secret(ref.Env)
	}
	if ref.Keychain != "" {
		value, err := keyringGet(keyringService, ref.Keychain)
		if err == nil && value != "" {
			return value, nil
		}
		fallback, fallbackErr := readFallbackSecret(ref.Keychain)
		if fallbackErr == nil && fallback != "" {
			return fallback, nil
		}
		if err != nil {
			return "", fmt.Errorf("resolve keychain secret %q: %w", ref.Keychain, err)
		}
		if fallbackErr != nil {
			return "", fmt.Errorf("resolve keychain secret %q: %w", ref.Keychain, fallbackErr)
		}
		return "", fmt.Errorf("keychain secret %q is empty", ref.Keychain)
	}
	return "", fmt.Errorf("secret reference is not configured")
}

func SetKeychainSecret(name, value string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if value == "" {
		return fmt.Errorf("keychain secret name and value are required")
	}
	if err := keyringSet(keyringService, name, value); err == nil {
		return nil
	} else if err := writeFallbackSecret(name, value); err != nil {
		// Prefer the OS keychain. The file fallback is plaintext mode 0600 next
		// to config — a local secret file, not encrypted cache.
		return fmt.Errorf("store keychain secret %q: %w", name, err)
	}
	return nil
}

func DeleteKeychainSecret(name string) error {
	if err := validateSecretName(name); err != nil {
		return err
	}
	if err := keyringDelete(keyringService, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		// Servers often have no OS keychain; the on-disk fallback is enough.
	}
	if err := os.Remove(fallbackSecretPath(name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete keychain secret %q: %w", name, err)
	}
	return nil
}

func validateSecretName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("keychain secret name and value are required")
	}
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("secret name %q contains unsupported characters", name)
	}
	return nil
}

func secretsDir() string {
	if dir := strings.TrimSpace(os.Getenv("POSTHOUSE_SECRETS_DIR")); dir != "" {
		return dir
	}
	if defaultSecretsDir != "" {
		return defaultSecretsDir
	}
	path := strings.TrimSpace(os.Getenv("POSTHOUSE_CONFIG"))
	if path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return ""
		}
		return filepath.Join(root, "posthouse", "secrets")
	}
	return filepath.Join(filepath.Dir(path), "secrets")
}

func fallbackSecretPath(name string) string {
	return filepath.Join(secretsDir(), name)
}

// writeFallbackSecret stores a secret as a mode-0600 file next to the config.
// This is a local file, not a vault: it is not encrypted with POSTHOUSE_CACHE_KEY.
func writeFallbackSecret(name, value string) error {
	dir := secretsDir()
	if dir == "" {
		return fmt.Errorf("no secret store is available")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	_, err := safeio.WriteFile(fallbackSecretPath(name), []byte(value), true)
	return err
}

func readFallbackSecret(name string) (string, error) {
	data, err := os.ReadFile(fallbackSecretPath(name))
	if err != nil {
		return "", err
	}
	value := strings.TrimRight(string(data), "\r\n")
	if value == "" {
		return "", fmt.Errorf("secret file %q is empty", name)
	}
	return value, nil
}

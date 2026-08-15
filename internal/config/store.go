package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/timborovkov/posthouse/internal/model"
	"github.com/zalando/go-keyring"
)

const (
	currentVersion  = 2
	keyringService  = "posthouse"
	defaultMaxBytes = int64(2 << 30)
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
	return &Store{path: path}, nil
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Load() (model.Config, error) {
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
		if err := s.Save(cfg); err != nil {
			return model.Config{}, fmt.Errorf("save migrated config: %w", err)
		}
	}
	if cfg.Version != currentVersion {
		return model.Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	applyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
		return model.Config{}, err
	}
	return cfg, nil
}

func (s *Store) Save(cfg model.Config) error {
	cfg.Version = currentVersion
	normalizeLegacyRefs(&cfg)
	applyDefaults(&cfg)
	if err := Validate(cfg); err != nil {
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
		if connection.Mail.IMAP.Address != "" {
			result = append(result, "mail.read")
		}
		if connection.Mail.SMTP.Address != "" {
			result = append(result, "mail.send")
		}
	}
	if connection.Calendar != nil {
		result = append(result, "calendar.read")
		if connection.Calendar.Kind == "caldav" {
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
		if strings.TrimSpace(connection.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if _, ok := seen[strings.ToLower(connection.ID)]; ok {
			return fmt.Errorf("duplicate connection id %q", connection.ID)
		}
		seen[strings.ToLower(connection.ID)] = struct{}{}
		if connection.Mail == nil && connection.Calendar == nil {
			return fmt.Errorf("%s needs at least one capability", prefix)
		}
		if connection.Mail != nil {
			if connection.Mail.Username == "" || !(validSecretRef(connection.Mail.Secret) || (connection.Mail.SecretEnv != "" && connection.Mail.Secret.Env == "" && connection.Mail.Secret.Keychain == "")) {
				return fmt.Errorf("%s.mail username and exactly one secret env or keychain reference are required", prefix)
			}
			if connection.Mail.IMAP.Address == "" && connection.Mail.SMTP.Address == "" {
				return fmt.Errorf("%s.mail needs an IMAP or SMTP address", prefix)
			}
			if connection.Mail.IMAP.Address != "" {
				if err := validateTransport(prefix+".mail.imap", connection.Mail.IMAP.Address, connection.Mail.IMAP.TLS, connection.Mail.IMAP.StartTLS, connection.Mail.IMAP.Insecure); err != nil {
					return err
				}
				host, _, _ := net.SplitHostPort(connection.Mail.IMAP.Address)
				if !connection.Mail.IMAP.TLS && !connection.Mail.IMAP.StartTLS && host != "localhost" && host != "127.0.0.1" && host != "::1" {
					return fmt.Errorf("%s.mail.imap cannot authenticate over remote cleartext IMAP; enable tls or starttls", prefix)
				}
			}
			if connection.Mail.SMTP.Address != "" {
				if err := validateTransport(prefix+".mail.smtp", connection.Mail.SMTP.Address, connection.Mail.SMTP.TLS, connection.Mail.SMTP.StartTLS, connection.Mail.SMTP.Insecure); err != nil {
					return err
				}
				host, _, _ := net.SplitHostPort(connection.Mail.SMTP.Address)
				if connection.Mail.SMTP.Insecure && !connection.Mail.SMTP.TLS && !connection.Mail.SMTP.StartTLS && host != "localhost" && host != "127.0.0.1" && host != "::1" {
					return fmt.Errorf("%s.mail.smtp cannot authenticate over remote cleartext SMTP; enable tls or starttls", prefix)
				}
			}
			switch connection.Mail.SentCopy {
			case "", "always", "never", "provider-managed":
			default:
				return fmt.Errorf("%s.mail.sent_copy must be always, never, or provider-managed", prefix)
			}
			if connection.Mail.SentCopy == "always" && strings.TrimSpace(connection.Mail.Folders.Sent) == "" {
				return fmt.Errorf("%s.mail.folders.sent is required when sent_copy is always", prefix)
			}
		}
		if connection.Calendar != nil {
			cal := connection.Calendar
			hasURL := strings.TrimSpace(cal.URL) != ""
			hasSecretURL := validSecretRef(cal.URLSecret) || (cal.URLSecretEnv != "" && cal.URLSecret.Env == "" && cal.URLSecret.Keychain == "")
			if hasURL == hasSecretURL {
				return fmt.Errorf("%s.calendar requires exactly one of url or url_secret", prefix)
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
			kind := cal.Kind
			if kind == "" {
				kind = "feed"
			}
			switch kind {
			case "feed":
				if cal.Username != "" || validSecretRef(cal.Secret) {
					return fmt.Errorf("%s.calendar feed cannot have CalDAV credentials", prefix)
				}
			case "caldav":
				if cal.Username == "" || !validSecretRef(cal.Secret) {
					return fmt.Errorf("%s.calendar CalDAV username and secret are required", prefix)
				}
			default:
				return fmt.Errorf("%s.calendar.kind must be feed or caldav", prefix)
			}
		}
	}
	return nil
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
		value, err := keyring.Get(keyringService, ref.Keychain)
		if err != nil {
			return "", fmt.Errorf("resolve keychain secret %q: %w", ref.Keychain, err)
		}
		if value == "" {
			return "", fmt.Errorf("keychain secret %q is empty", ref.Keychain)
		}
		return value, nil
	}
	return "", fmt.Errorf("secret reference is not configured")
}

func SetKeychainSecret(name, value string) error {
	if strings.TrimSpace(name) == "" || value == "" {
		return fmt.Errorf("keychain secret name and value are required")
	}
	if err := keyring.Set(keyringService, name, value); err != nil {
		return fmt.Errorf("store keychain secret %q: %w", name, err)
	}
	return nil
}

func DeleteKeychainSecret(name string) error {
	if err := keyring.Delete(keyringService, name); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete keychain secret %q: %w", name, err)
	}
	return nil
}

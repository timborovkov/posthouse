package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/posthousehq/posthouse/internal/model"
)

const currentVersion = 1

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
		return model.Config{Version: currentVersion}, nil
	}
	if err != nil {
		return model.Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg model.Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return model.Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Version != currentVersion {
		return model.Config{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if err := Validate(cfg); err != nil {
		return model.Config{}, err
	}
	return cfg, nil
}

func (s *Store) Save(cfg model.Config) error {
	cfg.Version = currentVersion
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

func Validate(cfg model.Config) error {
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
			if connection.Mail.Username == "" || connection.Mail.SecretEnv == "" {
				return fmt.Errorf("%s.mail username and secret_env are required", prefix)
			}
			if connection.Mail.IMAP.Address == "" && connection.Mail.SMTP.Address == "" {
				return fmt.Errorf("%s.mail needs an IMAP or SMTP address", prefix)
			}
			if connection.Mail.IMAP.Address != "" {
				if err := validateTransport(prefix+".mail.imap", connection.Mail.IMAP.Address, connection.Mail.IMAP.TLS, connection.Mail.IMAP.StartTLS, connection.Mail.IMAP.Insecure); err != nil {
					return err
				}
			}
			if connection.Mail.SMTP.Address != "" {
				if err := validateTransport(prefix+".mail.smtp", connection.Mail.SMTP.Address, connection.Mail.SMTP.TLS, connection.Mail.SMTP.StartTLS, connection.Mail.SMTP.Insecure); err != nil {
					return err
				}
			}
		}
		if connection.Calendar != nil {
			hasURL := strings.TrimSpace(connection.Calendar.URL) != ""
			hasSecretURL := strings.TrimSpace(connection.Calendar.URLSecretEnv) != ""
			if hasURL == hasSecretURL {
				return fmt.Errorf("%s.calendar requires exactly one of url or url_secret_env", prefix)
			}
		}
	}
	return nil
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

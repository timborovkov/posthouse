package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestLoadMigratesV1AtomicallyAndKeepsBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	v1 := model.Config{Version: 1, Connections: []model.Connection{{ID: "work", Name: "Work", Mail: &model.MailConfig{Username: "work@example.test", SecretEnv: "WORK_PASSWORD", IMAP: model.IMAPConfig{Address: "localhost:3143", Insecure: true}}}}}
	data, _ := json.Marshal(v1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Version != 2 || migrated.Connections[0].Mail.Secret.Env != "WORK_PASSWORD" || migrated.Connections[0].Mail.SecretEnv != "" {
		t.Fatalf("migration returned %#v", migrated)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil {
		t.Fatal(err)
	}
	var original model.Config
	if err := json.Unmarshal(backup, &original); err != nil {
		t.Fatal(err)
	}
	if original.Version != 1 || original.Connections[0].Mail.SecretEnv != "WORK_PASSWORD" {
		t.Fatalf("backup changed: %#v", original)
	}
}

func TestValidateRejectsRemoteCleartextSMTPAuthentication(t *testing.T) {
	cfg := model.Config{Version: 2, Connections: []model.Connection{{ID: "work", Name: "Work", Mail: &model.MailConfig{Username: "work@example.test", Secret: model.SecretRef{Env: "WORK_PASSWORD"}, SMTP: model.SMTPConfig{Address: "smtp.example.test:25", Insecure: true}}}}}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate accepted remote cleartext SMTP authentication")
	}
	cfg.Connections[0].Mail.SMTP.Address = "localhost:3025"
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate rejected loopback development SMTP: %v", err)
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	store, err := New(path)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	want := model.Config{Connections: []model.Connection{{
		ID: "home", Name: "Home", Mail: &model.MailConfig{Username: "me@example.com", SecretEnv: "HOME_MAIL_PASSWORD", IMAP: model.IMAPConfig{Address: "imap.example.com:993", TLS: true}},
	}}}
	if err := store.Save(want); err != nil {
		t.Fatalf("Save returned error: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(got.Connections) != 1 || got.Connections[0].ID != "home" {
		t.Fatalf("Load returned %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions are %o, want 600", info.Mode().Perm())
	}
}

func TestValidateRejectsDuplicateIDsIgnoringCase(t *testing.T) {
	cfg := model.Config{Connections: []model.Connection{
		{ID: "Home", Name: "Home", Mail: &model.MailConfig{Username: "a", SecretEnv: "A", IMAP: model.IMAPConfig{Address: "a.example:993", TLS: true}}},
		{ID: "home", Name: "Other", Mail: &model.MailConfig{Username: "b", SecretEnv: "B", IMAP: model.IMAPConfig{Address: "b.example:993", TLS: true}}},
	}}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate returned nil error")
	}
}

func TestValidateCalendarRequiresOneFeedURLSource(t *testing.T) {
	base := model.Connection{ID: "calendar", Name: "Calendar"}
	for _, calendar := range []*model.CalendarConfig{
		{},
		{URL: "https://example.com/feed.ics", URLSecretEnv: "CALENDAR_URL"},
	} {
		connection := base
		connection.Calendar = calendar
		if err := Validate(model.Config{Connections: []model.Connection{connection}}); err == nil {
			t.Fatalf("Validate accepted %#v", calendar)
		}
	}
	connection := base
	connection.Calendar = &model.CalendarConfig{URLSecretEnv: "CALENDAR_URL"}
	if err := Validate(model.Config{Connections: []model.Connection{connection}}); err != nil {
		t.Fatalf("Validate rejected a secret feed URL: %v", err)
	}
}

func TestValidateRequiresFolderForAlwaysSentCopy(t *testing.T) {
	connection := model.Connection{ID: "work", Name: "Work", Mail: &model.MailConfig{
		Username: "work@example.test", SecretEnv: "WORK_PASSWORD",
		SMTP: model.SMTPConfig{Address: "smtp.example.test:465", TLS: true}, SentCopy: "always",
	}}
	if err := Validate(model.Config{Connections: []model.Connection{connection}}); err == nil {
		t.Fatal("Validate accepted always sent-copy without a sent folder")
	}
	connection.Mail.Folders.Sent = "Sent"
	if err := Validate(model.Config{Connections: []model.Connection{connection}}); err != nil {
		t.Fatalf("Validate rejected configured sent-copy folder: %v", err)
	}
}

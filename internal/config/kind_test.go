package config

import (
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestOAuthSecretNameDoesNotReuseIMAPKeychainForCalendarOAuth(t *testing.T) {
	mixed := model.Connection{
		ID: "mixed",
		Mail: &model.MailConfig{
			Username: "me@example.test",
			Secret:   model.SecretRef{Keychain: "imap-password"},
			IMAP:     model.IMAPConfig{Address: "localhost:1993"},
		},
		Calendar: &model.CalendarConfig{Kind: "gmail", Secret: model.SecretRef{Keychain: "calendar-oauth"}},
	}
	if got := OAuthSecretName(mixed); got != "calendar-oauth" {
		t.Fatalf("mixed IMAP+Gmail calendar secret name = %q", got)
	}
	mixed.Calendar.Secret.Keychain = ""
	if got := OAuthSecretName(mixed); got != "posthouse-mixed" {
		t.Fatalf("mixed IMAP+Gmail calendar fallback = %q", got)
	}
	native := model.Connection{
		ID:   "gmail-work",
		Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Keychain: "mail-oauth"}},
	}
	if got := OAuthSecretName(native); got != "mail-oauth" {
		t.Fatalf("native mail secret name = %q", got)
	}
}

func TestOAuthSecretNameSanitizesGeneratedKeychainNames(t *testing.T) {
	name := OAuthSecretName(model.Connection{ID: "work/home office", Mail: &model.MailConfig{Kind: "gmail"}})
	if strings.ContainsAny(name, "/ ") {
		t.Fatalf("unsafe generated keychain name %q", name)
	}
	if err := validateSecretName(name); err != nil {
		t.Fatalf("generated keychain name %q: %v", name, err)
	}
	if got := OAuthSecretName(model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail"}}); got != "posthouse-gmail-work" {
		t.Fatalf("simple generated name = %q", got)
	}
}

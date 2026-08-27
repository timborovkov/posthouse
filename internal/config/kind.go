package config

import (
	"strings"

	"github.com/timborovkov/posthouse/internal/model"
)

const (
	MailKindIMAP          = "imap"
	MailKindGmail         = "gmail"
	MailKindMicrosoft     = "microsoft"
	CalendarKindFeed      = "feed"
	CalendarKindCalDAV    = "caldav"
	CalendarKindGmail     = "gmail"
	CalendarKindMicrosoft = "microsoft"
)

func MailKind(mail *model.MailConfig) string {
	if mail == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(mail.Kind)) {
	case "", MailKindIMAP:
		return MailKindIMAP
	case MailKindGmail:
		return MailKindGmail
	case MailKindMicrosoft:
		return MailKindMicrosoft
	default:
		return strings.ToLower(strings.TrimSpace(mail.Kind))
	}
}

func CalendarKind(calendar *model.CalendarConfig) string {
	if calendar == nil {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(calendar.Kind))
	if kind == "" {
		return CalendarKindFeed
	}
	return kind
}

func NativeMail(connection model.Connection) bool {
	kind := MailKind(connection.Mail)
	return kind == MailKindGmail || kind == MailKindMicrosoft
}

func NativeCalendar(connection model.Connection) bool {
	kind := CalendarKind(connection.Calendar)
	return kind == CalendarKindGmail || kind == CalendarKindMicrosoft
}

func NativeKind(connection model.Connection) string {
	if NativeMail(connection) {
		return MailKind(connection.Mail)
	}
	if NativeCalendar(connection) {
		return CalendarKind(connection.Calendar)
	}
	return ""
}

func CanSendMail(connection model.Connection) bool {
	if connection.Mail == nil {
		return false
	}
	if NativeMail(connection) {
		return true
	}
	return connection.Mail.SMTP.Address != ""
}

func OAuthSecretName(connection model.Connection) string {
	if connection.Mail != nil && strings.TrimSpace(connection.Mail.Secret.Keychain) != "" {
		return strings.TrimSpace(connection.Mail.Secret.Keychain)
	}
	if connection.Calendar != nil && strings.TrimSpace(connection.Calendar.Secret.Keychain) != "" {
		return strings.TrimSpace(connection.Calendar.Secret.Keychain)
	}
	return "posthouse-" + connection.ID
}

func ConnectionReferencesOAuthSecret(connection model.Connection, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if connection.Mail != nil && strings.TrimSpace(connection.Mail.Secret.Keychain) == name {
		return true
	}
	if connection.Calendar != nil && strings.TrimSpace(connection.Calendar.Secret.Keychain) == name {
		return true
	}
	return false
}

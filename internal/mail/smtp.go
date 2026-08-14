package mail

import (
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"slices"
	"strings"
	"time"

	"github.com/posthousehq/posthouse/internal/config"
	"github.com/posthousehq/posthouse/internal/model"
)

func Send(connection model.Connection, message model.SendMessage) error {
	if connection.Mail == nil || connection.Mail.SMTP.Address == "" {
		return fmt.Errorf("connection %s has no SMTP capability", connection.ID)
	}
	if len(message.To)+len(message.CC)+len(message.BCC) == 0 {
		return fmt.Errorf("at least one recipient is required")
	}
	secret, err := config.Secret(connection.Mail.SecretEnv)
	if err != nil {
		return err
	}
	host, err := smtpHost(connection.Mail.SMTP.Address)
	if err != nil {
		return err
	}
	client, err := dialSMTP(connection.Mail.SMTP, host)
	if err != nil {
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("AUTH"); !ok {
		return fmt.Errorf("SMTP server %s does not advertise AUTH", host)
	}
	if err := client.Auth(smtp.PlainAuth("", connection.Mail.Username, secret, host)); err != nil {
		return fmt.Errorf("authenticate to SMTP: %w", err)
	}
	from := connection.Identity.Email
	if from == "" {
		from = connection.Mail.Username
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	recipients := uniqueStrings(append(append(slices.Clone(message.To), message.CC...), message.BCC...))
	for _, recipient := range recipients {
		address, err := stdmail.ParseAddress(recipient)
		if err != nil {
			return fmt.Errorf("invalid recipient %q: %w", recipient, err)
		}
		if err := client.Rcpt(address.Address); err != nil {
			return fmt.Errorf("set SMTP recipient %s: %w", address.Address, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP body: %w", err)
	}
	if _, err := writer.Write(buildMessage(connection.Identity, from, message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}

func dialSMTP(settings model.SMTPConfig, host string) (*smtp.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if settings.TLS {
		connection, err := tls.Dial("tcp", settings.Address, tlsConfig)
		if err != nil {
			return nil, fmt.Errorf("connect to SMTP over TLS: %w", err)
		}
		client, err := smtp.NewClient(connection, host)
		if err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("start SMTP client: %w", err)
		}
		return client, nil
	}
	client, err := smtp.Dial(settings.Address)
	if err != nil {
		return nil, fmt.Errorf("connect to SMTP: %w", err)
	}
	if settings.StartTLS {
		if err := client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if !settings.Insecure && host != "localhost" && host != "127.0.0.1" && host != "::1" {
		_ = client.Close()
		return nil, fmt.Errorf("refusing cleartext SMTP connection without insecure=true")
	}
	return client, nil
}

func buildMessage(identity model.Identity, from string, message model.SendMessage) []byte {
	fromAddress := (&stdmail.Address{Name: identity.Name, Address: from}).String()
	var builder strings.Builder
	writeHeader(&builder, "From", fromAddress)
	writeHeader(&builder, "To", strings.Join(message.To, ", "))
	if len(message.CC) > 0 {
		writeHeader(&builder, "Cc", strings.Join(message.CC, ", "))
	}
	if message.ReplyTo != "" {
		writeHeader(&builder, "Reply-To", message.ReplyTo)
	}
	writeHeader(&builder, "Date", time.Now().Format(time.RFC1123Z))
	writeHeader(&builder, "Subject", mime.QEncoding.Encode("utf-8", cleanHeader(message.Subject)))
	writeHeader(&builder, "MIME-Version", "1.0")
	writeHeader(&builder, "Content-Type", `text/plain; charset="utf-8"`)
	writeHeader(&builder, "Content-Transfer-Encoding", "8bit")
	builder.WriteString("\r\n")
	body := strings.ReplaceAll(message.Text, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	builder.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if !strings.HasSuffix(builder.String(), "\r\n") {
		builder.WriteString("\r\n")
	}
	return []byte(builder.String())
}

func writeHeader(writer io.StringWriter, key string, value string) {
	if value == "" {
		return
	}
	_, _ = writer.WriteString(key + ": " + cleanHeader(value) + "\r\n")
}

func cleanHeader(value string) string {
	return strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
}

func smtpHost(address string) (string, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", fmt.Errorf("SMTP address %q must use host:port: %w", address, err)
	}
	if host == "" {
		return "", fmt.Errorf("SMTP address %q has an empty host", address)
	}
	return host, nil
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

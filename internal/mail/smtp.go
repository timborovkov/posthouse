package mail

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"net/textproto"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
	messageproto "github.com/emersion/go-message/textproto"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/netproxy"
)

type UncertainError struct{ Err error }

func (e *UncertainError) Error() string {
	return "SMTP delivery outcome is uncertain after DATA: " + e.Err.Error()
}
func (e *UncertainError) Unwrap() error { return e.Err }

func Send(connection model.Connection, message model.SendMessage) error {
	data, err := BuildMessage(connection, message)
	if err != nil {
		return err
	}
	return SendSerialized(connection, message, data)
}

func BuildMessage(connection model.Connection, message model.SendMessage) ([]byte, error) {
	from := connection.Identity.Email
	if from == "" && connection.Mail != nil {
		from = connection.Mail.Username
	}
	return buildMessage(connection.Identity, from, message)
}

func BuildDraftMessage(connection model.Connection, message model.SendMessage) ([]byte, error) {
	from := connection.Identity.Email
	if from == "" && connection.Mail != nil {
		from = connection.Mail.Username
	}
	return buildMessageWithBCC(connection.Identity, from, message, true)
}

// BuildSentCopy adds the confirmed blind-recipient list to an already
// serialized SMTP message without regenerating its Date, Message-ID, MIME
// boundaries, body, or attachments.
func BuildSentCopy(data []byte, bcc []string) ([]byte, error) {
	if len(bcc) == 0 {
		return data, nil
	}
	var header gomail.Header
	if err := setAddresses(&header, "Bcc", bcc); err != nil {
		return nil, err
	}
	separator := []byte("\r\n\r\n")
	index := bytes.Index(data, separator)
	if index < 0 {
		return nil, fmt.Errorf("serialized message has no header boundary")
	}
	var rendered bytes.Buffer
	if err := messageproto.WriteHeader(&rendered, header.Header.Header); err != nil {
		return nil, fmt.Errorf("serialize archival Bcc header: %w", err)
	}
	line := append([]byte("\r\n"), bytes.TrimSuffix(rendered.Bytes(), separator)...)
	result := make([]byte, 0, len(data)+len(line))
	result = append(result, data[:index]...)
	result = append(result, line...)
	result = append(result, data[index:]...)
	return result, nil
}

func ValidateMessage(message model.SendMessage) error { return validateMessage(message) }

func SendSerialized(connection model.Connection, message model.SendMessage, data []byte) error {
	return SendSerializedContext(context.Background(), connection, message, data)
}

func SendSerializedContext(ctx context.Context, connection model.Connection, message model.SendMessage, data []byte) error {
	if connection.Mail == nil || connection.Mail.SMTP.Address == "" {
		return fmt.Errorf("connection %s has no SMTP capability", connection.ID)
	}
	if len(message.To)+len(message.CC)+len(message.BCC) == 0 {
		return fmt.Errorf("at least one recipient is required")
	}
	if len(data) == 0 {
		return fmt.Errorf("serialized message is empty")
	}
	secret, err := resolvedMailSecret(connection)
	if err != nil {
		return err
	}
	host, err := smtpHost(connection.Mail.SMTP.Address)
	if err != nil {
		return err
	}
	client, err := dialSMTPContext(ctx, connection.Mail.SMTP, host)
	if err != nil {
		return err
	}
	defer client.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()

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
	recipients, err := envelopeRecipients(append(append(slices.Clone(message.To), message.CC...), message.BCC...))
	if err != nil {
		return err
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("set SMTP recipient %s: %w", recipient, err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP body: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		// Do not close the DATA writer here: Close sends the SMTP terminator and
		// could make the server accept a partial message. Closing the client
		// connection causes the server to discard the incomplete transaction.
		return fmt.Errorf("write SMTP body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return classifyDataCloseError(err)
	}
	// DATA has been accepted at this point. A failed QUIT must not invite a
	// retry that could duplicate the message.
	_ = client.Quit()
	return nil
}

func classifyDataCloseError(err error) error {
	var statusErr *textproto.Error
	if errors.As(err, &statusErr) {
		return fmt.Errorf("SMTP server rejected message after DATA: %w", err)
	}
	return &UncertainError{Err: err}
}

func dialSMTP(settings model.SMTPConfig, host string) (*smtp.Client, error) {
	return dialSMTPContext(context.Background(), settings, host)
}

func dialSMTPContext(ctx context.Context, settings model.SMTPConfig, host string) (*smtp.Client, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	connection, err := netproxy.DialTCP(ctx, settings.Address, settings.Proxy)
	if err != nil {
		return nil, fmt.Errorf("connect to SMTP: %w", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	_ = connection.SetDeadline(deadline)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	if settings.TLS {
		tlsConnection := tls.Client(connection, tlsConfig)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("connect to SMTP over TLS: %w", err)
		}
		client, err := smtp.NewClient(tlsConnection, host)
		if err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("start SMTP client: %w", err)
		}
		_ = connection.SetDeadline(time.Time{})
		return client, nil
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		_ = connection.Close()
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
	_ = connection.SetDeadline(time.Time{})
	return client, nil
}

func buildMessage(identity model.Identity, from string, message model.SendMessage) ([]byte, error) {
	return buildMessageWithBCC(identity, from, message, false)
}

func buildMessageWithBCC(identity model.Identity, from string, message model.SendMessage, includeBCC bool) ([]byte, error) {
	if err := validateMessage(message); err != nil {
		return nil, err
	}
	var buffer bytes.Buffer
	if err := writeMessage(&buffer, identity, from, message, includeBCC); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func validateMessage(message model.SendMessage) error {
	var header gomail.Header
	for key, values := range map[string][]string{"To": message.To, "Cc": message.CC, "Bcc": message.BCC} {
		if err := setAddresses(&header, key, values); err != nil {
			return err
		}
	}
	if message.ReplyTo != "" {
		if err := setAddresses(&header, "Reply-To", []string{message.ReplyTo}); err != nil {
			return err
		}
	}
	for _, attachment := range message.Attachments {
		name := attachment.Name
		if name == "" && attachment.Path != "" {
			name = filepath.Base(attachment.Path)
		}
		if name == "" {
			return fmt.Errorf("attachment name is required")
		}
		if attachment.Path != "" && attachment.Data == nil {
			info, err := os.Stat(attachment.Path)
			if err != nil {
				return fmt.Errorf("inspect attachment %s: %w", name, err)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("attachment %s must be a regular file", name)
			}
			file, err := os.Open(attachment.Path)
			if err != nil {
				return fmt.Errorf("open attachment %s: %w", name, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close attachment %s: %w", name, err)
			}
		}
	}
	return nil
}

func writeMessage(writer io.Writer, identity model.Identity, from string, message model.SendMessage, includeBCC bool) error {
	var header gomail.Header
	header.SetDate(time.Now())
	header.SetAddressList("From", []*gomail.Address{{Name: identity.Name, Address: from}})
	if err := setAddresses(&header, "To", message.To); err != nil {
		return err
	}
	if err := setAddresses(&header, "Cc", message.CC); err != nil {
		return err
	}
	if includeBCC {
		if err := setAddresses(&header, "Bcc", message.BCC); err != nil {
			return err
		}
	}
	if message.ReplyTo != "" {
		if err := setAddresses(&header, "Reply-To", []string{message.ReplyTo}); err != nil {
			return err
		}
	}
	header.SetSubject(cleanHeader(message.Subject))
	if message.InReplyTo != "" {
		header.Set("In-Reply-To", cleanHeader(message.InReplyTo))
	}
	if len(message.References) > 0 {
		header.Set("References", cleanHeader(strings.Join(message.References, " ")))
	}
	if err := header.GenerateMessageID(); err != nil {
		return fmt.Errorf("generate message ID: %w", err)
	}
	if len(message.Attachments) == 0 {
		if strings.TrimSpace(message.HTML) == "" {
			var inline gomail.InlineHeader
			inline.Set("Content-Type", `text/plain; charset="utf-8"`)
			part, err := gomail.CreateSingleInlineWriter(writer, header)
			if err != nil {
				return fmt.Errorf("create message body: %w", err)
			}
			_, writeErr := io.WriteString(part, normalizeBody(message.Text))
			closeErr := part.Close()
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		alternative, err := gomail.CreateInlineWriter(writer, header)
		if err != nil {
			return fmt.Errorf("create alternative message: %w", err)
		}
		if err := writeTextAndHTML(alternative.CreatePart, message); err != nil {
			_ = alternative.Close()
			return err
		}
		return alternative.Close()
	}
	multipart, err := gomail.CreateWriter(writer, header)
	if err != nil {
		return fmt.Errorf("create multipart message: %w", err)
	}
	if strings.TrimSpace(message.HTML) == "" {
		if err := writeInlinePart(multipart.CreateSingleInline, `text/plain; charset="utf-8"`, message.Text); err != nil {
			_ = multipart.Close()
			return err
		}
	} else {
		alternative, err := multipart.CreateInline()
		if err != nil {
			_ = multipart.Close()
			return fmt.Errorf("create alternative body: %w", err)
		}
		if err := writeTextAndHTML(alternative.CreatePart, message); err != nil {
			_ = alternative.Close()
			_ = multipart.Close()
			return err
		}
		if err := alternative.Close(); err != nil {
			_ = multipart.Close()
			return err
		}
	}
	for _, attachment := range message.Attachments {
		name := attachment.Name
		if name == "" && attachment.Path != "" {
			name = filepath.Base(attachment.Path)
		}
		if name == "" {
			return fmt.Errorf("attachment name is required")
		}
		contentType := attachment.ContentType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		var attachmentHeader gomail.AttachmentHeader
		attachmentHeader.Set("Content-Type", contentType)
		attachmentHeader.SetFilename(name)
		part, err := multipart.CreateAttachment(attachmentHeader)
		if err != nil {
			return fmt.Errorf("create attachment %s: %w", name, err)
		}
		var source io.ReadCloser
		if attachment.Data != nil {
			source = io.NopCloser(bytes.NewReader(attachment.Data))
		} else if attachment.Path != "" {
			source, err = os.Open(attachment.Path)
			if err != nil {
				_ = part.Close()
				return fmt.Errorf("open attachment %s: %w", name, err)
			}
		} else {
			source = io.NopCloser(bytes.NewReader(nil))
		}
		_, copyErr := io.Copy(part, source)
		closeSourceErr := source.Close()
		closePartErr := part.Close()
		if copyErr != nil {
			return fmt.Errorf("write attachment %s: %w", name, copyErr)
		}
		if closeSourceErr != nil {
			return closeSourceErr
		}
		if closePartErr != nil {
			return closePartErr
		}
	}
	return multipart.Close()
}

func writeTextAndHTML(create func(gomail.InlineHeader) (io.WriteCloser, error), message model.SendMessage) error {
	text := message.Text
	if strings.TrimSpace(text) == "" {
		text = htmlToText(message.HTML)
	}
	if err := writeInlinePart(create, `text/plain; charset="utf-8"`, text); err != nil {
		return err
	}
	return writeInlinePart(create, `text/html; charset="utf-8"`, message.HTML)
}

func writeInlinePart(create func(gomail.InlineHeader) (io.WriteCloser, error), contentType, body string) error {
	var inline gomail.InlineHeader
	inline.Set("Content-Type", contentType)
	part, err := create(inline)
	if err != nil {
		return fmt.Errorf("create %s body: %w", contentType, err)
	}
	_, writeErr := io.WriteString(part, normalizeBody(body))
	closeErr := part.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func setAddresses(header *gomail.Header, key string, values []string) error {
	if len(values) == 0 {
		return nil
	}
	addresses := make([]*gomail.Address, 0, len(values))
	for _, value := range values {
		parsed, err := stdmail.ParseAddress(value)
		if err != nil {
			return fmt.Errorf("invalid %s address %q: %w", strings.ToLower(key), value, err)
		}
		addresses = append(addresses, &gomail.Address{Name: parsed.Name, Address: parsed.Address})
	}
	header.SetAddressList(key, addresses)
	return nil
}

func normalizeBody(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	return value
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

func envelopeRecipients(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		address, err := stdmail.ParseAddress(value)
		if err != nil {
			return nil, fmt.Errorf("invalid recipient %q: %w", value, err)
		}
		key := strings.ToLower(address.Address)
		if !seen[key] {
			seen[key] = true
			result = append(result, address.Address)
		}
	}
	return result, nil
}

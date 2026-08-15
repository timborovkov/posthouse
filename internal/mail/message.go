package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/smtp"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	gomail "github.com/emersion/go-message/mail"
	"github.com/microcosm-cc/bluemonday"

	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
)

const maxMessageBytes = 64 << 20

type FetchedMessage struct {
	Detail      model.MessageDetail
	Attachments map[string][]byte
	Raw         []byte
	UIDValidity uint32
}

type Discovery struct {
	Capabilities []string           `json:"capabilities"`
	Folders      model.FolderConfig `json:"folders"`
}

type MessagePrecondition struct {
	UIDValidity uint32 `json:"uid_validity"`
	ModSeq      uint64 `json:"modseq,omitempty"`
}

type UncertainAppendError struct{ Err error }

func (err *UncertainAppendError) Error() string {
	return "IMAP APPEND outcome is uncertain after the message literal was written: " + err.Err.Error()
}

func (err *UncertainAppendError) Unwrap() error { return err.Err }

type flagChange struct {
	flag  imap.Flag
	value *bool
}

func Get(connection model.Connection, folder string, uid uint32) (FetchedMessage, error) {
	if connection.Mail == nil || connection.Mail.IMAP.Address == "" {
		return FetchedMessage{}, fmt.Errorf("connection %s has no IMAP capability", connection.ID)
	}
	if uid == 0 {
		return FetchedMessage{}, fmt.Errorf("message UID is required")
	}
	if folder == "" {
		folder = connection.Mail.Folders.Inbox
	}
	if folder == "" {
		folder = "INBOX"
	}
	client, err := authenticatedIMAP(connection)
	if err != nil {
		return FetchedMessage{}, err
	}
	defer client.Close()
	defer client.Logout()
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return FetchedMessage{}, fmt.Errorf("select IMAP folder %s: %w", folder, err)
	}
	items, err := client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
		UID: true, Envelope: true, Flags: true, InternalDate: true, RFC822Size: true,
	}).Collect()
	if err != nil {
		return FetchedMessage{}, fmt.Errorf("fetch IMAP message: %w", err)
	}
	if len(items) != 1 {
		return FetchedMessage{}, fmt.Errorf("message %s/%d does not exist", folder, uid)
	}
	if items[0].RFC822Size > maxMessageBytes {
		return FetchedMessage{}, fmt.Errorf("message exceeds 64 MiB read limit")
	}
	section := messageBodySection()
	command := client.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}})
	message := command.Next()
	if message == nil {
		if err := command.Close(); err != nil {
			return FetchedMessage{}, fmt.Errorf("fetch IMAP message body: %w", err)
		}
		return FetchedMessage{}, fmt.Errorf("message %s/%d does not exist", folder, uid)
	}
	var raw []byte
	for item := message.Next(); item != nil; item = message.Next() {
		body, ok := item.(imapclient.FetchItemDataBodySection)
		if !ok || !body.MatchCommand(section) || body.Literal == nil {
			continue
		}
		raw, err = readBoundedLiteral(body.Literal, maxMessageBytes)
		if err != nil {
			_ = client.Close()
			return FetchedMessage{}, err
		}
	}
	if err := command.Close(); err != nil {
		return FetchedMessage{}, fmt.Errorf("fetch IMAP message body: %w", err)
	}
	if raw == nil {
		return FetchedMessage{}, fmt.Errorf("message %s/%d body was not returned", folder, uid)
	}
	result, err := parseMessage(raw)
	if err != nil {
		return FetchedMessage{}, err
	}
	result.Raw = append([]byte(nil), raw...)
	result.UIDValidity = selected.UIDValidity
	result.Detail.ConnectionID = connection.ID
	result.Detail.Folder = folder
	result.Detail.UID = uid
	result.Detail.ReceivedAt = items[0].InternalDate
	result.Detail.Unread = !slices.Contains(items[0].Flags, imap.FlagSeen)
	result.Detail.Flagged = slices.Contains(items[0].Flags, imap.FlagFlagged)
	result.Detail.HasAttachments = len(result.Detail.Attachments) > 0
	return result, nil
}

func messageBodySection() *imap.FetchItemBodySection {
	return &imap.FetchItemBodySection{Peek: true}
}

func readBoundedLiteral(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read IMAP message body: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("message exceeds 64 MiB read limit")
	}
	return data, nil
}

func GetAttachment(connection model.Connection, folder string, uid uint32, attachmentID string) (model.Attachment, []byte, uint32, error) {
	message, err := Get(connection, folder, uid)
	if err != nil {
		return model.Attachment{}, nil, 0, err
	}
	for _, attachment := range message.Detail.Attachments {
		if attachment.ID == attachmentID {
			return attachment, message.Attachments[attachmentID], message.UIDValidity, nil
		}
	}
	return model.Attachment{}, nil, 0, fmt.Errorf("attachment %q does not exist", attachmentID)
}

func parseMessage(raw []byte) (FetchedMessage, error) {
	reader, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && reader == nil {
		return FetchedMessage{}, fmt.Errorf("parse MIME message: %w", err)
	}
	defer reader.Close()
	result := FetchedMessage{Attachments: make(map[string][]byte)}
	result.Detail.MessageID, _ = reader.Header.MessageID()
	result.Detail.Subject, _ = reader.Header.Subject()
	result.Detail.Date, _ = reader.Header.Date()
	result.Detail.From = headerAddresses(reader.Header, "From")
	result.Detail.To = headerAddresses(reader.Header, "To")
	result.Detail.CC = headerAddresses(reader.Header, "Cc")
	result.Detail.ReplyTo = headerAddresses(reader.Header, "Reply-To")
	result.Detail.InReplyTo = strings.TrimSpace(reader.Header.Get("In-Reply-To"))
	result.Detail.References = strings.Fields(reader.Header.Get("References"))
	attachmentIndex := 0
	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}
		if partErr != nil && part == nil {
			return FetchedMessage{}, fmt.Errorf("read MIME part: %w", partErr)
		}
		data, readErr := io.ReadAll(io.LimitReader(part.Body, maxMessageBytes+1))
		if readErr != nil {
			return FetchedMessage{}, fmt.Errorf("read MIME part: %w", readErr)
		}
		if len(data) > maxMessageBytes {
			return FetchedMessage{}, fmt.Errorf("MIME part exceeds 64 MiB read limit")
		}
		switch header := part.Header.(type) {
		case *gomail.InlineHeader:
			contentType, _, _ := header.ContentType()
			switch strings.ToLower(contentType) {
			case "text/plain":
				if result.Detail.Text == "" {
					result.Detail.Text = string(data)
				}
			case "text/html":
				if result.Detail.HTML == "" {
					result.Detail.HTML = sanitizeHTML(string(data))
				}
			default:
				name := inlineFilename(header, attachmentIndex)
				digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", attachmentIndex, name, contentType)))
				id := base64.RawURLEncoding.EncodeToString(digest[:12])
				attachment := model.Attachment{ID: id, Name: name, ContentType: contentType, Size: int64(len(data)), Inline: true, ContentID: strings.Trim(header.Get("Content-ID"), "<>")}
				result.Detail.Attachments = append(result.Detail.Attachments, attachment)
				result.Attachments[id] = data
				attachmentIndex++
			}
		case *gomail.AttachmentHeader:
			name, _ := header.Filename()
			contentType, _, _ := header.ContentType()
			digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", attachmentIndex, name, contentType)))
			id := base64.RawURLEncoding.EncodeToString(digest[:12])
			disposition := strings.ToLower(header.Get("Content-Disposition"))
			attachment := model.Attachment{ID: id, Name: name, ContentType: contentType, Size: int64(len(data)), Inline: strings.HasPrefix(disposition, "inline"), ContentID: strings.Trim(header.Get("Content-ID"), "<>")}
			result.Detail.Attachments = append(result.Detail.Attachments, attachment)
			result.Attachments[id] = data
			attachmentIndex++
		}
	}
	if result.Detail.Text == "" && result.Detail.HTML != "" {
		result.Detail.Text = htmlToText(result.Detail.HTML)
	}
	result.Detail.Preview = preview([]byte(result.Detail.Text))
	return result, nil
}

func inlineFilename(header *gomail.InlineHeader, index int) string {
	_, dispositionParams, _ := header.ContentDisposition()
	if name := dispositionParams["filename"]; name != "" {
		return name
	}
	_, contentTypeParams, _ := header.ContentType()
	if name := contentTypeParams["name"]; name != "" {
		return name
	}
	return fmt.Sprintf("inline-%d", index+1)
}

func headerAddresses(header gomail.Header, key string) []model.Address {
	values, err := header.AddressList(key)
	if err != nil {
		return nil
	}
	result := make([]model.Address, 0, len(values))
	for _, value := range values {
		result = append(result, model.Address{Name: value.Name, Email: value.Address})
	}
	return result
}

var (
	emailHTMLPolicy = bluemonday.UGCPolicy()
	htmlTags        = regexp.MustCompile(`(?s)<[^>]+>`)
)

func sanitizeHTML(value string) string {
	return emailHTMLPolicy.Sanitize(value)
}

func htmlToText(value string) string {
	value = regexp.MustCompile(`(?i)<br\s*/?>|</(p|div|li|tr|h[1-6])>`).ReplaceAllString(value, "\n")
	value = htmlTags.ReplaceAllString(value, "")
	return strings.TrimSpace(strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(value))
}

func Discover(connection model.Connection) (Discovery, error) {
	client, err := authenticatedIMAP(connection)
	if err != nil {
		return Discovery{}, err
	}
	defer client.Close()
	defer client.Logout()
	caps := client.Caps()
	result := Discovery{}
	for capability := range caps {
		result.Capabilities = append(result.Capabilities, string(capability))
	}
	slices.Sort(result.Capabilities)
	options := &imap.ListOptions{}
	if caps.Has(imap.CapSpecialUse) || caps.Has(imap.CapIMAP4rev2) {
		options.ReturnSpecialUse = true
	}
	mailboxes, err := client.List("", "*", options).Collect()
	if err != nil {
		return Discovery{}, fmt.Errorf("list IMAP folders: %w", err)
	}
	for _, mailbox := range mailboxes {
		if strings.EqualFold(mailbox.Mailbox, "INBOX") {
			result.Folders.Inbox = mailbox.Mailbox
		}
		for _, attribute := range mailbox.Attrs {
			switch attribute {
			case imap.MailboxAttrSent:
				result.Folders.Sent = mailbox.Mailbox
			case imap.MailboxAttrDrafts:
				result.Folders.Drafts = mailbox.Mailbox
			case imap.MailboxAttrArchive:
				result.Folders.Archive = mailbox.Mailbox
			case imap.MailboxAttrTrash:
				result.Folders.Trash = mailbox.Mailbox
			case imap.MailboxAttrJunk:
				result.Folders.Junk = mailbox.Mailbox
			}
		}
	}
	if result.Folders.Inbox == "" {
		result.Folders.Inbox = "INBOX"
	}
	return result, nil
}

func SnapshotMessage(connection model.Connection, folder string, uid uint32) (MessagePrecondition, error) {
	client, err := authenticatedIMAP(connection)
	if err != nil {
		return MessagePrecondition{}, err
	}
	defer client.Close()
	defer client.Logout()
	folder = defaultFolder(connection, folder)
	selected, err := client.Select(folder, &imap.SelectOptions{ReadOnly: true, CondStore: client.Caps().Has(imap.CapCondStore)}).Wait()
	if err != nil {
		return MessagePrecondition{}, fmt.Errorf("select IMAP folder %s: %w", folder, err)
	}
	precondition := MessagePrecondition{UIDValidity: selected.UIDValidity}
	options := &imap.FetchOptions{UID: true}
	if client.Caps().Has(imap.CapCondStore) {
		options.ModSeq = true
	}
	items, err := client.Fetch(imap.UIDSetNum(imap.UID(uid)), options).Collect()
	if err != nil {
		return MessagePrecondition{}, fmt.Errorf("fetch IMAP message precondition: %w", err)
	}
	if len(items) != 1 || uint32(items[0].UID) != uid {
		return MessagePrecondition{}, fmt.Errorf("message %s/%d does not exist", folder, uid)
	}
	precondition.ModSeq = items[0].ModSeq
	return precondition, nil
}

func SetFlags(connection model.Connection, folder string, uid uint32, seen, flagged *bool, expected MessagePrecondition) error {
	return SetFlagsContext(context.Background(), connection, folder, uid, seen, flagged, expected)
}

func SetFlagsContext(ctx context.Context, connection model.Connection, folder string, uid uint32, seen, flagged *bool, expected MessagePrecondition) error {
	client, stop, err := authenticatedIMAPContext(ctx, connection)
	if err != nil {
		return err
	}
	defer stop()
	defer client.Close()
	defer client.Logout()
	folder = defaultFolder(connection, folder)
	selected, err := client.Select(folder, &imap.SelectOptions{CondStore: expected.ModSeq != 0}).Wait()
	if err != nil {
		return fmt.Errorf("select IMAP folder %s: %w", folder, err)
	}
	if err := validateSelectedMessage(client, folder, uid, selected.UIDValidity, expected); err != nil {
		return err
	}
	set := imap.UIDSetNum(imap.UID(uid))
	changes := []flagChange{{imap.FlagSeen, seen}, {imap.FlagFlagged, flagged}}
	modSeq := expected.ModSeq
	for index, change := range changes {
		flag, value := change.flag, change.value
		if value == nil {
			continue
		}
		op := imap.StoreFlagsDel
		if *value {
			op = imap.StoreFlagsAdd
		}
		storeOptions := &imap.StoreOptions{UnchangedSince: modSeq}
		if modSeq == 0 {
			storeOptions = nil
		}
		if err := client.Store(set, &imap.StoreFlags{Op: op, Silent: true, Flags: []imap.Flag{flag}}, storeOptions).Close(); err != nil {
			return fmt.Errorf("update IMAP flags: %w", err)
		}
		if modSeq != 0 && hasFlagChange(changes[index+1:]) {
			items, err := client.Fetch(set, &imap.FetchOptions{UID: true, ModSeq: true}).Collect()
			if err != nil || len(items) != 1 {
				return fmt.Errorf("refresh IMAP message precondition after flag update")
			}
			modSeq = items[0].ModSeq
		}
	}
	return nil
}

func hasFlagChange(changes []flagChange) bool {
	for _, change := range changes {
		if change.value != nil {
			return true
		}
	}
	return false
}

func Move(connection model.Connection, folder string, uid uint32, destination string, expected MessagePrecondition) error {
	return MoveContext(context.Background(), connection, folder, uid, destination, expected)
}

func MoveContext(ctx context.Context, connection model.Connection, folder string, uid uint32, destination string, expected MessagePrecondition) error {
	if destination == "" {
		return fmt.Errorf("destination folder is required")
	}
	client, stop, err := authenticatedIMAPContext(ctx, connection)
	if err != nil {
		return err
	}
	defer stop()
	defer client.Close()
	defer client.Logout()
	folder = defaultFolder(connection, folder)
	selected, err := client.Select(folder, &imap.SelectOptions{CondStore: expected.ModSeq != 0}).Wait()
	if err != nil {
		return fmt.Errorf("select IMAP folder %s: %w", folder, err)
	}
	if err := validateSelectedMessage(client, folder, uid, selected.UIDValidity, expected); err != nil {
		return err
	}
	if !client.Caps().Has(imap.CapMove) {
		return fmt.Errorf("IMAP server does not advertise MOVE; refusing unsafe COPY/EXPUNGE fallback")
	}
	if _, err := client.Move(imap.UIDSetNum(imap.UID(uid)), destination).Wait(); err != nil {
		return fmt.Errorf("move IMAP message: %w", err)
	}
	return nil
}

func Append(connection model.Connection, folder string, message model.SendMessage, flags []imap.Flag) (uint32, error) {
	return AppendContext(context.Background(), connection, folder, message, flags)
}

func AppendContext(ctx context.Context, connection model.Connection, folder string, message model.SendMessage, flags []imap.Flag) (uint32, error) {
	data, err := BuildMessage(connection, message)
	if err != nil {
		return 0, fmt.Errorf("build IMAP message: %w", err)
	}
	return AppendSerializedContext(ctx, connection, folder, data, flags)
}

func AppendSerialized(connection model.Connection, folder string, data []byte, flags []imap.Flag) (uint32, error) {
	return AppendSerializedContext(context.Background(), connection, folder, data, flags)
}

func AppendSerializedContext(ctx context.Context, connection model.Connection, folder string, data []byte, flags []imap.Flag) (uint32, error) {
	if folder == "" {
		return 0, fmt.Errorf("append folder is required")
	}
	if len(data) == 0 {
		return 0, fmt.Errorf("serialized message is empty")
	}
	client, stop, err := authenticatedIMAPContext(ctx, connection)
	if err != nil {
		return 0, err
	}
	defer stop()
	defer client.Close()
	defer client.Logout()
	command := client.Append(folder, int64(len(data)), &imap.AppendOptions{Flags: flags, Time: time.Now()})
	if _, err := command.Write(data); err != nil {
		_ = command.Close()
		return 0, fmt.Errorf("write IMAP append: %w", err)
	}
	if err := command.Close(); err != nil {
		return 0, &UncertainAppendError{Err: fmt.Errorf("close IMAP append: %w", err)}
	}
	result, err := command.Wait()
	if err != nil {
		return 0, classifyAppendWaitError(err)
	}
	return uint32(result.UID), nil
}

func classifyAppendWaitError(err error) error {
	wrapped := fmt.Errorf("append IMAP message: %w", err)
	var statusErr *imap.Error
	if errors.As(err, &statusErr) && (statusErr.Type == imap.StatusResponseTypeNo || statusErr.Type == imap.StatusResponseTypeBad) {
		return wrapped
	}
	return &UncertainAppendError{Err: wrapped}
}

func MarkDeleted(connection model.Connection, folder string, uid uint32, expected MessagePrecondition) error {
	return MarkDeletedContext(context.Background(), connection, folder, uid, expected)
}

func MarkDeletedContext(ctx context.Context, connection model.Connection, folder string, uid uint32, expected MessagePrecondition) error {
	client, stop, err := authenticatedIMAPContext(ctx, connection)
	if err != nil {
		return err
	}
	defer stop()
	defer client.Close()
	defer client.Logout()
	selected, err := client.Select(folder, &imap.SelectOptions{CondStore: expected.ModSeq != 0}).Wait()
	if err != nil {
		return fmt.Errorf("select IMAP folder %s: %w", folder, err)
	}
	if err := validateSelectedMessage(client, folder, uid, selected.UIDValidity, expected); err != nil {
		return err
	}
	storeOptions := &imap.StoreOptions{UnchangedSince: expected.ModSeq}
	if expected.ModSeq == 0 {
		storeOptions = nil
	}
	if err := client.Store(imap.UIDSetNum(imap.UID(uid)), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagDeleted}}, storeOptions).Close(); err != nil {
		return fmt.Errorf("mark IMAP draft deleted: %w", err)
	}
	return nil
}

func validateSelectedMessage(client *imapclient.Client, folder string, uid uint32, uidValidity uint32, expected MessagePrecondition) error {
	if expected.UIDValidity == 0 || uidValidity != expected.UIDValidity {
		return fmt.Errorf("mailbox UIDVALIDITY changed; refresh and prepare the operation again")
	}
	options := &imap.FetchOptions{UID: true}
	if expected.ModSeq != 0 {
		options.ModSeq = true
	}
	items, err := client.Fetch(imap.UIDSetNum(imap.UID(uid)), options).Collect()
	if err != nil {
		return fmt.Errorf("validate IMAP message precondition: %w", err)
	}
	if len(items) != 1 || uint32(items[0].UID) != uid {
		return fmt.Errorf("provider message changed; refresh and prepare the operation again")
	}
	if expected.ModSeq != 0 && items[0].ModSeq != expected.ModSeq {
		return fmt.Errorf("provider message changed; refresh and prepare the operation again")
	}
	return nil
}

func defaultFolder(connection model.Connection, folder string) string {
	if folder == "" && connection.Mail != nil {
		folder = connection.Mail.Folders.Inbox
	}
	if folder == "" {
		return "INBOX"
	}
	return folder
}

func authenticatedIMAP(connection model.Connection) (*imapclient.Client, error) {
	client, stop, err := authenticatedIMAPContext(context.Background(), connection)
	if err != nil {
		return nil, err
	}
	stop()
	return client, nil
}

func authenticatedIMAPContext(ctx context.Context, connection model.Connection) (*imapclient.Client, func(), error) {
	if connection.Mail == nil || connection.Mail.IMAP.Address == "" {
		return nil, func() {}, fmt.Errorf("connection %s has no IMAP capability", connection.ID)
	}
	secret, err := config.ResolveSecret(connection.Mail.Secret)
	if err != nil {
		return nil, func() {}, err
	}
	client, err := dialIMAPContext(ctx, connection.Mail.IMAP)
	if err != nil {
		return nil, func() {}, err
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	go func() {
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	if err := client.Login(connection.Mail.Username, secret).Wait(); err != nil {
		stop()
		_ = client.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, func() {}, ctxErr
		}
		return nil, func() {}, fmt.Errorf("authenticate to IMAP: %w", err)
	}
	return client, stop, nil
}

func DoctorSMTP(ctx context.Context, connection model.Connection) error {
	if connection.Mail == nil || connection.Mail.SMTP.Address == "" {
		return nil
	}
	secret, err := config.ResolveSecret(connection.Mail.Secret)
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
	done := make(chan error, 1)
	go func() {
		if ok, _ := client.Extension("AUTH"); !ok {
			done <- fmt.Errorf("SMTP server does not advertise AUTH")
			return
		}
		done <- client.Auth(smtp.PlainAuth("", connection.Mail.Username, secret, host))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("authenticate to SMTP: %w", err)
		}
		return nil
	}
}

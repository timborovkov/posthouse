package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/gmail"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/microsoft"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
	"github.com/timborovkov/posthouse/internal/state"
)

func searchMailContext(ctx context.Context, connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
	switch config.MailKind(connection.Mail) {
	case config.MailKindGmail:
		return gmail.Search(ctx, connection, options)
	case config.MailKindMicrosoft:
		return microsoft.Search(ctx, connection, options)
	default:
		return postmail.SearchContext(ctx, connection, options)
	}
}

func sendSerializedContext(ctx context.Context, connection model.Connection, message model.SendMessage, data []byte) error {
	switch config.MailKind(connection.Mail) {
	case config.MailKindGmail:
		return gmail.Send(ctx, connection, data)
	case config.MailKindMicrosoft:
		return microsoft.Send(ctx, connection, data)
	default:
		return postmail.SendSerializedContext(ctx, connection, message, data)
	}
}

func oauthProvider(connection model.Connection) (string, error) {
	kind := config.NativeKind(connection)
	switch kind {
	case config.MailKindGmail:
		return oauth.ProviderGoogle, nil
	case config.MailKindMicrosoft:
		return oauth.ProviderMicrosoft, nil
	default:
		return "", fmt.Errorf("connection %s is not a Gmail or Microsoft OAuth connection", connection.ID)
	}
}

func (s *Service) AuthorizeConnection(ctx context.Context, id string, device bool) (map[string]any, error) {
	connection, err := s.exactConnection(id, "")
	if err != nil {
		return nil, err
	}
	provider, err := oauthProvider(connection)
	if err != nil {
		return nil, err
	}
	creds, err := oauth.CredentialsFor(provider)
	if err != nil {
		return nil, err
	}
	cfg := oauth.Config{
		Provider:    provider,
		Credentials: creds,
		Endpoint:    oauth.EndpointFor(provider),
		Scopes:      oauth.Scopes(provider, config.NativeMail(connection), config.NativeCalendar(connection)),
	}
	var refresh string
	if s.authorizeOAuth != nil {
		refresh, err = s.authorizeOAuth(ctx, cfg, device)
	} else if device {
		refresh, err = oauth.Device(ctx, cfg)
	} else {
		refresh, err = oauth.Loopback(ctx, cfg)
	}
	if err != nil {
		return nil, err
	}
	keychainName := oauthKeychainName(connection)
	if err := config.SetKeychainSecret(keychainName, refresh); err != nil {
		return nil, err
	}
	err = s.store.Update(func(current model.Config) (model.Config, error) {
		for index := range current.Connections {
			if current.Connections[index].ID != connection.ID {
				continue
			}
			if current.Connections[index].Mail != nil && config.NativeMail(current.Connections[index]) {
				mailConfig := *current.Connections[index].Mail
				mailConfig.Secret = model.SecretRef{Keychain: keychainName}
				mailConfig.SecretEnv = ""
				current.Connections[index].Mail = &mailConfig
			}
			if current.Connections[index].Calendar != nil && config.NativeCalendar(current.Connections[index]) {
				calendarConfig := *current.Connections[index].Calendar
				if current.Connections[index].Mail == nil || !config.NativeMail(current.Connections[index]) {
					calendarConfig.Secret = model.SecretRef{Keychain: keychainName}
				}
				current.Connections[index].Calendar = &calendarConfig
			}
			return current, nil
		}
		return current, fmt.Errorf("connection %s was removed during authorization", connection.ID)
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "connection": connection.ID, "keychain": keychainName}, nil
}

func oauthKeychainName(connection model.Connection) string {
	if connection.Mail != nil && strings.TrimSpace(connection.Mail.Secret.Keychain) != "" {
		return connection.Mail.Secret.Keychain
	}
	if connection.Calendar != nil && strings.TrimSpace(connection.Calendar.Secret.Keychain) != "" {
		return connection.Calendar.Secret.Keychain
	}
	return "posthouse-" + connection.ID
}

func (s *Service) doctorNative(ctx context.Context, connection model.Connection, add func(string, error)) {
	provider, err := oauthProvider(connection)
	if err != nil {
		add("oauth.provider", err)
		return
	}
	secret := ""
	if connection.Mail != nil {
		secret, err = config.ResolveSecret(connection.Mail.Secret)
		add("mail.secret", err)
	}
	if secret == "" && connection.Calendar != nil {
		var calErr error
		secret, calErr = config.ResolveSecret(connection.Calendar.Secret)
		if connection.Mail == nil {
			add("calendar.secret", calErr)
			err = calErr
		} else if calErr == nil && secret != "" {
			err = nil
		}
	}
	if err != nil || secret == "" {
		return
	}
	creds, credErr := oauth.CredentialsFor(provider)
	if credErr != nil {
		add("oauth.client", credErr)
		return
	}
	endpoint := oauth.EndpointFor(provider)
	if provider == oauth.ProviderGoogle && gmail.TokenURL != "" {
		endpoint.TokenURL = gmail.TokenURL
	}
	if provider == oauth.ProviderMicrosoft && microsoft.TokenURL != "" {
		endpoint.TokenURL = microsoft.TokenURL
	}
	token, refreshErr := oauth.Refresh(ctx, oauth.Config{Credentials: creds, Endpoint: endpoint, Scopes: oauth.Scopes(provider, config.NativeMail(connection), config.NativeCalendar(connection))}, secret)
	if refreshErr != nil {
		add("oauth.token", fmt.Errorf("refresh access token failed"))
		return
	}
	if token == nil || token.AccessToken == "" {
		add("oauth.token", fmt.Errorf("refresh access token failed"))
		return
	}
	add("oauth.token", nil)
	if config.NativeMail(connection) {
		connection.Mail.ResolvedSecret = secret
		switch config.MailKind(connection.Mail) {
		case config.MailKindGmail:
			add("gmail.api", gmail.Ping(ctx, connection))
		case config.MailKindMicrosoft:
			add("graph.api", microsoft.Ping(ctx, connection))
		}
	} else if config.NativeCalendar(connection) {
		connection.Calendar.ResolvedSecret = secret
		switch config.CalendarKind(connection.Calendar) {
		case config.CalendarKindGmail:
			_, pingErr := gmail.ListEvents(ctx, connection, s.now(), s.now().Add(time.Hour), "")
			add("gmail.calendar", pingErr)
		case config.CalendarKindMicrosoft:
			_, pingErr := microsoft.ListEvents(ctx, connection, s.now(), s.now().Add(time.Hour), "")
			add("graph.calendar", pingErr)
		}
	}
}

func executeNativeMail(ctx context.Context, connection model.Connection, kind string, payload json.RawMessage, build func(model.Connection, model.SendMessage) ([]byte, error)) (map[string]any, error) {
	switch kind {
	case "mail.send":
		var message model.SendMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, err
		}
		data, err := build(connection, message)
		if err != nil {
			return nil, err
		}
		var sendErr error
		switch config.MailKind(connection.Mail) {
		case config.MailKindGmail:
			sendErr = gmail.Send(ctx, connection, data)
		default:
			sendErr = microsoft.Send(ctx, connection, data)
		}
		if sendErr != nil {
			return nil, sendErr
		}
		return map[string]any{"sent": true}, nil
	case "mail.mark", "mail.move", "mail.archive", "mail.trash":
		var action MailAction
		if err := json.Unmarshal(payload, &action); err != nil {
			return nil, err
		}
		id := nativeMessageID(action)
		if id == "" {
			return nil, fmt.Errorf("message id is required")
		}
		switch kind {
		case "mail.mark":
			if err := nativeMark(ctx, connection, id, action.Seen, action.Flagged); err != nil {
				return nil, err
			}
			return map[string]any{"updated": true}, nil
		case "mail.archive":
			if err := nativeArchive(ctx, connection, id); err != nil {
				return nil, err
			}
			return map[string]any{"moved": true, "destination": "ARCHIVE"}, nil
		case "mail.trash":
			if err := nativeTrash(ctx, connection, id); err != nil {
				return nil, err
			}
			return map[string]any{"moved": true, "destination": "TRASH"}, nil
		default:
			if err := nativeMove(ctx, connection, id, action.Destination); err != nil {
				return nil, err
			}
			return map[string]any{"moved": true, "destination": action.Destination}, nil
		}
	case "mail.draft.create", "mail.draft.update", "mail.draft.delete":
		var draft draftPayload
		if err := json.Unmarshal(payload, &draft); err != nil {
			return nil, err
		}
		id := nativeMessageID(MailAction{ID: draft.ID, Folder: draft.Folder, UID: draft.UID})
		if kind == "mail.draft.delete" {
			if err := nativeTrash(ctx, connection, id); err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true}, nil
		}
		data, err := build(connection, draft.Message)
		if err != nil {
			return nil, err
		}
		created, err := nativeCreateDraft(ctx, connection, data)
		if err != nil {
			return nil, err
		}
		if kind == "mail.draft.update" && id != "" {
			if err := nativeTrash(ctx, connection, id); err != nil {
				return map[string]any{"id": created, "cleanup": "failed"}, &uncertainOperationError{message: fmt.Sprintf("replacement draft was created as %s but old draft cleanup failed: %v", created, err)}
			}
		}
		return map[string]any{"id": created}, nil
	default:
		return nil, fmt.Errorf("unsupported prepared operation kind %q", kind)
	}
}

func executeNativeCalendar(ctx context.Context, connection model.Connection, kind string, payload json.RawMessage) (map[string]any, error) {
	switch kind {
	case "calendar.create", "calendar.update":
		var mutation calendarWritePayload
		if err := json.Unmarshal(payload, &mutation); err != nil {
			return nil, err
		}
		if mutation.Event.ID == "" {
			if err := json.Unmarshal(payload, &mutation.Event); err != nil {
				return nil, err
			}
		}
		written, err := nativePutEvent(ctx, connection, mutation.Event, kind == "calendar.create")
		if err != nil {
			return nil, err
		}
		return map[string]any{"event": written}, nil
	case "calendar.delete":
		var deletion calendarDeletePayload
		if err := json.Unmarshal(payload, &deletion); err != nil {
			return nil, err
		}
		id := deletion.Href
		if id == "" {
			return nil, fmt.Errorf("calendar delete requires the provider event id")
		}
		if err := nativeDeleteEvent(ctx, connection, id); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	default:
		return nil, fmt.Errorf("unsupported prepared operation kind %q", kind)
	}
}

func nativeMessageID(action MailAction) string {
	if strings.TrimSpace(action.ID) != "" {
		return strings.TrimSpace(action.ID)
	}
	return ""
}

func nativeMark(ctx context.Context, connection model.Connection, id string, seen, flagged *bool) error {
	switch config.MailKind(connection.Mail) {
	case config.MailKindGmail:
		var add, remove []string
		if seen != nil {
			if *seen {
				remove = append(remove, "UNREAD")
			} else {
				add = append(add, "UNREAD")
			}
		}
		if flagged != nil {
			if *flagged {
				add = append(add, "STARRED")
			} else {
				remove = append(remove, "STARRED")
			}
		}
		return gmail.Modify(ctx, connection, id, add, remove)
	default:
		return microsoft.Mark(ctx, connection, id, seen, flagged)
	}
}

func nativeArchive(ctx context.Context, connection model.Connection, id string) error {
	if config.MailKind(connection.Mail) == config.MailKindGmail {
		return gmail.Archive(ctx, connection, id)
	}
	return microsoft.Move(ctx, connection, id, "archive")
}

func nativeTrash(ctx context.Context, connection model.Connection, id string) error {
	if config.MailKind(connection.Mail) == config.MailKindGmail {
		return gmail.Trash(ctx, connection, id)
	}
	return microsoft.Move(ctx, connection, id, "deleteditems")
}

func nativeMove(ctx context.Context, connection model.Connection, id, destination string) error {
	if config.MailKind(connection.Mail) == config.MailKindGmail {
		label := strings.ToUpper(strings.TrimSpace(destination))
		if label == "" {
			label = "INBOX"
		}
		return gmail.Modify(ctx, connection, id, []string{label}, []string{"INBOX"})
	}
	return microsoft.Move(ctx, connection, id, destination)
}

func nativeCreateDraft(ctx context.Context, connection model.Connection, raw []byte) (string, error) {
	if config.MailKind(connection.Mail) == config.MailKindGmail {
		return gmail.CreateDraft(ctx, connection, raw)
	}
	return microsoft.CreateDraft(ctx, connection, raw)
}

func nativeGetMessage(ctx context.Context, connection model.Connection, id string) (postmail.FetchedMessage, error) {
	switch config.MailKind(connection.Mail) {
	case config.MailKindGmail:
		return gmail.Get(ctx, connection, id)
	case config.MailKindMicrosoft:
		return microsoft.Get(ctx, connection, id)
	default:
		return postmail.FetchedMessage{}, fmt.Errorf("connection %s is not a native mail backend", connection.ID)
	}
}

func nativePutEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	switch config.CalendarKind(connection.Calendar) {
	case config.CalendarKindGmail:
		return gmail.PutEvent(ctx, connection, event, create)
	default:
		return microsoft.PutEvent(ctx, connection, event, create)
	}
}

func nativeDeleteEvent(ctx context.Context, connection model.Connection, id string) error {
	switch config.CalendarKind(connection.Calendar) {
	case config.CalendarKindGmail:
		return gmail.DeleteEvent(ctx, connection, id)
	default:
		return microsoft.DeleteEvent(ctx, connection, id)
	}
}

func nativeBodyCacheKey(cacheID, id string) string {
	return framedCacheKey(cacheID, "id", id)
}

func (s *Service) getNativeMessageMode(ctx context.Context, connection model.Connection, loc MessageLocator, mode string) (model.MessageDetail, error) {
	var err error
	if mode != "offline" {
		connection, err = resolveMailConnection(connection)
		if err != nil && mode == "refresh" {
			return model.MessageDetail{}, err
		}
	}
	if mode != "offline" && err == nil {
		fetched, fetchErr := nativeGetMessage(ctx, connection, loc.ID)
		if fetchErr == nil {
			if requestCacheID := mailCacheID(connection); requestCacheID != "" {
				if ledger, stateErr := s.ensureState(); stateErr == nil {
					data, _ := json.Marshal(fetched.Detail)
					_ = ledger.Put(ctx, state.CacheEntry{Namespace: "message_body", Key: nativeBodyCacheKey(requestCacheID, loc.ID), ConnectionID: connection.ID, Kind: "message_body", ProviderID: loc.ID, ExpiresAt: s.now().Add(s.messageBodyTTL()), Value: data})
				}
			}
			return fetched.Detail, nil
		}
		if mode == "refresh" {
			return model.MessageDetail{}, fetchErr
		}
		err = fetchErr
	}
	if cached, ok := s.cachedNativeMessage(connection, loc.ID); ok {
		cached.Stale = true
		return cached, nil
	}
	if err != nil {
		return model.MessageDetail{}, err
	}
	return model.MessageDetail{}, fmt.Errorf("no cached message body for %s", loc.ID)
}

func (s *Service) cachedNativeMessage(connection model.Connection, id string) (model.MessageDetail, bool) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.MessageDetail{}, false
	}
	cacheID := mailCacheID(connection)
	if cacheID == "" {
		return model.MessageDetail{}, false
	}
	entry, ok, err := ledger.Get(context.Background(), "message_body", nativeBodyCacheKey(cacheID, id), false)
	if err != nil || !ok {
		return model.MessageDetail{}, false
	}
	var detail model.MessageDetail
	if json.Unmarshal(entry.Value, &detail) != nil {
		return model.MessageDetail{}, false
	}
	detail.CachedAt = entry.CachedAt
	return detail, true
}

func (s *Service) getNativeAttachment(ctx context.Context, connection model.Connection, loc MessageLocator, attachmentID, mode string) (model.Attachment, []byte, error) {
	detail, err := s.getNativeMessageMode(ctx, connection, loc, mode)
	if err != nil {
		return model.Attachment{}, nil, err
	}
	fetched, fetchErr := nativeGetMessage(ctx, connection, loc.ID)
	if mode != "offline" && fetchErr == nil {
		data, ok := fetched.Attachments[attachmentID]
		if !ok {
			return model.Attachment{}, nil, fmt.Errorf("no cached attachment %q", attachmentID)
		}
		for _, attachment := range fetched.Detail.Attachments {
			if attachment.ID == attachmentID {
				return attachment, data, nil
			}
		}
		return model.Attachment{ID: attachmentID, Size: int64(len(data))}, data, nil
	}
	for _, attachment := range detail.Attachments {
		if attachment.ID == attachmentID {
			return attachment, nil, fmt.Errorf("no cached attachment %q", attachmentID)
		}
	}
	if fetchErr != nil {
		return model.Attachment{}, nil, fetchErr
	}
	return model.Attachment{}, nil, fmt.Errorf("no cached attachment %q", attachmentID)
}

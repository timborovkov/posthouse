package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/timborovkov/posthouse/internal/calendar"
	"github.com/timborovkov/posthouse/internal/config"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/pagination"
	"github.com/timborovkov/posthouse/internal/selector"
	"github.com/timborovkov/posthouse/internal/state"
)

type Service struct {
	store                  *config.Store
	calendar               *calendar.Client
	mailSearch             func(model.Connection, postmail.SearchOptions) (postmail.SearchResult, error)
	mailSearchContext      func(context.Context, model.Connection, postmail.SearchOptions) (postmail.SearchResult, error)
	mailSnapshot           func(model.Connection, string, uint32) (postmail.MessagePrecondition, error)
	mailSnapshotContext    func(context.Context, model.Connection, string, uint32) (postmail.MessagePrecondition, error)
	mailAppend             func(model.Connection, string, model.SendMessage, []imap.Flag) (uint32, error)
	mailAppendContext      func(context.Context, model.Connection, string, model.SendMessage, []imap.Flag) (uint32, error)
	mailBuild              func(model.Connection, model.SendMessage) ([]byte, error)
	mailSendRaw            func(model.Connection, model.SendMessage, []byte) error
	mailSendRawContext     func(context.Context, model.Connection, model.SendMessage, []byte) error
	mailAppendRaw          func(model.Connection, string, []byte, []imap.Flag) (uint32, error)
	mailAppendRawContext   func(context.Context, model.Connection, string, []byte, []imap.Flag) (uint32, error)
	mailMarkDeleted        func(model.Connection, string, uint32, postmail.MessagePrecondition) error
	mailMarkDeletedContext func(context.Context, model.Connection, string, uint32, postmail.MessagePrecondition) error
	stateMu                sync.Mutex
	state                  *state.Store
	stateErr               error
	now                    func() time.Time
}

func New(store *config.Store) *Service {
	return &Service{
		store: store, calendar: calendar.NewClient(nil), mailSearchContext: postmail.SearchContext,
		mailSnapshotContext: postmail.SnapshotMessageContext,
		mailAppendContext:   postmail.AppendContext, mailMarkDeletedContext: postmail.MarkDeletedContext,
		mailBuild: postmail.BuildMessage, mailSendRawContext: postmail.SendSerializedContext, mailAppendRawContext: postmail.AppendSerializedContext,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Close() error {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != nil {
		return s.state.Close()
	}
	return nil
}

func (s *Service) ensureState() (*state.Store, error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.state != nil {
		return s.state, nil
	}
	cfg, err := s.store.Load()
	if err != nil {
		s.stateErr = err
		return nil, err
	}
	opened, err := state.Open(state.DefaultPath(s.store.Path(), cfg.Cache.Path), cfg.Cache.MaxBytes)
	if err != nil {
		s.stateErr = err
		return nil, err
	}
	s.state = opened
	s.stateErr = nil
	return opened, nil
}

func (s *Service) Ready(ctx context.Context) error {
	if _, err := s.store.Load(); err != nil {
		return err
	}
	_, err := s.ensureState()
	return err
}

func (s *Service) ConfigPath() string {
	return s.store.Path()
}

func (s *Service) Connections(selection model.Selector) ([]model.Connection, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	if len(selection.Connections) == 0 && selection.Category == "" && len(selection.Labels) == 0 && len(selection.Collections) == 0 && selection.Capability == "" {
		result := make([]model.Connection, 0, len(cfg.Connections))
		for _, connection := range cfg.Connections {
			if !connection.Disabled {
				result = append(result, publicConnection(connection))
			}
		}
		return result, nil
	}
	matches, err := selector.Match(cfg.Connections, selection)
	if err != nil {
		return nil, err
	}
	for index := range matches {
		matches[index] = publicConnection(matches[index])
	}
	return matches, nil
}

func (s *Service) ListConnections(selection model.Selector, requestedPageSize int, cursor string) (model.ConnectionPage, error) {
	pageSize, err := pagination.PageSize(requestedPageSize, 50, 200)
	if err != nil {
		return model.ConnectionPage{}, err
	}
	connections, err := s.Connections(selection)
	if err != nil {
		return model.ConnectionPage{}, err
	}
	slices.SortFunc(connections, func(a, b model.Connection) int { return strings.Compare(a.ID, b.ID) })
	connectionIDs := make([]string, len(connections))
	for index, connection := range connections {
		connectionIDs[index] = connection.ID
	}
	scope := struct {
		Selector      model.Selector `json:"selector"`
		ConnectionIDs []string       `json:"connection_ids"`
	}{Selector: selection, ConnectionIDs: connectionIDs}
	position := struct {
		AfterID string `json:"after_id"`
	}{}
	if err := pagination.Decode(cursor, "connections", scope, &position); err != nil {
		return model.ConnectionPage{}, err
	}
	start := 0
	if position.AfterID != "" {
		start = len(connections)
		for index, connection := range connections {
			if connection.ID > position.AfterID {
				start = index
				break
			}
		}
	}
	end := min(start+pageSize, len(connections))
	page := model.ConnectionPage{Connections: connections[start:end]}
	if end < len(connections) {
		position.AfterID = page.Connections[len(page.Connections)-1].ID
		page.NextCursor, err = pagination.Encode("connections", scope, position)
		if err != nil {
			return model.ConnectionPage{}, err
		}
	}
	return page, nil
}

func (s *Service) UpsertConnection(connection model.Connection, replace bool) error {
	cfg, err := s.store.Load()
	if err != nil {
		return err
	}
	for index, existing := range cfg.Connections {
		if existing.ID != connection.ID {
			continue
		}
		if !replace {
			return fmt.Errorf("connection %s already exists; pass --replace to update it", connection.ID)
		}
		cfg.Connections[index] = connection
		return s.store.Save(cfg)
	}
	cfg.Connections = append(cfg.Connections, connection)
	return s.store.Save(cfg)
}

func (s *Service) RemoveConnection(id string) error {
	cfg, err := s.store.Load()
	if err != nil {
		return err
	}
	for index, connection := range cfg.Connections {
		if connection.ID == id {
			cfg.Connections = append(cfg.Connections[:index], cfg.Connections[index+1:]...)
			return s.store.Save(cfg)
		}
	}
	return fmt.Errorf("connection %s does not exist", id)
}

type mailCursorState struct {
	UIDValidity uint32    `json:"uid_validity"`
	UIDNext     uint32    `json:"uid_next,omitempty"`
	BeforeTime  time.Time `json:"before_time,omitempty"`
	BeforeUID   uint32    `json:"before_uid,omitempty"`
}

type mailCursorPosition struct {
	Connections map[string]mailCursorState   `json:"connections"`
	Failed      map[string]model.SourceError `json:"failed,omitempty"`
}

func (s *Service) SearchMessages(selection model.Selector, options postmail.SearchOptions, requestedPageSize int, cursor string) (model.MessagePage, error) {
	return s.SearchMessagesContext(context.Background(), selection, options, requestedPageSize, cursor)
}

func (s *Service) SearchMessagesContext(ctx context.Context, selection model.Selector, options postmail.SearchOptions, requestedPageSize int, cursor string) (model.MessagePage, error) {
	pageSize, err := pagination.PageSize(requestedPageSize, 25, 100)
	if err != nil {
		return model.MessagePage{}, err
	}
	cfg, err := s.store.Load()
	if err != nil {
		return model.MessagePage{}, err
	}
	selection.Capability = "mail.read"
	connections, err := selector.Match(cfg.Connections, selection)
	if err != nil {
		return model.MessagePage{}, err
	}
	slices.SortFunc(connections, func(a, b model.Connection) int { return strings.Compare(a.ID, b.ID) })
	connectionIDs := make([]string, len(connections))
	for index, connection := range connections {
		connectionIDs[index] = connection.ID
	}
	scope := struct {
		Selector      model.Selector `json:"selector"`
		ConnectionIDs []string       `json:"connection_ids"`
		Folder        string         `json:"folder"`
		Query         string         `json:"query"`
		Since         time.Time      `json:"since"`
		Before        time.Time      `json:"before"`
		Unread        bool           `json:"unread"`
	}{selection, connectionIDs, options.Folder, options.Query, options.Since, options.Before, options.Unread}
	position := mailCursorPosition{Connections: make(map[string]mailCursorState, len(connections)), Failed: make(map[string]model.SourceError)}
	if err := pagination.Decode(cursor, "messages", scope, &position); err != nil {
		return model.MessagePage{}, err
	}
	results := make(map[string]postmail.SearchResult, len(connections))
	pageErrors := make([]model.SourceError, 0)
	for _, connection := range connections {
		if failed, ok := position.Failed[connection.ID]; ok && cursor != "" {
			pageErrors = append(pageErrors, failed)
			continue
		}
		state, hasState := position.Connections[connection.ID]
		if cursor != "" && !hasState {
			return model.MessagePage{}, fmt.Errorf("cursor is missing state for connection %s", connection.ID)
		}
		connectionOptions := options
		connectionOptions.Limit = pageSize + 1
		connectionOptions.CursorTime = state.BeforeTime
		connectionOptions.CursorUID = state.BeforeUID
		connectionOptions.MaxUIDExclusive = state.UIDNext
		connectionOptions.ExpectedUIDValidity = state.UIDValidity
		var result postmail.SearchResult
		var err error
		if options.Mode == "offline" {
			var ok bool
			result, ok = s.cachedMailResult(connection.ID, mailFolder(connection, connectionOptions.Folder), scope, connectionOptions, state, connectionOptions.Limit)
			if !ok {
				err = fmt.Errorf("no cached result for this source and query")
			}
		} else {
			if s.mailSearch != nil {
				result, err = s.mailSearch(connection, connectionOptions)
			} else {
				result, err = s.mailSearchContext(ctx, connection, connectionOptions)
			}
		}
		if err != nil {
			sourceError := sourceError(connection.ID, "mail_unavailable", err)
			if options.Mode != "offline" && options.Mode != "refresh" {
				if cached, ok := s.cachedMailResult(connection.ID, mailFolder(connection, connectionOptions.Folder), scope, connectionOptions, state, connectionOptions.Limit); ok {
					result = cached
					sourceError.Stale = true
					for index := range result.Messages {
						result.Messages[index].Stale = true
					}
					pageErrors = append(pageErrors, sourceError)
				} else {
					position.Failed[connection.ID] = sourceError
					pageErrors = append(pageErrors, sourceError)
					continue
				}
			} else {
				position.Failed[connection.ID] = sourceError
				pageErrors = append(pageErrors, sourceError)
				continue
			}
		} else if options.Mode != "offline" {
			if cacheErr := s.cacheMailResult(connection.ID, mailFolder(connection, connectionOptions.Folder), scope, connectionOptions, result); cacheErr != nil {
				pageErrors = append(pageErrors, sourceError(connection.ID, "cache_write_failed", cacheErr))
			}
		} else {
			for index := range result.Messages {
				result.Messages[index].Stale = true
			}
		}
		results[connection.ID] = result
		state.UIDValidity = result.UIDValidity
		if state.UIDNext == 0 {
			state.UIDNext = result.UIDNext
		}
		position.Connections[connection.ID] = state
	}
	indices := make(map[string]int, len(connections))
	consumedLast := make(map[string]model.Message, len(connections))
	page := model.MessagePage{Messages: make([]model.Message, 0, pageSize), Errors: pageErrors}
	for len(page.Messages) < pageSize {
		bestConnection := ""
		var best model.Message
		for _, connection := range connections {
			result := results[connection.ID]
			index := indices[connection.ID]
			if index >= len(result.Messages) {
				continue
			}
			candidate := result.Messages[index]
			if bestConnection == "" || messageBefore(candidate, best) {
				bestConnection, best = connection.ID, candidate
			}
		}
		if bestConnection == "" {
			break
		}
		page.Messages = append(page.Messages, best)
		indices[bestConnection]++
		consumedLast[bestConnection] = best
	}
	hasMore := false
	for _, connection := range connections {
		if _, ok := results[connection.ID]; !ok {
			continue
		}
		result := results[connection.ID]
		if indices[connection.ID] < len(result.Messages) || result.HasMore {
			hasMore = true
		}
		if last, ok := consumedLast[connection.ID]; ok {
			state := position.Connections[connection.ID]
			state.BeforeTime = messageTime(last)
			state.BeforeUID = last.UID
			position.Connections[connection.ID] = state
		}
	}
	if len(results) == 0 {
		return model.MessagePage{}, fmt.Errorf("all selected mail connections failed")
	}
	if hasMore {
		page.NextCursor, err = pagination.Encode("messages", scope, position)
		if err != nil {
			return model.MessagePage{}, err
		}
	}
	return page, nil
}

func (s *Service) SendMessage(message model.SendMessage) error {
	cfg, err := s.store.Load()
	if err != nil {
		return err
	}
	connection, err := selector.One(cfg.Connections, message.ConnectionID, "mail.send")
	if err != nil {
		return err
	}
	return postmail.Send(connection, message)
}

type eventCursorPosition struct {
	Start        time.Time                    `json:"start"`
	ConnectionID string                       `json:"connection_id"`
	CollectionID string                       `json:"collection_id,omitempty"`
	ID           string                       `json:"id"`
	Href         string                       `json:"href,omitempty"`
	RecurrenceID string                       `json:"recurrence_id,omitempty"`
	Snapshot     string                       `json:"snapshot"`
	Failed       map[string]model.SourceError `json:"failed,omitempty"`
	RangeStart   time.Time                    `json:"range_start"`
	RangeEnd     time.Time                    `json:"range_end"`
}

func (s *Service) ListEvents(ctx context.Context, selection model.Selector, start, end time.Time, query string, requestedPageSize int, cursor string) (model.EventPage, error) {
	return s.ListEventsMode(ctx, selection, start, end, query, requestedPageSize, cursor, "")
}

func (s *Service) GetEvent(ctx context.Context, selection model.Selector, start, end time.Time, id string) (model.Event, error) {
	cursor := ""
	var match *model.Event
	for {
		page, err := s.ListEvents(ctx, selection, start, end, "", 500, cursor)
		if err != nil {
			return model.Event{}, err
		}
		for _, event := range page.Events {
			if event.ID == id {
				if match != nil {
					return model.Event{}, fmt.Errorf("event %q is ambiguous across the selected sources; select one connection and collection", id)
				}
				matched := event
				match = &matched
			}
		}
		if page.NextCursor == "" {
			if match != nil {
				return *match, nil
			}
			return model.Event{}, fmt.Errorf("event %q was not found in the selected range", id)
		}
		cursor = page.NextCursor
		start, end = time.Time{}, time.Time{}
	}
}

func (s *Service) ListEventsMode(ctx context.Context, selection model.Selector, start, end time.Time, query string, requestedPageSize int, cursor, mode string) (model.EventPage, error) {
	pageSize, err := pagination.PageSize(requestedPageSize, 100, 500)
	if err != nil {
		return model.EventPage{}, err
	}
	cfg, err := s.store.Load()
	if err != nil {
		return model.EventPage{}, err
	}
	selection.Capability = "calendar.read"
	connections, err := selector.Match(cfg.Connections, selection)
	if err != nil {
		return model.EventPage{}, err
	}
	slices.SortFunc(connections, func(a, b model.Connection) int { return strings.Compare(a.ID, b.ID) })
	connectionIDs := make([]string, len(connections))
	for index, connection := range connections {
		connectionIDs[index] = connection.ID
	}
	position := eventCursorPosition{Failed: make(map[string]model.SourceError)}
	if cursor != "" {
		if err := pagination.DecodePosition(cursor, "events", &position); err != nil {
			return model.EventPage{}, err
		}
		if start.IsZero() {
			start = position.RangeStart
		}
		if end.IsZero() {
			end = position.RangeEnd
		}
	}
	start, end = normalizeEventRange(start, end, s.now())
	scope := struct {
		Selector      model.Selector `json:"selector"`
		ConnectionIDs []string       `json:"connection_ids"`
		Start         time.Time      `json:"start"`
		End           time.Time      `json:"end"`
		Query         string         `json:"query"`
	}{selection, connectionIDs, start, end, query}
	if err := pagination.Decode(cursor, "events", scope, &position); err != nil {
		return model.EventPage{}, err
	}
	position.RangeStart, position.RangeEnd = start, end
	var result []model.Event
	var pageErrors []model.SourceError
	successfulSources := 0
	for _, connection := range connections {
		if failed, ok := position.Failed[connection.ID]; ok && cursor != "" {
			pageErrors = append(pageErrors, failed)
			continue
		}
		var events []model.Event
		var err error
		partialResult := false
		if mode == "offline" {
			var ok bool
			events, ok = s.cachedEvents(connection, scope, selection.Collections, start, end, query)
			if !ok {
				err = fmt.Errorf("no cached result for this source and query")
			}
		} else if connection.Calendar != nil && connection.Calendar.Kind == "caldav" {
			events, err = s.calendar.ListCalDAV(ctx, connection, selection.Collections, start, end, query)
		} else {
			events, err = s.calendar.List(ctx, connection, start, end, query)
		}
		var partial *calendar.PartialError
		if errors.As(err, &partial) {
			pageErrors = append(pageErrors, partial.Errors...)
			if partial.SuccessfulCollections > 0 {
				partialResult = true
				err = nil
			}
		}
		if err != nil {
			sourceError := sourceError(connection.ID, "calendar_unavailable", err)
			if mode != "offline" && mode != "refresh" {
				if cached, ok := s.cachedEvents(connection, scope, selection.Collections, start, end, query); ok {
					events = cached
					successfulSources++
					sourceError.Stale = true
					for index := range events {
						events[index].Stale = true
					}
					pageErrors = append(pageErrors, sourceError)
				} else {
					position.Failed[connection.ID] = sourceError
					pageErrors = append(pageErrors, sourceError)
					continue
				}
			} else {
				position.Failed[connection.ID] = sourceError
				pageErrors = append(pageErrors, sourceError)
				continue
			}
		} else if mode != "offline" {
			successfulSources++
			replaceIndex := !partialResult && strings.TrimSpace(query) == "" && len(selection.Collections) == 0
			if cacheErr := s.cacheEvents(connection.ID, scope, events, start, end, !partialResult, replaceIndex); cacheErr != nil {
				pageErrors = append(pageErrors, sourceError(connection.ID, "cache_write_failed", cacheErr))
			}
		} else {
			successfulSources++
			for index := range events {
				events[index].Stale = true
			}
		}
		result = append(result, events...)
	}
	if successfulSources == 0 {
		return model.EventPage{}, fmt.Errorf("all selected calendar connections failed")
	}
	slices.SortFunc(result, compareEvents)
	snapshotEvents := append([]model.Event(nil), result...)
	for index := range snapshotEvents {
		// Cache provenance is response metadata, not provider content. Including it
		// would invalidate every live traversal because cacheEvents stamps now.
		snapshotEvents[index].CachedAt = time.Time{}
		snapshotEvents[index].Stale = false
	}
	snapshot, err := pagination.Fingerprint(snapshotEvents)
	if err != nil {
		return model.EventPage{}, err
	}
	if position.Snapshot != "" && position.Snapshot != snapshot {
		return model.EventPage{}, fmt.Errorf("event sources changed; restart pagination")
	}
	position.Snapshot = snapshot
	startIndex := 0
	if !position.Start.IsZero() {
		startIndex = len(result)
		cursorEvent := model.Event{Start: position.Start, ConnectionID: position.ConnectionID, CollectionID: position.CollectionID, ID: position.ID, Href: position.Href, RecurrenceID: position.RecurrenceID}
		for index, event := range result {
			if compareEvents(event, cursorEvent) > 0 {
				startIndex = index
				break
			}
		}
	}
	endIndex := min(startIndex+pageSize, len(result))
	page := model.EventPage{Events: result[startIndex:endIndex], Errors: pageErrors}
	if endIndex < len(result) {
		last := page.Events[len(page.Events)-1]
		position.Start, position.ConnectionID, position.CollectionID, position.ID, position.Href, position.RecurrenceID = last.Start, last.ConnectionID, last.CollectionID, last.ID, last.Href, last.RecurrenceID
		page.NextCursor, err = pagination.Encode("events", scope, position)
		if err != nil {
			return model.EventPage{}, err
		}
	}
	return page, nil
}

func (s *Service) GenerateICS(event model.Event) (model.Event, string, error) {
	return calendar.Generate(event)
}

type MailAction struct {
	Folder       string                       `json:"folder"`
	UID          uint32                       `json:"uid"`
	Destination  string                       `json:"destination,omitempty"`
	Seen         *bool                        `json:"seen,omitempty"`
	Flagged      *bool                        `json:"flagged,omitempty"`
	Precondition postmail.MessagePrecondition `json:"precondition"`
}

type draftPayload struct {
	Folder       string                       `json:"folder"`
	UID          uint32                       `json:"uid,omitempty"`
	Message      model.SendMessage            `json:"message"`
	Precondition postmail.MessagePrecondition `json:"precondition,omitempty"`
}

type calendarDeletePayload struct {
	CollectionID string `json:"collection_id"`
	Href         string `json:"href"`
	ETag         string `json:"etag"`
}

type uncertainOperationError struct{ message string }

func (err *uncertainOperationError) Error() string { return err.message }

func (s *Service) PrepareSend(ctx context.Context, message model.SendMessage) (model.PreparedOperation, error) {
	connection, err := s.exactConnection(message.ConnectionID, "mail.send")
	if err != nil {
		return model.PreparedOperation{}, err
	}
	if connection.Mail.SMTP.Address == "" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s cannot send mail", connection.ID)
	}
	if len(message.To)+len(message.CC)+len(message.BCC) == 0 {
		return model.PreparedOperation{}, fmt.Errorf("at least one recipient is required")
	}
	if connection.Mail.SentCopy == "always" && connection.Mail.Folders.Sent == "" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s requires a sent-copy folder; run connection discover or configure folders.sent", connection.ID)
	}
	message.ConnectionID = connection.ID
	if err := postmail.ValidateMessage(message); err != nil {
		return model.PreparedOperation{}, fmt.Errorf("validate message: %w", err)
	}
	return s.prepare(ctx, "mail.send", connection, message, map[string]any{
		"acting_identity": connection.Identity,
		"recipients":      map[string]any{"to": message.To, "cc": message.CC, "bcc": message.BCC},
		"subject":         message.Subject, "text": message.Text, "reply_to": message.ReplyTo,
		"in_reply_to": message.InReplyTo, "references": message.References,
		"attachments":  attachmentPreviews(message.Attachments),
		"side_effects": []string{"send SMTP message", sentCopyEffect(connection)},
	})
}

func (s *Service) PrepareReply(ctx context.Context, connectionID, folder string, uid uint32, text string) (model.PreparedOperation, error) {
	original, err := s.GetMessage(connectionID, folder, uid)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	recipients := replyRecipients(original)
	return s.PrepareSend(ctx, model.SendMessage{
		ConnectionID: connectionID,
		To:           recipients,
		Subject:      prefixedSubject(original.Subject, "Re:"),
		Text:         quotedBody(text, original.Text),
		InReplyTo:    original.MessageID,
		References:   append(append([]string(nil), original.References...), original.MessageID),
	})
}

func (s *Service) PrepareForward(ctx context.Context, connectionID, folder string, uid uint32, recipients []string, text string) (model.PreparedOperation, error) {
	if len(recipients) == 0 {
		return model.PreparedOperation{}, fmt.Errorf("forward requires at least one recipient")
	}
	original, err := s.GetMessage(connectionID, folder, uid)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	return s.PrepareSend(ctx, model.SendMessage{
		ConnectionID: connectionID,
		To:           recipients,
		Subject:      prefixedSubject(original.Subject, "Fwd:"),
		Text:         quotedBody(text, original.Text),
	})
}

func messageAddresses(addresses []model.Address) []string {
	result := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if address.Email != "" {
			result = append(result, address.Email)
		}
	}
	return result
}

func replyRecipients(original model.MessageDetail) []string {
	recipients := messageAddresses(original.ReplyTo)
	if len(recipients) == 0 {
		recipients = messageAddresses(original.From)
	}
	return recipients
}

func quotedBody(text, original string) string {
	if original == "" {
		return text
	}
	return text + "\n\n--- original message ---\n" + original
}

func prefixedSubject(subject, prefix string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), strings.ToLower(prefix)) {
		return subject
	}
	if subject == "" {
		return prefix
	}
	return prefix + " " + subject
}

func (s *Service) PrepareMailAction(ctx context.Context, connectionID, kind string, payload MailAction) (model.PreparedOperation, error) {
	connection, err := s.exactConnection(connectionID, "mail.read")
	if err != nil {
		return model.PreparedOperation{}, err
	}
	switch kind {
	case "mail.mark":
		if payload.Seen == nil && payload.Flagged == nil {
			return model.PreparedOperation{}, fmt.Errorf("mark operation needs seen or flagged state")
		}
	case "mail.move":
		if payload.Destination == "" {
			return model.PreparedOperation{}, fmt.Errorf("move destination is required")
		}
	case "mail.archive":
		payload.Destination = connection.Mail.Folders.Archive
	case "mail.trash":
		payload.Destination = connection.Mail.Folders.Trash
	default:
		return model.PreparedOperation{}, fmt.Errorf("unsupported mail action %q", kind)
	}
	if (kind == "mail.archive" || kind == "mail.trash") && payload.Destination == "" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s has no discovered destination folder; run connection discover", connection.ID)
	}
	payload.Precondition, err = s.snapshotMessage(ctx, connection, payload.Folder, payload.UID)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	return s.prepare(ctx, kind, connection, payload, map[string]any{
		"acting_identity": connection.Identity, "folder": payload.Folder, "uid": payload.UID,
		"destination": payload.Destination, "seen": payload.Seen, "flagged": payload.Flagged,
		"side_effects": []string{"modify one provider message"},
	})
}

func (s *Service) PrepareDraft(ctx context.Context, connectionID, kind string, folder string, uid uint32, message model.SendMessage) (model.PreparedOperation, error) {
	connection, err := s.exactConnection(connectionID, "mail.read")
	if err != nil {
		return model.PreparedOperation{}, err
	}
	if folder == "" {
		folder = connection.Mail.Folders.Drafts
	}
	if folder == "" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s has no drafts folder; run connection discover", connection.ID)
	}
	if kind != "mail.draft.create" && kind != "mail.draft.update" && kind != "mail.draft.delete" {
		return model.PreparedOperation{}, fmt.Errorf("unsupported draft operation %q", kind)
	}
	if kind != "mail.draft.create" && uid == 0 {
		return model.PreparedOperation{}, fmt.Errorf("draft UID is required")
	}
	message.ConnectionID = connection.ID
	if kind != "mail.draft.delete" {
		if err := postmail.ValidateMessage(message); err != nil {
			return model.PreparedOperation{}, fmt.Errorf("validate draft message: %w", err)
		}
	}
	payload := draftPayload{Folder: folder, UID: uid, Message: message}
	if kind != "mail.draft.create" {
		payload.Precondition, err = s.snapshotMessage(ctx, connection, folder, uid)
		if err != nil {
			return model.PreparedOperation{}, err
		}
	}
	return s.prepare(ctx, kind, connection, payload, map[string]any{
		"acting_identity": connection.Identity, "folder": folder, "uid": uid,
		"recipients": map[string]any{"to": message.To, "cc": message.CC, "bcc": message.BCC},
		"subject":    message.Subject, "text": message.Text, "reply_to": message.ReplyTo,
		"in_reply_to": message.InReplyTo, "references": message.References,
		"attachments":  attachmentPreviews(message.Attachments),
		"side_effects": []string{"modify one provider draft"},
	})
}

func (s *Service) PrepareCalendarWrite(ctx context.Context, connectionID, kind string, event model.Event) (model.PreparedOperation, error) {
	connection, err := s.exactConnection(connectionID, "calendar.write")
	if err != nil {
		return model.PreparedOperation{}, err
	}
	if connection.Calendar.Kind != "caldav" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s calendar is read-only", connection.ID)
	}
	if kind != "calendar.create" && kind != "calendar.update" {
		return model.PreparedOperation{}, fmt.Errorf("unsupported calendar operation %q", kind)
	}
	if kind == "calendar.update" && event.ETag == "" {
		return model.PreparedOperation{}, fmt.Errorf("calendar update requires the current ETag")
	}
	if kind == "calendar.update" && event.Href == "" {
		return model.PreparedOperation{}, fmt.Errorf("calendar update requires the provider href from calendar list")
	}
	if kind == "calendar.update" && event.RecurrenceID == "" && event.SeriesID != "" && event.ID != event.SeriesID {
		return model.PreparedOperation{}, fmt.Errorf("cannot replace a recurring series from an expanded occurrence; refresh and edit the series master")
	}
	if err := calendar.ValidateCalDAVHref(connection, event.CollectionID, event.Href); err != nil {
		return model.PreparedOperation{}, err
	}
	if event.RecurrenceID != "" {
		if _, err := time.Parse(time.RFC3339, event.RecurrenceID); err != nil {
			return model.PreparedOperation{}, fmt.Errorf("validate recurrence ID: %w", err)
		}
	}
	if _, _, err := calendar.Generate(event); err != nil {
		return model.PreparedOperation{}, fmt.Errorf("validate calendar event: %w", err)
	}
	event.ConnectionID = connection.ID
	return s.prepare(ctx, kind, connection, event, map[string]any{
		"acting_identity": connection.Identity, "calendar": event.CollectionID, "title": event.Title,
		"start": event.Start, "end": event.End, "attendees": event.Attendees, "changed_fields": event,
		"side_effects": []string{"write one CalDAV event"},
	})
}

func (s *Service) PrepareCalendarDelete(ctx context.Context, connectionID, collectionID, href, etag, recurrenceID string) (model.PreparedOperation, error) {
	if recurrenceID != "" {
		return model.PreparedOperation{}, fmt.Errorf("cannot delete one expanded occurrence; update it with STATUS:CANCELLED or delete the series master")
	}
	if etag == "" {
		return model.PreparedOperation{}, fmt.Errorf("calendar delete requires the current ETag")
	}
	connection, err := s.exactConnection(connectionID, "calendar.write")
	if err != nil {
		return model.PreparedOperation{}, err
	}
	if connection.Calendar.Kind != "caldav" {
		return model.PreparedOperation{}, fmt.Errorf("connection %s calendar is read-only", connection.ID)
	}
	if err := calendar.ValidateCalDAVHref(connection, collectionID, href); err != nil {
		return model.PreparedOperation{}, err
	}
	return s.prepare(ctx, "calendar.delete", connection, calendarDeletePayload{CollectionID: collectionID, Href: href, ETag: etag}, map[string]any{
		"acting_identity": connection.Identity, "calendar": collectionID, "href": href,
		"side_effects": []string{"delete one CalDAV event"},
	})
}

func (s *Service) prepare(ctx context.Context, kind string, connection model.Connection, payload any, preview map[string]any) (model.PreparedOperation, error) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.PreparedOperation{}, fmt.Errorf("initialize encrypted operation store: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return model.PreparedOperation{}, fmt.Errorf("encode operation payload: %w", err)
	}
	digest, err := digestPayload(kind, data)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return model.PreparedOperation{}, fmt.Errorf("generate operation token: %w", err)
	}
	now := s.now()
	prepared := model.PreparedOperation{
		Token: base64.RawURLEncoding.EncodeToString(tokenBytes), Kind: kind, ConnectionID: connection.ID,
		Identity: connection.Identity, Preview: preview, CreatedAt: now, ExpiresAt: now.Add(10 * time.Minute), Status: "prepared",
	}
	connectionPrecondition, err := digestConnection(connection)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	if err := ledger.PutOperation(ctx, state.OperationRecord{Public: prepared, Payload: data, Digest: digest, Precondition: connectionPrecondition}); err != nil {
		return model.PreparedOperation{}, err
	}
	return prepared, nil
}

func (s *Service) OperationShow(ctx context.Context, token string) (model.PreparedOperation, error) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.PreparedOperation{}, err
	}
	record, err := ledger.GetOperation(ctx, token)
	if err != nil {
		return model.PreparedOperation{}, err
	}
	return record.Public, nil
}

func (s *Service) ExecuteOperation(ctx context.Context, token string) (model.OperationResult, error) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.OperationResult{}, err
	}
	record, err := ledger.GetOperation(ctx, token)
	if err != nil {
		return model.OperationResult{}, err
	}
	if record.Public.Status == "executing" {
		return s.waitForOperation(ctx, ledger, token, record)
	}
	if record.Public.Status != "prepared" {
		return operationResult(record.Public), nil
	}
	if !s.now().Before(record.Public.ExpiresAt) {
		return model.OperationResult{}, fmt.Errorf("prepared operation expired; prepare it again")
	}
	executionPayload, err := snapshotAttachmentPayload(record.Public.Kind, record.Payload)
	if err != nil {
		return model.OperationResult{}, err
	}
	digest, err := digestPayload(record.Public.Kind, executionPayload)
	if err != nil || digest != record.Digest {
		return model.OperationResult{}, fmt.Errorf("prepared operation payload changed; prepare it again")
	}
	connection, err := s.exactConnection(record.Public.ConnectionID, operationCapability(record.Public.Kind))
	if err != nil {
		return model.OperationResult{}, fmt.Errorf("prepared operation connection changed: %w", err)
	}
	if connection.Identity != record.Public.Identity {
		return model.OperationResult{}, fmt.Errorf("prepared operation acting identity changed; prepare it again")
	}
	connectionPrecondition, err := digestConnection(connection)
	if err != nil || connectionPrecondition != record.Precondition {
		return model.OperationResult{}, fmt.Errorf("prepared operation connection or provider preconditions changed; prepare it again")
	}
	record, claimed, err := ledger.ClaimOperation(ctx, token)
	if err != nil {
		return model.OperationResult{}, err
	}
	if !claimed {
		if record.Public.Status == "executing" {
			return s.waitForOperation(ctx, ledger, token, record)
		}
		return operationResult(record.Public), nil
	}
	result, executeErr := s.execute(ctx, connection, record.Public.Kind, executionPayload)
	record.Public.ExecutedAt = s.now()
	record.Public.Result = result
	record.Public.Status = "succeeded"
	if executeErr != nil {
		record.Public.Status = "failed"
		var uncertain *postmail.UncertainError
		var uncertainAppend *postmail.UncertainAppendError
		var uncertainMutation *postmail.UncertainMutationError
		var uncertainCalendar *calendar.UncertainError
		var partial *uncertainOperationError
		if errors.As(executeErr, &uncertain) || errors.As(executeErr, &uncertainAppend) || errors.As(executeErr, &uncertainMutation) || errors.As(executeErr, &uncertainCalendar) || errors.As(executeErr, &partial) {
			record.Public.Status = "uncertain"
		}
		if record.Public.Result == nil {
			record.Public.Result = make(map[string]any)
		}
		record.Public.Result["error"] = executeErr.Error()
	}
	persistCtx := ctx
	var cancelPersist context.CancelFunc
	if ctx.Err() != nil {
		persistCtx, cancelPersist = context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelPersist()
	}
	if err := ledger.CompleteOperation(persistCtx, record); err != nil {
		current, readErr := ledger.GetOperation(persistCtx, token)
		if readErr == nil && current.Public.Status != "executing" {
			return operationResult(current.Public), executeErr
		}
		return model.OperationResult{}, err
	}
	if executeErr != nil {
		return operationResult(record.Public), executeErr
	}
	return operationResult(record.Public), nil
}

func (s *Service) waitForOperation(ctx context.Context, ledger *state.Store, token string, record state.OperationRecord) (model.OperationResult, error) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for record.Public.Status == "executing" {
		executionDeadline := record.Public.ExecutedAt.Add(10 * time.Minute)
		if record.Public.ExecutedAt.IsZero() {
			executionDeadline = record.Public.ExpiresAt.Add(10 * time.Minute)
		}
		if !s.now().Before(executionDeadline) {
			record.Public.Status = "uncertain"
			record.Public.ExecutedAt = s.now()
			record.Public.Result = map[string]any{"error": "execution was interrupted after the provider operation may have started; prepare a fresh operation only after verifying provider state"}
			if err := ledger.CompleteOperation(ctx, record); err != nil {
				current, readErr := ledger.GetOperation(ctx, token)
				if readErr != nil {
					return model.OperationResult{}, err
				}
				record = current
			}
			return operationResult(record.Public), nil
		}
		select {
		case <-ctx.Done():
			return operationResult(record.Public), ctx.Err()
		case <-ticker.C:
			var err error
			record, err = ledger.GetOperation(ctx, token)
			if err != nil {
				return model.OperationResult{}, err
			}
		}
	}
	return operationResult(record.Public), nil
}

func (s *Service) execute(ctx context.Context, connection model.Connection, kind string, payload json.RawMessage) (map[string]any, error) {
	switch kind {
	case "mail.send":
		var message model.SendMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, err
		}
		if connection.Mail.SentCopy == "always" && connection.Mail.Folders.Sent == "" {
			return nil, fmt.Errorf("configured sent copy folder is missing; message was not sent")
		}
		data, err := s.mailBuild(connection, message)
		if err != nil {
			return nil, err
		}
		var sendErr error
		if s.mailSendRaw != nil {
			sendErr = s.mailSendRaw(connection, message, data)
		} else {
			sendErr = s.mailSendRawContext(ctx, connection, message, data)
		}
		if sendErr != nil {
			return nil, sendErr
		}
		result := map[string]any{"sent": true}
		if connection.Mail.SentCopy == "always" {
			var uid uint32
			if s.mailAppendRaw != nil {
				uid, err = s.mailAppendRaw(connection, connection.Mail.Folders.Sent, data, []imap.Flag{imap.FlagSeen})
			} else {
				uid, err = s.mailAppendRawContext(ctx, connection, connection.Mail.Folders.Sent, data, []imap.Flag{imap.FlagSeen})
			}
			if err != nil {
				result["sent_copy"] = "failed"
				return result, &uncertainOperationError{message: fmt.Sprintf("message was sent but appending its sent copy failed: %v", err)}
			}
			result["sent_copy_uid"] = uid
		}
		return result, nil
	case "mail.mark", "mail.move", "mail.archive", "mail.trash":
		var action MailAction
		if err := json.Unmarshal(payload, &action); err != nil {
			return nil, err
		}
		if kind == "mail.mark" {
			if err := postmail.SetFlagsContext(ctx, connection, action.Folder, action.UID, action.Seen, action.Flagged, action.Precondition); err != nil {
				return nil, err
			}
			return map[string]any{"updated": true}, nil
		}
		if err := postmail.MoveContext(ctx, connection, action.Folder, action.UID, action.Destination, action.Precondition); err != nil {
			return nil, err
		}
		return map[string]any{"moved": true, "destination": action.Destination}, nil
	case "mail.draft.create", "mail.draft.update", "mail.draft.delete":
		var draft draftPayload
		if err := json.Unmarshal(payload, &draft); err != nil {
			return nil, err
		}
		if kind == "mail.draft.delete" {
			var err error
			if s.mailMarkDeleted != nil {
				err = s.mailMarkDeleted(connection, draft.Folder, draft.UID, draft.Precondition)
			} else {
				err = s.mailMarkDeletedContext(ctx, connection, draft.Folder, draft.UID, draft.Precondition)
			}
			if err != nil {
				return nil, err
			}
			return map[string]any{"deleted": true}, nil
		}
		if kind == "mail.draft.update" {
			var current postmail.MessagePrecondition
			var err error
			current, err = s.snapshotMessage(ctx, connection, draft.Folder, draft.UID)
			if err != nil || current != draft.Precondition {
				return nil, fmt.Errorf("provider draft changed; refresh and prepare the operation again")
			}
		}
		var uid uint32
		var err error
		if s.mailAppend != nil {
			uid, err = s.mailAppend(connection, draft.Folder, draft.Message, []imap.Flag{imap.FlagDraft})
		} else {
			uid, err = s.mailAppendContext(ctx, connection, draft.Folder, draft.Message, []imap.Flag{imap.FlagDraft})
		}
		if err != nil {
			return nil, err
		}
		if kind == "mail.draft.update" {
			if s.mailMarkDeleted != nil {
				err = s.mailMarkDeleted(connection, draft.Folder, draft.UID, draft.Precondition)
			} else {
				err = s.mailMarkDeletedContext(ctx, connection, draft.Folder, draft.UID, draft.Precondition)
			}
			if err != nil {
				return map[string]any{"uid": uid, "replaced_uid": draft.UID, "cleanup": "failed"}, &uncertainOperationError{message: fmt.Sprintf("replacement draft was appended as UID %d but old draft cleanup failed: %v", uid, err)}
			}
		}
		return map[string]any{"uid": uid}, nil
	case "calendar.create", "calendar.update":
		var event model.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, err
		}
		written, err := calendar.PutCalDAVEvent(ctx, connection, event, kind == "calendar.create")
		if err != nil {
			return nil, err
		}
		return map[string]any{"event": written}, nil
	case "calendar.delete":
		var deletion calendarDeletePayload
		if err := json.Unmarshal(payload, &deletion); err != nil {
			return nil, err
		}
		if err := calendar.DeleteCalDAVEvent(ctx, connection, deletion.CollectionID, deletion.Href, deletion.ETag); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	default:
		return nil, fmt.Errorf("unsupported prepared operation kind %q", kind)
	}
}

func (s *Service) snapshotMessage(ctx context.Context, connection model.Connection, folder string, uid uint32) (postmail.MessagePrecondition, error) {
	if s.mailSnapshot != nil {
		return s.mailSnapshot(connection, folder, uid)
	}
	return s.mailSnapshotContext(ctx, connection, folder, uid)
}

func (s *Service) GetMessage(connectionID, folder string, uid uint32) (model.MessageDetail, error) {
	return s.GetMessageMode(connectionID, folder, uid, "")
}

func (s *Service) GetMessageMode(connectionID, folder string, uid uint32, mode string) (model.MessageDetail, error) {
	connection, err := s.exactConnection(connectionID, "mail.read")
	if err != nil {
		return model.MessageDetail{}, err
	}
	if folder == "" {
		folder = connection.Mail.Folders.Inbox
		if folder == "" {
			folder = "INBOX"
		}
	}
	if mode != "offline" {
		fetched, fetchErr := postmail.Get(connection, folder, uid)
		if fetchErr == nil {
			if ledger, stateErr := s.ensureState(); stateErr == nil {
				data, _ := json.Marshal(fetched.Detail)
				_ = s.cacheMailboxUIDValidity(ledger, connection.ID, folder, fetched.UIDValidity)
				_ = ledger.Put(context.Background(), state.CacheEntry{Namespace: "message_body", Key: messageCacheKey(connection.ID, folder, fetched.UIDValidity, uid), ConnectionID: connection.ID, Kind: "message_body", ProviderID: fmt.Sprintf("%s/%d", folder, uid), ExpiresAt: s.now().Add(s.messageBodyTTL()), Value: data})
			}
			return fetched.Detail, nil
		}
		if mode == "refresh" {
			return model.MessageDetail{}, fetchErr
		}
		err = fetchErr
	}
	if cached, ok := s.cachedMessage(connection.ID, folder, uid); ok {
		cached.Stale = true
		return cached, nil
	}
	if err != nil {
		return model.MessageDetail{}, err
	}
	return model.MessageDetail{}, fmt.Errorf("no cached message body for %s/%d", folder, uid)
}

func (s *Service) GetAttachment(ctx context.Context, connectionID, folder string, uid uint32, attachmentID string) (model.Attachment, []byte, error) {
	return s.GetAttachmentMode(ctx, connectionID, folder, uid, attachmentID, "")
}

func (s *Service) GetAttachmentMode(ctx context.Context, connectionID, folder string, uid uint32, attachmentID, mode string) (model.Attachment, []byte, error) {
	connection, err := s.exactConnection(connectionID, "mail.read")
	if err != nil {
		return model.Attachment{}, nil, err
	}
	if folder == "" {
		folder = connection.Mail.Folders.Inbox
		if folder == "" {
			folder = "INBOX"
		}
	}
	if mode == "" {
		if attachment, data, ok := s.cachedAttachment(ctx, connection.ID, folder, uid, attachmentID); ok {
			attachment.Stale = false
			return attachment, data, nil
		}
	}
	if mode != "offline" {
		attachment, data, uidValidity, fetchErr := postmail.GetAttachment(connection, folder, uid, attachmentID)
		if fetchErr == nil {
			if ledger, stateErr := s.ensureState(); stateErr == nil {
				_ = s.cacheMailboxUIDValidity(ledger, connection.ID, folder, uidValidity)
				_ = ledger.Put(ctx, state.CacheEntry{Namespace: "attachment", Key: messageCacheKey(connection.ID, folder, uidValidity, uid) + "/" + attachmentID, ConnectionID: connection.ID, Kind: "attachment", ProviderID: attachmentID, ExpiresAt: s.now().Add(s.messageBodyTTL()), Value: data})
			}
			return attachment, data, nil
		}
		if mode == "refresh" {
			return model.Attachment{}, nil, fetchErr
		}
		err = fetchErr
	}
	if attachment, data, ok := s.cachedAttachment(ctx, connection.ID, folder, uid, attachmentID); ok {
		attachment.Stale = true
		return attachment, data, nil
	}
	if err != nil {
		return model.Attachment{}, nil, err
	}
	return model.Attachment{}, nil, fmt.Errorf("no cached attachment %q", attachmentID)
}

func (s *Service) CacheStatus(ctx context.Context) (state.Stats, error) {
	ledger, err := s.ensureState()
	if err != nil {
		return state.Stats{}, err
	}
	return ledger.Stats(ctx)
}

func (s *Service) CacheClear(ctx context.Context) error {
	ledger, err := s.ensureState()
	if err != nil {
		return err
	}
	return ledger.Clear(ctx)
}

func (s *Service) CacheRekey(ctx context.Context, encoded string) error {
	key, err := decodeCacheKey(encoded)
	if err != nil {
		return err
	}
	ledger, err := s.ensureState()
	if err != nil {
		return err
	}
	return ledger.Rekey(ctx, key)
}

func (s *Service) exactConnection(id, capability string) (model.Connection, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return model.Connection{}, err
	}
	connection, err := selector.One(cfg.Connections, id, capability)
	if err != nil {
		return model.Connection{}, err
	}
	if connection.Identity.Email == "" && connection.Mail != nil {
		connection.Identity.Email = connection.Mail.Username
	}
	return connection, nil
}

func operationResult(prepared model.PreparedOperation) model.OperationResult {
	return model.OperationResult{Token: prepared.Token, Status: prepared.Status, ExecutedAt: prepared.ExecutedAt, Result: prepared.Result}
}

func operationCapability(kind string) string {
	if strings.HasPrefix(kind, "calendar.") {
		return "calendar.write"
	}
	if kind == "mail.send" {
		return "mail.send"
	}
	return "mail.read"
}

func digestPayload(kind string, payload []byte) (string, error) {
	digest := sha256.New()
	_, _ = digest.Write([]byte(kind + "\x00"))
	if kind == "mail.send" || strings.HasPrefix(kind, "mail.draft.") {
		var message model.SendMessage
		var draft draftPayload
		if kind == "mail.send" {
			if err := json.Unmarshal(payload, &message); err != nil {
				return "", err
			}
		} else {
			if err := json.Unmarshal(payload, &draft); err != nil {
				return "", err
			}
			message = draft.Message
		}
		canonical := message
		canonical.Attachments = append([]model.AttachmentInput(nil), message.Attachments...)
		for index := range canonical.Attachments {
			if canonical.Attachments[index].Path != "" {
				canonical.Attachments[index].Data = nil
			}
		}
		var canonicalPayload []byte
		var err error
		if kind == "mail.send" {
			canonicalPayload, err = json.Marshal(canonical)
		} else {
			draft.Message = canonical
			canonicalPayload, err = json.Marshal(draft)
		}
		if err != nil {
			return "", err
		}
		_, _ = digest.Write(canonicalPayload)
		for index, attachment := range message.Attachments {
			attachmentDigest := sha256.New()
			if attachment.Path == "" || attachment.Data != nil {
				_, _ = attachmentDigest.Write(attachment.Data)
			} else {
				file, err := os.Open(attachment.Path)
				if err != nil {
					return "", fmt.Errorf("read attachment %s for operation digest: %w", attachment.Path, err)
				}
				if _, err := io.Copy(attachmentDigest, file); err != nil {
					_ = file.Close()
					return "", err
				}
				if err := file.Close(); err != nil {
					return "", err
				}
			}
			_, _ = fmt.Fprintf(digest, "\x00attachment:%d:%x", index, attachmentDigest.Sum(nil))
		}
	} else {
		_, _ = digest.Write(payload)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func snapshotAttachmentPayload(kind string, payload []byte) ([]byte, error) {
	if kind != "mail.send" && !strings.HasPrefix(kind, "mail.draft.") {
		return payload, nil
	}
	var message model.SendMessage
	var draft draftPayload
	if kind == "mail.send" {
		if err := json.Unmarshal(payload, &message); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(payload, &draft); err != nil {
			return nil, err
		}
		message = draft.Message
	}
	for index := range message.Attachments {
		attachment := &message.Attachments[index]
		if attachment.Path == "" || attachment.Data != nil {
			continue
		}
		data, err := os.ReadFile(attachment.Path)
		if err != nil {
			return nil, fmt.Errorf("snapshot attachment %s for operation execution: %w", attachment.Path, err)
		}
		attachment.Data = data
	}
	if kind == "mail.send" {
		return json.Marshal(message)
	}
	draft.Message = message
	return json.Marshal(draft)
}

func digestConnection(connection model.Connection) (string, error) {
	data, err := json.Marshal(connection)
	if err != nil {
		return "", fmt.Errorf("encode connection precondition: %w", err)
	}
	digest := sha256.New()
	_, _ = digest.Write(data)
	writeSecret := func(label string, ref model.SecretRef, legacyEnv string) error {
		if ref.Env == "" && ref.Keychain == "" && legacyEnv == "" {
			return nil
		}
		var value string
		var resolveErr error
		if ref.Env != "" || ref.Keychain != "" {
			value, resolveErr = config.ResolveSecret(ref)
		} else {
			value, resolveErr = config.Secret(legacyEnv)
		}
		if resolveErr != nil {
			return fmt.Errorf("resolve %s precondition: %w", label, resolveErr)
		}
		_, _ = digest.Write([]byte("\x00" + label + "\x00" + value))
		return nil
	}
	if connection.Mail != nil {
		if err := writeSecret("mail.secret", connection.Mail.Secret, connection.Mail.SecretEnv); err != nil {
			return "", err
		}
	}
	if connection.Calendar != nil {
		if err := writeSecret("calendar.url", connection.Calendar.URLSecret, connection.Calendar.URLSecretEnv); err != nil {
			return "", err
		}
		if connection.Calendar.Kind == "caldav" {
			if err := writeSecret("calendar.secret", connection.Calendar.Secret, ""); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func attachmentPreviews(attachments []model.AttachmentInput) []map[string]any {
	result := make([]map[string]any, 0, len(attachments))
	for _, attachment := range attachments {
		name := attachment.Name
		if name == "" {
			name = filepath.Base(attachment.Path)
		}
		result = append(result, map[string]any{"name": name, "content_type": attachment.ContentType, "path": attachment.Path})
	}
	return result
}

func sentCopyEffect(connection model.Connection) string {
	switch connection.Mail.SentCopy {
	case "always":
		return "append a copy to " + connection.Mail.Folders.Sent
	case "never":
		return "do not append a sent copy"
	default:
		return "provider manages sent copies"
	}
}

func decodeCacheKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.StdEncoding, base64.RawStdEncoding} {
		if key, err := encoding.DecodeString(encoded); err == nil && len(key) == 32 {
			return key, nil
		}
	}
	if key, err := hex.DecodeString(encoded); err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, fmt.Errorf("cache key must be a base64 or hex encoded 32-byte key")
}

func (s *Service) DiscoverConnection(ctx context.Context, id string) (model.Connection, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return model.Connection{}, err
	}
	connection, err := selector.One(cfg.Connections, id, "")
	if err != nil {
		return model.Connection{}, err
	}
	if connection.Mail != nil && connection.Mail.IMAP.Address != "" {
		discovery, err := postmail.Discover(connection)
		if err != nil {
			return model.Connection{}, err
		}
		connection.Mail.Folders = mergeFolders(connection.Mail.Folders, discovery.Folders)
	}
	if connection.Calendar != nil && connection.Calendar.Kind == "caldav" {
		existingCollections := connection.Calendar.Collections
		discovery, err := calendar.DiscoverCalDAV(ctx, connection)
		if err != nil {
			return model.Connection{}, err
		}
		connection.Calendar.Collections = preserveCollectionPolicies(existingCollections, discovery.Calendars)
	}
	if err := s.UpsertConnection(connection, true); err != nil {
		return model.Connection{}, err
	}
	return publicConnection(connection), nil
}

func preserveCollectionPolicies(existing, discovered []model.CalendarCollection) []model.CalendarCollection {
	for index := range discovered {
		for _, configured := range existing {
			if (configured.ID != "" && strings.EqualFold(configured.ID, discovered[index].ID)) || (configured.Path != "" && configured.Path == discovered[index].Path) {
				discovered[index].ReadOnly = configured.ReadOnly
				break
			}
		}
	}
	return discovered
}

func (s *Service) DoctorConnection(ctx context.Context, id string) (model.DoctorResult, error) {
	connection, err := s.exactConnection(id, "")
	if err != nil {
		return model.DoctorResult{}, err
	}
	result := model.DoctorResult{ConnectionID: connection.ID, OK: true}
	add := func(name string, err error) {
		check := model.DoctorCheck{Name: name, Status: "ok"}
		if err != nil {
			check.Status, check.Message, result.OK = "error", err.Error(), false
		}
		result.Checks = append(result.Checks, check)
	}
	if connection.Mail != nil {
		_, secretErr := config.ResolveSecret(connection.Mail.Secret)
		add("mail.secret", secretErr)
		if secretErr == nil && connection.Mail.IMAP.Address != "" {
			_, err := postmail.Discover(connection)
			add("imap.discovery", err)
		}
		if secretErr == nil && connection.Mail.SMTP.Address != "" {
			add("smtp.authentication", postmail.DoctorSMTP(ctx, connection))
		}
	}
	if connection.Calendar != nil {
		if connection.Calendar.Kind == "feed" {
			_, err := resolveDoctorCalendarURL(connection)
			if err == nil {
				_, err = s.calendar.List(ctx, connection, s.now().Add(-24*time.Hour), s.now().Add(24*time.Hour), "")
			}
			add("calendar.feed", err)
		} else {
			_, err := config.ResolveSecret(connection.Calendar.Secret)
			add("caldav.secret", err)
			if err == nil {
				_, err := calendar.DiscoverCalDAV(ctx, connection)
				add("caldav.discovery", err)
			}
		}
	}
	return result, nil
}

func resolveDoctorCalendarURL(connection model.Connection) (string, error) {
	if connection.Calendar.URL != "" {
		return "configured", nil
	}
	if _, err := config.ResolveSecret(connection.Calendar.URLSecret); err != nil {
		return "", err
	}
	return "configured", nil
}

func (s *Service) Sync(ctx context.Context, selection model.Selector) (map[string]any, error) {
	result := map[string]any{"messages": 0, "events": 0, "errors": []model.SourceError{}}
	cacheConfig := s.cacheConfig()
	requestedCapability := strings.ToLower(strings.TrimSpace(selection.Capability))
	syncMail := requestedCapability == "" || requestedCapability == "mail" || requestedCapability == "mail.read"
	syncCalendar := requestedCapability == "" || requestedCapability == "calendar" || requestedCapability == "calendar.read"
	if !syncMail && !syncCalendar {
		return nil, fmt.Errorf("sync capability must be mail, mail.read, calendar, or calendar.read")
	}
	if requestedCapability == "" {
		selected, err := s.Connections(selection)
		if err != nil {
			return nil, err
		}
		syncMail, syncCalendar = false, false
		for _, connection := range selected {
			syncMail = syncMail || slices.Contains(connection.Capabilities, "mail.read")
			syncCalendar = syncCalendar || slices.Contains(connection.Capabilities, "calendar.read")
		}
	}
	if syncMail {
		mailSelection := selection
		mailSelection.Capability = "mail.read"
		mailSince := s.now().Add(-time.Duration(cacheConfig.MessageMetadataDays) * 24 * time.Hour)
		cursor := ""
		for {
			page, err := s.SearchMessagesContext(ctx, mailSelection, postmail.SearchOptions{Since: mailSince, Mode: "refresh"}, 100, cursor)
			if err != nil {
				if requestedCapability == "mail" || requestedCapability == "mail.read" {
					return nil, err
				}
				result["errors"] = append(result["errors"].([]model.SourceError), s.syncSourceErrors(mailSelection, "mail", err)...)
				break
			}
			result["messages"] = result["messages"].(int) + len(page.Messages)
			result["errors"] = append(result["errors"].([]model.SourceError), page.Errors...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
	}
	if syncCalendar {
		calendarSelection := selection
		calendarSelection.Capability = "calendar.read"
		calendarNow := s.now()
		calendarStart := calendarNow.Add(-time.Duration(cacheConfig.EventPastDays) * 24 * time.Hour)
		calendarEnd := calendarNow.Add(time.Duration(cacheConfig.EventFutureDays) * 24 * time.Hour)
		cursor := ""
		for {
			page, err := s.ListEventsMode(ctx, calendarSelection, calendarStart, calendarEnd, "", 500, cursor, "refresh")
			if err != nil {
				if requestedCapability == "calendar" || requestedCapability == "calendar.read" {
					return nil, err
				}
				result["errors"] = append(result["errors"].([]model.SourceError), s.syncSourceErrors(calendarSelection, "calendar", err)...)
				break
			}
			result["events"] = result["events"].(int) + len(page.Events)
			result["errors"] = append(result["errors"].([]model.SourceError), page.Errors...)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
			calendarStart, calendarEnd = time.Time{}, time.Time{}
		}
	}
	return result, nil
}

func (s *Service) syncSourceErrors(selection model.Selector, protocol string, err error) []model.SourceError {
	connections, matchErr := s.Connections(selection)
	if matchErr != nil {
		return []model.SourceError{sourceError(protocol, protocol+"_sync_failed", err)}
	}
	result := make([]model.SourceError, 0, len(connections))
	for _, connection := range connections {
		result = append(result, sourceError(connection.ID, protocol+"_sync_failed", err))
	}
	return result
}

func sourceError(connectionID, code string, err error) model.SourceError {
	return model.SourceError{ConnectionID: connectionID, Code: code, Message: err.Error(), Retryable: true}
}

func (s *Service) cacheMailResult(connectionID, folder string, scope any, options postmail.SearchOptions, result postmail.SearchResult) error {
	ledger, err := s.ensureState()
	if err != nil {
		return err
	}
	now := s.now()
	result.Messages = slices.Clone(result.Messages)
	if err := s.cacheMailboxUIDValidity(ledger, connectionID, folder, result.UIDValidity); err != nil {
		return err
	}
	combined := result
	continuation := !options.CursorTime.IsZero() || options.CursorUID != 0
	pendingKey := scopedCacheKey(connectionID, scope)
	if continuation {
		entry, ok, getErr := ledger.Get(context.Background(), "message_metadata_pending", pendingKey, false)
		if getErr == nil && !ok {
			entry, ok, getErr = ledger.Get(context.Background(), "message_metadata", pendingKey, false)
		}
		if getErr == nil && ok {
			var existing postmail.SearchResult
			if json.Unmarshal(entry.Value, &existing) == nil && existing.UIDValidity == result.UIDValidity {
				combined = mergeMailResults(existing, result)
			}
		}
	}
	stampMessages(combined.Messages, now)
	data, err := json.Marshal(combined)
	if err != nil {
		return err
	}
	if result.HasMore {
		if err := ledger.Put(context.Background(), state.CacheEntry{Namespace: "message_metadata_pending", Key: pendingKey, ConnectionID: connectionID, Kind: "message_metadata", CachedAt: now, ExpiresAt: now.Add(s.messageMetadataTTL()), Value: data}); err != nil {
			return err
		}
	} else {
		if err := ledger.Put(context.Background(), state.CacheEntry{Namespace: "message_metadata", Key: pendingKey, ConnectionID: connectionID, Kind: "message_metadata", CachedAt: now, ExpiresAt: now.Add(s.messageMetadataTTL()), Value: data}); err != nil {
			return err
		}
		if err := ledger.Delete(context.Background(), "message_metadata_pending", pendingKey); err != nil {
			return err
		}
	}
	index := result
	indexKey := mailboxCacheKey(connectionID, folder)
	if entry, ok, getErr := ledger.Get(context.Background(), "message_metadata_index", indexKey, false); getErr == nil && ok {
		var existing postmail.SearchResult
		if json.Unmarshal(entry.Value, &existing) == nil && existing.UIDValidity == result.UIDValidity {
			if !result.HasMore && strings.TrimSpace(options.Query) == "" && !options.Unread {
				kept := existing
				kept.Messages = slices.DeleteFunc(kept.Messages, func(message model.Message) bool { return matchesCachedMessage(message, options) })
				index = mergeMailResults(kept, combined)
			} else {
				index = mergeMailResults(existing, result)
			}
		}
	}
	cutoff := now.Add(-s.messageMetadataTTL())
	index.Messages = slices.DeleteFunc(index.Messages, func(message model.Message) bool { return messageTime(message).Before(cutoff) })
	stampMessages(index.Messages, now)
	indexData, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return ledger.Put(context.Background(), state.CacheEntry{Namespace: "message_metadata_index", Key: indexKey, ConnectionID: connectionID, Kind: "message_metadata", CachedAt: now, ExpiresAt: now.Add(s.messageMetadataTTL()), Value: indexData})
}

func (s *Service) cachedMailResult(connectionID, folder string, scope any, options postmail.SearchOptions, cursorState mailCursorState, limit int) (postmail.SearchResult, bool) {
	ledger, err := s.ensureState()
	if err != nil {
		return postmail.SearchResult{}, false
	}
	entry, ok, err := ledger.Get(context.Background(), "message_metadata", scopedCacheKey(connectionID, scope), false)
	if err == nil && !ok {
		entry, ok, err = ledger.Get(context.Background(), "message_metadata_index", mailboxCacheKey(connectionID, folder), false)
	}
	if err != nil || !ok {
		return postmail.SearchResult{}, false
	}
	var result postmail.SearchResult
	if json.Unmarshal(entry.Value, &result) != nil {
		return postmail.SearchResult{}, false
	}
	if cursorState.UIDValidity != 0 && result.UIDValidity != cursorState.UIDValidity {
		return postmail.SearchResult{}, false
	}
	filtered := make([]model.Message, 0, len(result.Messages))
	for _, message := range result.Messages {
		if !matchesCachedMessage(message, options) {
			continue
		}
		if cursorState.UIDNext != 0 && message.UID >= cursorState.UIDNext {
			continue
		}
		if !cursorState.BeforeTime.IsZero() {
			boundary := model.Message{ConnectionID: message.ConnectionID, ReceivedAt: cursorState.BeforeTime, UID: cursorState.BeforeUID}
			if !messageBefore(boundary, message) {
				continue
			}
		}
		message.CachedAt = entry.CachedAt
		filtered = append(filtered, message)
	}
	result.HasMore = limit > 0 && len(filtered) > limit
	if result.HasMore {
		filtered = filtered[:limit]
	}
	result.Messages = filtered
	return result, true
}

func mergeMailResults(existing, fresh postmail.SearchResult) postmail.SearchResult {
	combined := fresh
	byUID := make(map[uint32]model.Message, len(existing.Messages)+len(fresh.Messages))
	for _, message := range existing.Messages {
		byUID[message.UID] = message
	}
	for _, message := range fresh.Messages {
		byUID[message.UID] = message
	}
	combined.Messages = make([]model.Message, 0, len(byUID))
	for _, message := range byUID {
		combined.Messages = append(combined.Messages, message)
	}
	slices.SortFunc(combined.Messages, func(a, b model.Message) int {
		if messageBefore(a, b) {
			return -1
		}
		if messageBefore(b, a) {
			return 1
		}
		return 0
	})
	combined.UIDNext = max(existing.UIDNext, fresh.UIDNext)
	return combined
}

func stampMessages(messages []model.Message, cachedAt time.Time) {
	for index := range messages {
		messages[index].CachedAt = cachedAt
	}
}

func matchesCachedMessage(message model.Message, options postmail.SearchOptions) bool {
	when := messageTime(message)
	if !options.Since.IsZero() && when.Before(options.Since) {
		return false
	}
	if !options.Before.IsZero() && !when.Before(options.Before) {
		return false
	}
	if options.Unread && !message.Unread {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(options.Query))
	if query == "" {
		return true
	}
	var searchable strings.Builder
	searchable.WriteString(message.Subject)
	searchable.WriteByte(' ')
	searchable.WriteString(message.Preview)
	for _, address := range append(append([]model.Address(nil), message.From...), message.To...) {
		searchable.WriteByte(' ')
		searchable.WriteString(address.Name)
		searchable.WriteByte(' ')
		searchable.WriteString(address.Email)
	}
	return strings.Contains(strings.ToLower(searchable.String()), query)
}

func (s *Service) cacheEvents(connectionID string, scope any, events []model.Event, rangeStart, rangeEnd time.Time, replaceScoped, replaceIndex bool) error {
	ledger, err := s.ensureState()
	if err != nil {
		return err
	}
	now := s.now()
	for index := range events {
		events[index].CachedAt = now
	}
	scoped := append([]model.Event(nil), events...)
	if !replaceScoped {
		if entry, ok, getErr := ledger.Get(context.Background(), "events", scopedCacheKey(connectionID, scope), false); getErr == nil && ok {
			var existing []model.Event
			if json.Unmarshal(entry.Value, &existing) == nil {
				scoped = mergeEvents(existing, scoped)
			}
		}
	}
	data, err := json.Marshal(scoped)
	if err != nil {
		return err
	}
	if err := ledger.Put(context.Background(), state.CacheEntry{Namespace: "events", Key: scopedCacheKey(connectionID, scope), ConnectionID: connectionID, Kind: "event", CachedAt: now, ExpiresAt: now.Add(s.eventTTL()), Value: data}); err != nil {
		return err
	}
	index := append([]model.Event(nil), events...)
	if entry, ok, getErr := ledger.Get(context.Background(), "event_index", connectionID, false); getErr == nil && ok {
		var existing []model.Event
		if json.Unmarshal(entry.Value, &existing) == nil {
			if replaceIndex {
				kept := existing[:0]
				for _, event := range existing {
					if !calendarEventOverlaps(event, rangeStart, rangeEnd) {
						kept = append(kept, event)
					}
				}
				existing = kept
			}
			index = mergeEvents(existing, index)
		}
	}
	indexData, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return ledger.Put(context.Background(), state.CacheEntry{Namespace: "event_index", Key: connectionID, ConnectionID: connectionID, Kind: "event", CachedAt: now, ExpiresAt: now.Add(s.eventTTL()), Value: indexData})
}

func (s *Service) cachedEvents(connection model.Connection, scope any, collections []string, start, end time.Time, query string) ([]model.Event, bool) {
	ledger, err := s.ensureState()
	if err != nil {
		return nil, false
	}
	entry, ok, err := ledger.Get(context.Background(), "events", scopedCacheKey(connection.ID, scope), false)
	fromIndex := false
	if err == nil && !ok {
		entry, ok, err = ledger.Get(context.Background(), "event_index", connection.ID, false)
		fromIndex = ok
	}
	if err != nil || !ok {
		return nil, false
	}
	var events []model.Event
	if json.Unmarshal(entry.Value, &events) != nil {
		return nil, false
	}
	filtered := events[:0]
	query = strings.ToLower(strings.TrimSpace(query))
	collectionIDs := selectedCollectionIDs(connection, collections)
	for _, event := range events {
		if fromIndex && len(collectionIDs) > 0 && !slices.ContainsFunc(collectionIDs, func(value string) bool { return strings.EqualFold(value, event.CollectionID) }) {
			continue
		}
		if !calendarEventOverlaps(event, start, end) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(event.Title+" "+event.Description+" "+event.Location), query) {
			continue
		}
		event.CachedAt = entry.CachedAt
		filtered = append(filtered, event)
	}
	return filtered, true
}

func selectedCollectionIDs(connection model.Connection, selected []string) []string {
	if len(selected) == 0 || connection.Calendar == nil {
		return selected
	}
	result := make([]string, 0, len(selected))
	for _, value := range selected {
		matched := false
		for _, collection := range connection.Calendar.Collections {
			if strings.EqualFold(value, collection.ID) || strings.EqualFold(value, collection.Name) {
				result = append(result, collection.ID)
				matched = true
				break
			}
		}
		if !matched {
			result = append(result, value)
		}
	}
	return result
}

func mergeEvents(existing, fresh []model.Event) []model.Event {
	byID := make(map[string]model.Event, len(existing)+len(fresh))
	for _, event := range existing {
		byID[eventCacheIdentity(event)] = event
	}
	for _, event := range fresh {
		byID[eventCacheIdentity(event)] = event
	}
	result := make([]model.Event, 0, len(byID))
	for _, event := range byID {
		result = append(result, event)
	}
	slices.SortFunc(result, compareEvents)
	return result
}

func eventCacheIdentity(event model.Event) string {
	return strings.Join([]string{event.ConnectionID, event.CollectionID, event.ID, event.Href, event.RecurrenceID}, "\x00")
}

func calendarEventOverlaps(event model.Event, start, end time.Time) bool {
	eventEnd := event.End
	if eventEnd.IsZero() {
		eventEnd = event.Start
	}
	return (start.IsZero() || eventEnd.After(start)) && (end.IsZero() || event.Start.Before(end))
}

func normalizeEventRange(start, end, now time.Time) (time.Time, time.Time) {
	if start.IsZero() {
		start = now.Add(-90 * 24 * time.Hour)
	}
	if end.IsZero() {
		end = now.Add(365 * 24 * time.Hour)
	}
	return start, end
}

func (s *Service) cachedMessage(connectionID, folder string, uid uint32) (model.MessageDetail, bool) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.MessageDetail{}, false
	}
	uidValidity, ok := s.cachedMailboxUIDValidity(ledger, connectionID, folder)
	if !ok {
		return model.MessageDetail{}, false
	}
	entry, ok, err := ledger.Get(context.Background(), "message_body", messageCacheKey(connectionID, folder, uidValidity, uid), false)
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

func (s *Service) cachedAttachment(ctx context.Context, connectionID, folder string, uid uint32, attachmentID string) (model.Attachment, []byte, bool) {
	ledger, err := s.ensureState()
	if err != nil {
		return model.Attachment{}, nil, false
	}
	uidValidity, ok := s.cachedMailboxUIDValidity(ledger, connectionID, folder)
	if !ok {
		return model.Attachment{}, nil, false
	}
	entry, ok, err := ledger.Get(ctx, "attachment", messageCacheKey(connectionID, folder, uidValidity, uid)+"/"+attachmentID, false)
	if err != nil || !ok {
		return model.Attachment{}, nil, false
	}
	attachment := model.Attachment{ID: attachmentID, Size: int64(len(entry.Value)), CachedAt: entry.CachedAt}
	if detail, found := s.cachedMessage(connectionID, folder, uid); found {
		for _, candidate := range detail.Attachments {
			if candidate.ID == attachmentID {
				attachment = candidate
				attachment.CachedAt = entry.CachedAt
				break
			}
		}
	}
	return attachment, entry.Value, true
}

func (s *Service) messageBodyTTL() time.Duration {
	return time.Duration(s.cacheConfig().MessageBodyDays) * 24 * time.Hour
}

func (s *Service) messageMetadataTTL() time.Duration {
	return time.Duration(s.cacheConfig().MessageMetadataDays) * 24 * time.Hour
}

func (s *Service) eventTTL() time.Duration {
	return time.Duration(s.cacheConfig().EventFutureDays) * 24 * time.Hour
}

func (s *Service) cacheConfig() model.CacheConfig {
	cfg, err := s.store.Load()
	if err == nil {
		return cfg.Cache
	}
	return model.CacheConfig{MessageMetadataDays: 90, MessageBodyDays: 30, EventPastDays: 90, EventFutureDays: 365}
}

func scopedCacheKey(connectionID string, scope any) string {
	data, _ := json.Marshal(scope)
	digest := sha256.Sum256(append([]byte(connectionID+"\x00"), data...))
	return hex.EncodeToString(digest[:])
}

func messageCacheKey(connectionID, folder string, uidValidity, uid uint32) string {
	return fmt.Sprintf("%s/%s/%d/%d", connectionID, folder, uidValidity, uid)
}

func mailboxCacheKey(connectionID, folder string) string {
	return connectionID + "/" + folder
}

func (s *Service) cacheMailboxUIDValidity(ledger *state.Store, connectionID, folder string, uidValidity uint32) error {
	data, err := json.Marshal(uidValidity)
	if err != nil {
		return err
	}
	return ledger.Put(context.Background(), state.CacheEntry{Namespace: "mailbox_uidvalidity", Key: mailboxCacheKey(connectionID, folder), ConnectionID: connectionID, Kind: "sync_state", ProviderID: folder, ExpiresAt: s.now().Add(s.messageMetadataTTL()), Value: data})
}

func (s *Service) cachedMailboxUIDValidity(ledger *state.Store, connectionID, folder string) (uint32, bool) {
	entry, ok, err := ledger.Get(context.Background(), "mailbox_uidvalidity", mailboxCacheKey(connectionID, folder), false)
	if err != nil || !ok {
		return 0, false
	}
	var uidValidity uint32
	if json.Unmarshal(entry.Value, &uidValidity) != nil || uidValidity == 0 {
		return 0, false
	}
	return uidValidity, true
}

func mailFolder(connection model.Connection, folder string) string {
	if folder == "" && connection.Mail != nil {
		folder = connection.Mail.Folders.Inbox
	}
	if folder == "" {
		return "INBOX"
	}
	return folder
}

func mergeFolders(configured, discovered model.FolderConfig) model.FolderConfig {
	if configured.Inbox == "" {
		configured.Inbox = discovered.Inbox
	}
	if configured.Sent == "" {
		configured.Sent = discovered.Sent
	}
	if configured.Drafts == "" {
		configured.Drafts = discovered.Drafts
	}
	if configured.Archive == "" {
		configured.Archive = discovered.Archive
	}
	if configured.Trash == "" {
		configured.Trash = discovered.Trash
	}
	if configured.Junk == "" {
		configured.Junk = discovered.Junk
	}
	return configured
}

func messageBefore(a, b model.Message) bool {
	aTime, bTime := messageTime(a), messageTime(b)
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	if a.ConnectionID != b.ConnectionID {
		return a.ConnectionID < b.ConnectionID
	}
	return a.UID < b.UID
}

func messageTime(message model.Message) time.Time {
	if !message.ReceivedAt.IsZero() {
		return message.ReceivedAt
	}
	return message.Date
}

func compareEvents(a, b model.Event) int {
	if compared := a.Start.Compare(b.Start); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.ConnectionID, b.ConnectionID); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.CollectionID, b.CollectionID); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.ID, b.ID); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.Href, b.Href); compared != 0 {
		return compared
	}
	return strings.Compare(a.RecurrenceID, b.RecurrenceID)
}

func publicConnection(connection model.Connection) model.Connection {
	if connection.Mail != nil {
		copy := *connection.Mail
		copy.Secret = model.SecretRef{}
		copy.SecretEnv = ""
		connection.Mail = &copy
	}
	if connection.Calendar != nil {
		copy := *connection.Calendar
		copy.URL = ""
		copy.URLSecret = model.SecretRef{}
		copy.Secret = model.SecretRef{}
		copy.URLSecretEnv = ""
		connection.Calendar = &copy
	}
	return connection
}

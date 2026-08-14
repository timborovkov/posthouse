package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/posthousehq/posthouse/internal/calendar"
	"github.com/posthousehq/posthouse/internal/config"
	postmail "github.com/posthousehq/posthouse/internal/mail"
	"github.com/posthousehq/posthouse/internal/model"
	"github.com/posthousehq/posthouse/internal/pagination"
	"github.com/posthousehq/posthouse/internal/selector"
)

type Service struct {
	store      *config.Store
	calendar   *calendar.Client
	mailSearch func(model.Connection, postmail.SearchOptions) (postmail.SearchResult, error)
}

func New(store *config.Store) *Service {
	return &Service{store: store, calendar: calendar.NewClient(nil), mailSearch: postmail.Search}
}

func (s *Service) ConfigPath() string {
	return s.store.Path()
}

func (s *Service) Connections(selection model.Selector) ([]model.Connection, error) {
	cfg, err := s.store.Load()
	if err != nil {
		return nil, err
	}
	if len(selection.Connections) == 0 && selection.Category == "" && len(selection.Labels) == 0 && selection.Capability == "" {
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
	UIDValidity uint32 `json:"uid_validity"`
	BeforeUID   uint32 `json:"before_uid,omitempty"`
}

type mailCursorPosition struct {
	Connections map[string]mailCursorState `json:"connections"`
}

func (s *Service) SearchMessages(selection model.Selector, options postmail.SearchOptions, requestedPageSize int, cursor string) (model.MessagePage, error) {
	pageSize, err := pagination.PageSize(requestedPageSize, 25, 100)
	if err != nil {
		return model.MessagePage{}, err
	}
	cfg, err := s.store.Load()
	if err != nil {
		return model.MessagePage{}, err
	}
	selection.Capability = "mail"
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
	position := mailCursorPosition{Connections: make(map[string]mailCursorState, len(connections))}
	if err := pagination.Decode(cursor, "messages", scope, &position); err != nil {
		return model.MessagePage{}, err
	}
	results := make(map[string]postmail.SearchResult, len(connections))
	for _, connection := range connections {
		state, hasState := position.Connections[connection.ID]
		if cursor != "" && !hasState {
			return model.MessagePage{}, fmt.Errorf("cursor is missing state for connection %s", connection.ID)
		}
		connectionOptions := options
		connectionOptions.Limit = pageSize + 1
		connectionOptions.BeforeUID = state.BeforeUID
		connectionOptions.ExpectedUIDValidity = state.UIDValidity
		result, err := s.mailSearch(connection, connectionOptions)
		if err != nil {
			return model.MessagePage{}, fmt.Errorf("connection %s: %w", connection.ID, err)
		}
		results[connection.ID] = result
		state.UIDValidity = result.UIDValidity
		if state.BeforeUID == 0 {
			state.BeforeUID = result.UIDNext
		}
		position.Connections[connection.ID] = state
	}
	indices := make(map[string]int, len(connections))
	consumedMinimumUID := make(map[string]uint32, len(connections))
	page := model.MessagePage{Messages: make([]model.Message, 0, pageSize)}
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
		if current := consumedMinimumUID[bestConnection]; current == 0 || best.UID < current {
			consumedMinimumUID[bestConnection] = best.UID
		}
	}
	hasMore := false
	for _, connection := range connections {
		result := results[connection.ID]
		if indices[connection.ID] < len(result.Messages) || result.HasMore {
			hasMore = true
		}
		if beforeUID := consumedMinimumUID[connection.ID]; beforeUID != 0 {
			state := position.Connections[connection.ID]
			state.BeforeUID = beforeUID
			position.Connections[connection.ID] = state
		}
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
	connection, err := selector.One(cfg.Connections, message.ConnectionID, "mail")
	if err != nil {
		return err
	}
	return postmail.Send(connection, message)
}

func (s *Service) ListEvents(ctx context.Context, selection model.Selector, start, end time.Time, query string, requestedPageSize int, cursor string) (model.EventPage, error) {
	pageSize, err := pagination.PageSize(requestedPageSize, 100, 500)
	if err != nil {
		return model.EventPage{}, err
	}
	cfg, err := s.store.Load()
	if err != nil {
		return model.EventPage{}, err
	}
	selection.Capability = "calendar"
	connections, err := selector.Match(cfg.Connections, selection)
	if err != nil {
		return model.EventPage{}, err
	}
	slices.SortFunc(connections, func(a, b model.Connection) int { return strings.Compare(a.ID, b.ID) })
	connectionIDs := make([]string, len(connections))
	for index, connection := range connections {
		connectionIDs[index] = connection.ID
	}
	position := struct {
		Start        time.Time `json:"start"`
		ConnectionID string    `json:"connection_id"`
		ID           string    `json:"id"`
	}{}
	var result []model.Event
	for _, connection := range connections {
		events, err := s.calendar.List(ctx, connection, start, end, query)
		if err != nil {
			return model.EventPage{}, fmt.Errorf("connection %s: %w", connection.ID, err)
		}
		result = append(result, events...)
	}
	slices.SortFunc(result, compareEvents)
	eventKeys := make([]struct {
		Start        time.Time `json:"start"`
		ConnectionID string    `json:"connection_id"`
		ID           string    `json:"id"`
	}, len(result))
	for index, event := range result {
		eventKeys[index].Start, eventKeys[index].ConnectionID, eventKeys[index].ID = event.Start, event.ConnectionID, event.ID
	}
	snapshot, err := pagination.Fingerprint(eventKeys)
	if err != nil {
		return model.EventPage{}, err
	}
	scope := struct {
		Selector      model.Selector `json:"selector"`
		ConnectionIDs []string       `json:"connection_ids"`
		Start         time.Time      `json:"start"`
		End           time.Time      `json:"end"`
		Query         string         `json:"query"`
		Snapshot      string         `json:"snapshot"`
	}{selection, connectionIDs, start, end, query, snapshot}
	if err := pagination.Decode(cursor, "events", scope, &position); err != nil {
		return model.EventPage{}, err
	}
	startIndex := 0
	if !position.Start.IsZero() {
		startIndex = len(result)
		cursorEvent := model.Event{Start: position.Start, ConnectionID: position.ConnectionID, ID: position.ID}
		for index, event := range result {
			if compareEvents(event, cursorEvent) > 0 {
				startIndex = index
				break
			}
		}
	}
	endIndex := min(startIndex+pageSize, len(result))
	page := model.EventPage{Events: result[startIndex:endIndex]}
	if endIndex < len(result) {
		last := page.Events[len(page.Events)-1]
		position.Start, position.ConnectionID, position.ID = last.Start, last.ConnectionID, last.ID
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

func messageBefore(a, b model.Message) bool {
	aTime, bTime := a.ReceivedAt, b.ReceivedAt
	if aTime.IsZero() {
		aTime = a.Date
	}
	if bTime.IsZero() {
		bTime = b.Date
	}
	if !aTime.Equal(bTime) {
		return aTime.After(bTime)
	}
	if a.ConnectionID != b.ConnectionID {
		return a.ConnectionID < b.ConnectionID
	}
	return a.UID > b.UID
}

func compareEvents(a, b model.Event) int {
	if compared := a.Start.Compare(b.Start); compared != 0 {
		return compared
	}
	if compared := strings.Compare(a.ConnectionID, b.ConnectionID); compared != 0 {
		return compared
	}
	return strings.Compare(a.ID, b.ID)
}

func publicConnection(connection model.Connection) model.Connection {
	if connection.Mail != nil {
		copy := *connection.Mail
		copy.SecretEnv = ""
		connection.Mail = &copy
	}
	if connection.Calendar != nil {
		copy := *connection.Calendar
		copy.URL = ""
		copy.URLSecretEnv = ""
		connection.Calendar = &copy
	}
	return connection
}

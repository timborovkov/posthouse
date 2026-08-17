package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
)

var APIBase = "https://gmail.googleapis.com/gmail/v1"
var CalendarAPIBase = "https://www.googleapis.com/calendar/v3"
var TokenURL string

const nativeUIDValidity uint32 = 1

type listResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
	NextPageToken string `json:"nextPageToken"`
}

type rawMessage struct {
	ID           string   `json:"id"`
	InternalDate string   `json:"internalDate"`
	LabelIDs     []string `json:"labelIds"`
	Raw          string   `json:"raw"`
	Payload      *struct {
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	} `json:"payload"`
}

func Search(ctx context.Context, connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return postmail.SearchResult{}, err
	}
	if options.Limit <= 0 {
		options.Limit = 25
	}
	query := url.Values{}
	query.Set("q", searchQuery(options))
	query.Set("maxResults", strconv.Itoa(options.Limit+1))
	pageToken := options.PageToken
	messages := make([]model.Message, 0, options.Limit+1)
	nextPage := ""
	for {
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		} else {
			query.Del("pageToken")
		}
		var listed listResponse
		if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/messages?"+query.Encode(), nil, &listed); err != nil {
			return postmail.SearchResult{}, err
		}
		for _, item := range listed.Messages {
			message, err := metadata(ctx, client, connection.ID, item.ID)
			if err != nil {
				return postmail.SearchResult{}, err
			}
			if !afterCursor(message, options) {
				continue
			}
			messages = append(messages, message)
			if len(messages) > options.Limit {
				break
			}
		}
		nextPage = listed.NextPageToken
		if len(messages) > options.Limit || nextPage == "" {
			break
		}
		pageToken = nextPage
	}
	hasMore := nextPage != "" || len(messages) > options.Limit
	if len(messages) > options.Limit {
		messages = messages[:options.Limit]
	}
	return postmail.SearchResult{Messages: messages, HasMore: hasMore, UIDValidity: nativeUIDValidity}, nil
}

func Get(ctx context.Context, connection model.Connection, id string) (postmail.FetchedMessage, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	var payload rawMessage
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/messages/"+url.PathEscape(id)+"?format=raw", nil, &payload); err != nil {
		return postmail.FetchedMessage{}, err
	}
	raw, err := decodeRaw(payload.Raw)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	fetched, err := postmail.ParseRFC822(raw)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	fetched.Detail.ConnectionID = connection.ID
	fetched.Detail.ID = payload.ID
	fetched.Detail.Folder = folderFromLabels(payload.LabelIDs)
	fetched.Detail.Unread = containsLabel(payload.LabelIDs, "UNREAD")
	fetched.Detail.Flagged = containsLabel(payload.LabelIDs, "STARRED")
	if ms, err := strconv.ParseInt(payload.InternalDate, 10, 64); err == nil {
		fetched.Detail.ReceivedAt = time.UnixMilli(ms).UTC()
	}
	return fetched, nil
}

func Send(ctx context.Context, connection model.Connection, raw []byte) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	body := map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}
	return doJSON(ctx, client, http.MethodPost, APIBase+"/users/me/messages/send", body, nil)
}

func Modify(ctx context.Context, connection model.Connection, id string, add, remove []string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodPost, APIBase+"/users/me/messages/"+url.PathEscape(id)+"/modify", map[string][]string{"addLabelIds": add, "removeLabelIds": remove}, nil)
}

func Trash(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodPost, APIBase+"/users/me/messages/"+url.PathEscape(id)+"/trash", struct{}{}, nil)
}

func Archive(ctx context.Context, connection model.Connection, id string) error {
	return Modify(ctx, connection, id, nil, []string{"INBOX"})
}

func CreateDraft(ctx context.Context, connection model.Connection, raw []byte) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	var created struct {
		ID      string `json:"id"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := doJSON(ctx, client, http.MethodPost, APIBase+"/users/me/drafts", map[string]any{"message": map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}}, &created); err != nil {
		return "", err
	}
	if created.Message.ID != "" {
		return created.Message.ID, nil
	}
	return created.ID, nil
}

func DeleteDraft(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodDelete, APIBase+"/users/me/drafts/"+url.PathEscape(id), nil, nil)
}

func Ping(ctx context.Context, connection model.Connection) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	var profile struct {
		EmailAddress string `json:"emailAddress"`
	}
	return doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/profile", nil, &profile)
}

func ListEvents(ctx context.Context, connection model.Connection, start, end time.Time, query string) ([]model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	if !start.IsZero() {
		values.Set("timeMin", start.UTC().Format(time.RFC3339))
	}
	if !end.IsZero() {
		values.Set("timeMax", end.UTC().Format(time.RFC3339))
	}
	if query != "" {
		values.Set("q", query)
	}
	values.Set("singleEvents", "true")
	var listed struct {
		Items []calendarEvent `json:"items"`
	}
	if err := doJSON(ctx, client, http.MethodGet, CalendarAPIBase+"/calendars/primary/events?"+values.Encode(), nil, &listed); err != nil {
		return nil, err
	}
	events := make([]model.Event, 0, len(listed.Items))
	for _, item := range listed.Items {
		events = append(events, item.model(connection.ID))
	}
	return events, nil
}

func PutEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return model.Event{}, err
	}
	payload := calendarEvent{
		ID:          event.ID,
		Summary:     event.Title,
		Description: event.Description,
		Location:    event.Location,
		Start:       calendarTime{DateTime: event.Start.UTC().Format(time.RFC3339)},
		End:         calendarTime{DateTime: event.End.UTC().Format(time.RFC3339)},
		Status:      event.Status,
		ICalUID:     event.ID,
	}
	method, path := http.MethodPost, CalendarAPIBase+"/calendars/primary/events"
	if !create && event.Href != "" {
		method, path = http.MethodPut, CalendarAPIBase+"/calendars/primary/events/"+url.PathEscape(event.Href)
	} else if !create && event.ID != "" {
		method, path = http.MethodPut, CalendarAPIBase+"/calendars/primary/events/"+url.PathEscape(event.ID)
	}
	var saved calendarEvent
	if err := doJSON(ctx, client, method, path, payload, &saved); err != nil {
		return model.Event{}, err
	}
	return saved.model(connection.ID), nil
}

func DeleteEvent(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodDelete, CalendarAPIBase+"/calendars/primary/events/"+url.PathEscape(id), nil, nil)
}

type calendarEvent struct {
	ID          string       `json:"id"`
	ICalUID     string       `json:"iCalUID,omitempty"`
	Summary     string       `json:"summary"`
	Description string       `json:"description,omitempty"`
	Location    string       `json:"location,omitempty"`
	Start       calendarTime `json:"start"`
	End         calendarTime `json:"end"`
	Status      string       `json:"status,omitempty"`
	ETag        string       `json:"etag,omitempty"`
}

type calendarTime struct {
	DateTime string `json:"dateTime,omitempty"`
	Date     string `json:"date,omitempty"`
}

func (item calendarEvent) model(connectionID string) model.Event {
	id := item.ICalUID
	if id == "" {
		id = item.ID
	}
	event := model.Event{ConnectionID: connectionID, ID: id, Href: item.ID, Title: item.Summary, Description: item.Description, Location: item.Location, Status: item.Status, ETag: item.ETag}
	event.Start = parseCalendarTime(item.Start)
	event.End = parseCalendarTime(item.End)
	event.AllDay = item.Start.Date != ""
	return event
}

func parseCalendarTime(value calendarTime) time.Time {
	if value.DateTime != "" {
		if parsed, err := time.Parse(time.RFC3339, value.DateTime); err == nil {
			return parsed.UTC()
		}
	}
	if value.Date != "" {
		if parsed, err := time.Parse("2006-01-02", value.Date); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func metadata(ctx context.Context, client *http.Client, connectionID, id string) (model.Message, error) {
	var payload rawMessage
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/messages/"+url.PathEscape(id)+"?format=metadata&metadataHeaders=From&metadataHeaders=To&metadataHeaders=Subject&metadataHeaders=Date&metadataHeaders=Message-Id", nil, &payload); err != nil {
		return model.Message{}, err
	}
	message := model.Message{ConnectionID: connectionID, ID: payload.ID, Folder: folderFromLabels(payload.LabelIDs), Unread: containsLabel(payload.LabelIDs, "UNREAD"), Flagged: containsLabel(payload.LabelIDs, "STARRED")}
	if ms, err := strconv.ParseInt(payload.InternalDate, 10, 64); err == nil {
		message.ReceivedAt = time.UnixMilli(ms).UTC()
	}
	if payload.Payload != nil {
		for _, header := range payload.Payload.Headers {
			switch strings.ToLower(header.Name) {
			case "subject":
				message.Subject = header.Value
			case "message-id":
				message.MessageID = header.Value
			case "from":
				message.From = []model.Address{{Email: header.Value}}
			case "to":
				message.To = []model.Address{{Email: header.Value}}
			case "date":
				if parsed, err := time.Parse(time.RFC1123Z, header.Value); err == nil {
					message.Date = parsed.UTC()
				}
			}
		}
	}
	return message, nil
}

func searchQuery(options postmail.SearchOptions) string {
	parts := make([]string, 0, 4)
	if options.Query != "" {
		parts = append(parts, options.Query)
	}
	if options.Unread {
		parts = append(parts, "is:unread")
	}
	if !options.Since.IsZero() {
		parts = append(parts, "after:"+options.Since.UTC().Format("2006/01/02"))
	}
	if !options.Before.IsZero() {
		parts = append(parts, "before:"+options.Before.UTC().Format("2006/01/02"))
	}
	switch strings.ToUpper(options.Folder) {
	case "", "INBOX":
		parts = append(parts, "in:inbox")
	case "SENT":
		parts = append(parts, "in:sent")
	case "DRAFTS", "DRAFT":
		parts = append(parts, "in:drafts")
	case "TRASH":
		parts = append(parts, "in:trash")
	}
	return strings.Join(parts, " ")
}

func afterCursor(message model.Message, options postmail.SearchOptions) bool {
	if options.CursorTime.IsZero() && options.CursorID == "" {
		return true
	}
	if !options.CursorTime.IsZero() {
		if message.ReceivedAt.After(options.CursorTime) {
			return false
		}
		if message.ReceivedAt.Before(options.CursorTime) {
			return true
		}
	}
	return message.ID != options.CursorID
}

func folderFromLabels(labels []string) string {
	for _, label := range labels {
		switch label {
		case "INBOX":
			return "INBOX"
		case "SENT":
			return "SENT"
		case "DRAFT":
			return "DRAFTS"
		case "TRASH":
			return "TRASH"
		}
	}
	return "INBOX"
}

func containsLabel(labels []string, want string) bool {
	for _, label := range labels {
		if label == want {
			return true
		}
	}
	return false
}

func decodeRaw(value string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(value)
	}
	if err != nil {
		return nil, fmt.Errorf("decode Gmail raw message: %w", err)
	}
	return raw, nil
}

func httpClient(ctx context.Context, connection model.Connection) (*http.Client, error) {
	secret := resolvedRefreshToken(connection)
	if secret == "" {
		return nil, fmt.Errorf("connection %s is missing an OAuth refresh token", connection.ID)
	}
	creds, err := oauth.CredentialsFor(oauth.ProviderGoogle)
	if err != nil {
		return nil, err
	}
	endpoint := oauth.GoogleEndpoint
	if TokenURL != "" {
		endpoint.TokenURL = TokenURL
	}
	return oauth.HTTPClient(ctx, oauth.Config{Credentials: creds, Endpoint: endpoint, Scopes: oauth.MailScopes(oauth.ProviderGoogle, true)}, secret)
}

func resolvedRefreshToken(connection model.Connection) string {
	if connection.Mail != nil && connection.Mail.ResolvedSecret != "" {
		return connection.Mail.ResolvedSecret
	}
	if connection.Calendar != nil {
		return connection.Calendar.ResolvedSecret
	}
	return ""
}

func doJSON(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(data))
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("gmail API unauthorized; refresh the connection token")
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("gmail API %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	if dest == nil || len(payload) == 0 {
		return nil
	}
	return json.Unmarshal(payload, dest)
}

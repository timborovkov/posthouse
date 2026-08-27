package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/timborovkov/posthouse/internal/config"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
)

var APIBase = "https://gmail.googleapis.com/gmail/v1"
var CalendarAPIBase = "https://www.googleapis.com/calendar/v3"
var UserInfoURL = "https://openidconnect.googleapis.com/v1/userinfo"
var TokenURL string

const nativeUIDValidity uint32 = 1
const gmailBatchLimit = 50
const gmailJSONLimit int64 = 8 << 20
const gmailRawJSONLimit = postmail.MaxMessageBytes + postmail.MaxMessageBytes/2
const maxEventPages = 50
const metadataQuery = "format=metadata&metadataHeaders=From&metadataHeaders=To&metadataHeaders=Subject&metadataHeaders=Date&metadataHeaders=Message-Id"

type listResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
	NextPageToken string `json:"nextPageToken"`
}

type rawMessage struct {
	ID           string        `json:"id"`
	InternalDate string        `json:"internalDate"`
	LabelIDs     []string      `json:"labelIds"`
	Snippet      string        `json:"snippet"`
	Raw          string        `json:"raw"`
	Payload      *gmailPayload `json:"payload"`
}

type gmailPayload struct {
	Filename string `json:"filename"`
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body *struct {
		AttachmentID string `json:"attachmentId"`
	} `json:"body"`
	Parts []gmailPayload `json:"parts"`
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
	if includeSpamTrash(options.Folder) {
		query.Set("includeSpamTrash", "true")
	}
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
		ids := make([]string, 0, len(listed.Messages))
		for _, item := range listed.Messages {
			ids = append(ids, item.ID)
		}
		page, err := metadataMany(ctx, client, connection.ID, ids)
		if err != nil {
			return postmail.SearchResult{}, err
		}
		for _, message := range page {
			if !afterCursor(message, options) {
				continue
			}
			if !options.Since.IsZero() && message.ReceivedAt.Before(options.Since) {
				continue
			}
			if !options.Before.IsZero() && !message.ReceivedAt.Before(options.Before) {
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
	if err := doJSONLimit(ctx, client, http.MethodGet, APIBase+"/users/me/messages/"+url.PathEscape(id)+"?format=raw", nil, &payload, gmailRawJSONLimit); err != nil {
		return postmail.FetchedMessage{}, err
	}
	raw, err := decodeRaw(payload.Raw)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	if int64(len(raw)) > postmail.MaxMessageBytes {
		return postmail.FetchedMessage{}, fmt.Errorf("message exceeds 64 MiB read limit")
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
	if created.ID != "" {
		return created.ID, nil
	}
	return created.Message.ID, nil
}

func UpdateDraft(ctx context.Context, connection model.Connection, id string, raw []byte) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	draftID, err := resolveDraftID(ctx, client, id)
	if err != nil {
		return "", err
	}
	var saved struct {
		ID string `json:"id"`
	}
	if err := doJSON(ctx, client, http.MethodPut, APIBase+"/users/me/drafts/"+url.PathEscape(draftID), map[string]any{"message": map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}}, &saved); err != nil {
		return "", err
	}
	if saved.ID != "" {
		return saved.ID, nil
	}
	return draftID, nil
}

func DeleteDraft(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	draftID, err := resolveDraftID(ctx, client, id)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodDelete, APIBase+"/users/me/drafts/"+url.PathEscape(draftID), nil, nil)
}

func Untrash(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodPost, APIBase+"/users/me/messages/"+url.PathEscape(id)+"/untrash", struct{}{}, nil)
}

func resolveDraftID(ctx context.Context, client *http.Client, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("draft id is required")
	}
	var draft struct {
		ID string `json:"id"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/drafts/"+url.PathEscape(id)+"?format=minimal", nil, &draft); err == nil && strings.TrimSpace(draft.ID) != "" {
		return draft.ID, nil
	}
	endpoint := APIBase + "/users/me/drafts?maxResults=100"
	for page := 0; page < maxEventPages; page++ {
		var listed struct {
			Drafts []struct {
				ID      string `json:"id"`
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			} `json:"drafts"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, &listed); err != nil {
			return "", err
		}
		for _, item := range listed.Drafts {
			if item.ID == id || item.Message.ID == id {
				if item.ID != "" {
					return item.ID, nil
				}
			}
		}
		if listed.NextPageToken == "" {
			break
		}
		endpoint = APIBase + "/users/me/drafts?maxResults=100&pageToken=" + url.QueryEscape(listed.NextPageToken)
	}
	return "", fmt.Errorf("gmail draft %s was not found", id)
}

func Ping(ctx context.Context, connection model.Connection) error {
	_, err := ProfileEmail(ctx, connection)
	return err
}

func ProfileEmail(ctx context.Context, connection model.Connection) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	var profile struct {
		EmailAddress string `json:"emailAddress"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/profile", nil, &profile); err != nil {
		return "", err
	}
	email := strings.TrimSpace(profile.EmailAddress)
	if email == "" {
		return "", fmt.Errorf("gmail profile did not include an email address")
	}
	return email, nil
}

func UserInfoEmail(ctx context.Context, connection model.Connection) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	var profile struct {
		Email string `json:"email"`
	}
	if err := doJSON(ctx, client, http.MethodGet, UserInfoURL, nil, &profile); err != nil {
		return "", err
	}
	email := strings.TrimSpace(profile.Email)
	if email == "" {
		return "", fmt.Errorf("Google userinfo did not include an email address")
	}
	return email, nil
}

func UnreadCount(ctx context.Context, connection model.Connection, folder string) (int, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return 0, err
	}
	label := unreadLabel(folder)
	var payload struct {
		MessagesUnread int `json:"messagesUnread"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/labels/"+url.PathEscape(label), nil, &payload); err != nil {
		return 0, err
	}
	return payload.MessagesUnread, nil
}

func Snapshot(ctx context.Context, connection model.Connection, id string) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("message id is required")
	}
	var message struct {
		HistoryID json.Number `json:"historyId"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/messages/"+url.PathEscape(id)+"?format=minimal", nil, &message); err == nil {
		if strings.TrimSpace(message.HistoryID.String()) != "" {
			return message.HistoryID.String(), nil
		}
	}
	var draft struct {
		Message struct {
			HistoryID json.Number `json:"historyId"`
		} `json:"message"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/drafts/"+url.PathEscape(id)+"?format=minimal", nil, &draft); err == nil {
		if strings.TrimSpace(draft.Message.HistoryID.String()) != "" {
			return draft.Message.HistoryID.String(), nil
		}
	}
	return "", fmt.Errorf("gmail message %s is missing a history id", id)
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
	values.Set("maxResults", "2500")
	events := make([]model.Event, 0)
	pageToken := ""
	for page := 0; page < maxEventPages; page++ {
		if pageToken != "" {
			values.Set("pageToken", pageToken)
		} else {
			values.Del("pageToken")
		}
		var listed struct {
			Items         []calendarEvent `json:"items"`
			NextPageToken string          `json:"nextPageToken"`
		}
		if err := doJSON(ctx, client, http.MethodGet, CalendarAPIBase+"/calendars/primary/events?"+values.Encode(), nil, &listed); err != nil {
			return nil, err
		}
		for _, item := range listed.Items {
			events = append(events, item.model(connection.ID))
		}
		if listed.NextPageToken == "" {
			return events, nil
		}
		pageToken = listed.NextPageToken
	}
	return nil, fmt.Errorf("gmail calendar listing exceeded %d pages", maxEventPages)
}

func PutEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return model.Event{}, err
	}
	payload := calendarEvent{
		Summary:     event.Title,
		Description: event.Description,
		Location:    event.Location,
		Status:      event.Status,
		Attendees:   calendarAttendees(event.Attendees),
		Recurrence:  calendarRecurrence(event),
	}
	if event.AllDay {
		payload.Start = calendarTime{Date: civilDate(event.Start)}
		payload.End = calendarTime{Date: civilDate(event.End)}
	} else {
		payload.Start = calendarTime{DateTime: event.Start.UTC().Format(time.RFC3339)}
		payload.End = calendarTime{DateTime: event.End.UTC().Format(time.RFC3339)}
	}
	method, path := http.MethodPost, CalendarAPIBase+"/calendars/primary/events"
	var headers http.Header
	if create {
		payload.ICalUID = event.ID
	} else {
		id := event.Href
		if id == "" {
			id = event.ID
		}
		method, path = http.MethodPut, CalendarAPIBase+"/calendars/primary/events/"+url.PathEscape(id)
		if strings.TrimSpace(event.ETag) != "" {
			headers = http.Header{"If-Match": []string{event.ETag}}
		}
	}
	var saved calendarEvent
	if err := doJSONHeaders(ctx, client, method, path, payload, &saved, headers, gmailJSONLimit); err != nil {
		return model.Event{}, err
	}
	return saved.model(connection.ID), nil
}

func DeleteEvent(ctx context.Context, connection model.Connection, id, etag string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	var headers http.Header
	if strings.TrimSpace(etag) != "" {
		headers = http.Header{"If-Match": []string{etag}}
	}
	return doJSONHeaders(ctx, client, http.MethodDelete, CalendarAPIBase+"/calendars/primary/events/"+url.PathEscape(id), nil, nil, headers, gmailJSONLimit)
}

type calendarEvent struct {
	ID          string             `json:"id,omitempty"`
	ICalUID     string             `json:"iCalUID,omitempty"`
	Summary     string             `json:"summary"`
	Description string             `json:"description,omitempty"`
	Location    string             `json:"location,omitempty"`
	Start       calendarTime       `json:"start"`
	End         calendarTime       `json:"end"`
	Status      string             `json:"status,omitempty"`
	ETag        string             `json:"etag,omitempty"`
	Attendees   []calendarAttendee `json:"attendees,omitempty"`
	Recurrence  []string           `json:"recurrence,omitempty"`
}

type calendarAttendee struct {
	Email string `json:"email"`
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
	for _, attendee := range item.Attendees {
		if email := strings.TrimSpace(attendee.Email); email != "" {
			event.Attendees = append(event.Attendees, email)
		}
	}
	if rule := firstRecurrenceRule(item.Recurrence); rule != "" {
		event.RecurrenceRule = rule
	}
	return event
}

func calendarAttendees(values []string) []calendarAttendee {
	if len(values) == 0 {
		return nil
	}
	out := make([]calendarAttendee, 0, len(values))
	for _, value := range values {
		if email := strings.TrimSpace(value); email != "" {
			out = append(out, calendarAttendee{Email: email})
		}
	}
	return out
}

func calendarRecurrence(event model.Event) []string {
	lines := make([]string, 0, 1+len(event.RecurrenceDates)+len(event.ExceptionDates))
	if rule := strings.TrimSpace(event.RecurrenceRule); rule != "" {
		if !strings.HasPrefix(strings.ToUpper(rule), "RRULE:") {
			rule = "RRULE:" + rule
		}
		lines = append(lines, rule)
	}
	for _, value := range event.RecurrenceDates {
		lines = append(lines, "RDATE:"+value.UTC().Format("20060102T150405Z"))
	}
	for _, value := range event.ExceptionDates {
		lines = append(lines, "EXDATE:"+value.UTC().Format("20060102T150405Z"))
	}
	return lines
}

func firstRecurrenceRule(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), "RRULE:") {
			return strings.TrimSpace(trimmed[6:])
		}
	}
	return ""
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

func civilDate(value time.Time) string {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func metadataMany(ctx context.Context, client *http.Client, connectionID string, ids []string) ([]model.Message, error) {
	out := make([]model.Message, 0, len(ids))
	for start := 0; start < len(ids); start += gmailBatchLimit {
		end := start + gmailBatchLimit
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		if len(chunk) == 1 {
			message, err := metadata(ctx, client, connectionID, chunk[0])
			if err != nil {
				return nil, err
			}
			out = append(out, message)
			continue
		}
		page, err := metadataBatch(ctx, client, connectionID, chunk)
		if err != nil {
			for _, id := range chunk {
				message, metaErr := metadata(ctx, client, connectionID, id)
				if metaErr != nil {
					return nil, metaErr
				}
				out = append(out, message)
			}
			continue
		}
		out = append(out, page...)
	}
	return out, nil
}

func batchEndpoint() string {
	return strings.TrimSuffix(APIBase, "/gmail/v1") + "/batch/gmail/v1"
}

func metadataBatch(ctx context.Context, client *http.Client, connectionID string, ids []string) ([]model.Message, error) {
	const boundary = "batch_posthouse"
	var body bytes.Buffer
	for index, id := range ids {
		fmt.Fprintf(&body, "--%s\r\nContent-Type: application/http\r\nContent-ID: <%d>\r\n\r\nGET /gmail/v1/users/me/messages/%s?%s\r\n\r\n", boundary, index, url.PathEscape(id), metadataQuery)
	}
	fmt.Fprintf(&body, "--%s--\r\n", boundary)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, batchEndpoint(), &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "multipart/mixed; boundary="+boundary)
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 300 {
		return nil, fmt.Errorf("gmail batch %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	_, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return nil, fmt.Errorf("gmail batch response missing multipart boundary")
	}
	reader := multipart.NewReader(bytes.NewReader(payload), params["boundary"])
	byIndex := map[int]model.Message{}
	order := 0
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("gmail batch part: %w", err)
		}
		raw, err := io.ReadAll(io.LimitReader(part, 1<<20))
		if err != nil {
			return nil, err
		}
		status, jsonBody, err := decodeApplicationHTTP(raw)
		if err != nil {
			return nil, err
		}
		if status >= 300 {
			return nil, fmt.Errorf("gmail batch item %s: %s", strings.TrimSpace(string(jsonBody)), strings.TrimSpace(string(jsonBody)))
		}
		var payload rawMessage
		if err := json.Unmarshal(jsonBody, &payload); err != nil {
			return nil, fmt.Errorf("gmail batch item: %w", err)
		}
		message := messageFromRaw(connectionID, payload)
		index := batchIndex(part.Header, order)
		byIndex[index] = message
		order++
	}
	messages := make([]model.Message, 0, len(ids))
	for index := range ids {
		message, ok := byIndex[index]
		if !ok {
			return nil, fmt.Errorf("gmail batch missing item %d", index)
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func batchIndex(header textproto.MIMEHeader, fallback int) int {
	id := strings.Trim(header.Get("Content-ID"), " <>")
	id = strings.TrimPrefix(id, "response-")
	if n, err := strconv.Atoi(id); err == nil {
		return n
	}
	return fallback
}

func decodeApplicationHTTP(payload []byte) (int, []byte, error) {
	payload = bytes.ReplaceAll(payload, []byte("\r\n"), []byte("\n"))
	header, body, ok := bytes.Cut(payload, []byte("\n\n"))
	if !ok {
		return 0, nil, fmt.Errorf("gmail batch part missing body")
	}
	first, _, _ := strings.Cut(string(header), "\n")
	fields := strings.Fields(first)
	if len(fields) < 2 {
		return 0, nil, fmt.Errorf("gmail batch part status %q", first)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, fmt.Errorf("gmail batch part status %q", first)
	}
	return status, bytes.TrimSpace(body), nil
}

func metadata(ctx context.Context, client *http.Client, connectionID, id string) (model.Message, error) {
	var payload rawMessage
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/users/me/messages/"+url.PathEscape(id)+"?"+metadataQuery, nil, &payload); err != nil {
		return model.Message{}, err
	}
	return messageFromRaw(connectionID, payload), nil
}

func messageFromRaw(connectionID string, payload rawMessage) model.Message {
	message := model.Message{
		ConnectionID:   connectionID,
		ID:             payload.ID,
		Folder:         folderFromLabels(payload.LabelIDs),
		Unread:         containsLabel(payload.LabelIDs, "UNREAD"),
		Flagged:        containsLabel(payload.LabelIDs, "STARRED"),
		Preview:        payload.Snippet,
		HasAttachments: payloadHasAttachment(payload.Payload),
	}
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
	return message
}

func payloadHasAttachment(payload *gmailPayload) bool {
	if payload == nil {
		return false
	}
	if strings.TrimSpace(payload.Filename) != "" {
		return true
	}
	if payload.Body != nil && strings.TrimSpace(payload.Body.AttachmentID) != "" {
		return true
	}
	for index := range payload.Parts {
		if payloadHasAttachment(&payload.Parts[index]) {
			return true
		}
	}
	return false
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
		parts = append(parts, "after:"+strconv.FormatInt(options.Since.UTC().Unix(), 10))
	}
	if !options.Before.IsZero() {
		parts = append(parts, "before:"+strconv.FormatInt(options.Before.UTC().Unix(), 10))
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
	case "SPAM", "JUNK":
		parts = append(parts, "in:spam")
	}
	return strings.Join(parts, " ")
}

func includeSpamTrash(folder string) bool {
	switch strings.ToUpper(strings.TrimSpace(folder)) {
	case "TRASH", "SPAM", "JUNK":
		return true
	default:
		return false
	}
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
	return options.CursorID == "" || message.ID > options.CursorID
}

func unreadLabel(folder string) string {
	switch strings.ToUpper(strings.TrimSpace(folder)) {
	case "", "INBOX":
		return "INBOX"
	case "SENT":
		return "SENT"
	case "DRAFTS", "DRAFT":
		return "DRAFT"
	case "TRASH":
		return "TRASH"
	case "SPAM", "JUNK":
		return "SPAM"
	default:
		return folder
	}
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
	return oauth.HTTPClient(ctx, oauth.Config{
		Credentials:    creds,
		Endpoint:       endpoint,
		Scopes:         oauth.MailScopes(oauth.ProviderGoogle, true),
		PersistRefresh: config.PersistOAuthRefresh(connection),
	}, secret)
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
	return doJSONHeaders(ctx, client, method, rawURL, body, dest, nil, gmailJSONLimit)
}

func doJSONLimit(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any, limit int64) error {
	return doJSONHeaders(ctx, client, method, rawURL, body, dest, nil, limit)
}

func doJSONHeaders(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any, headers http.Header, limit int64) error {
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
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if limit <= 0 {
		limit = gmailJSONLimit
	}
	payload, err := postmail.ReadBounded(response.Body, limit)
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

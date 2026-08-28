package microsoft

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/timborovkov/posthouse/internal/config"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
)

var APIBase = "https://graph.microsoft.com/v1.0"
var TokenURL string

const nativeUIDValidity uint32 = 1
const graphJSONLimit int64 = 8 << 20
const maxEventPages = 50
const graphSimpleMIMELimit = 4 << 20
const immutableIDPrefer = `IdType="ImmutableId"`

var wellKnownFolders sync.Map

type graphMessage struct {
	ID                string      `json:"id"`
	Subject           string      `json:"subject"`
	BodyPreview       string      `json:"bodyPreview"`
	ReceivedDateTime  string      `json:"receivedDateTime"`
	SentDateTime      string      `json:"sentDateTime"`
	IsRead            bool        `json:"isRead"`
	Flag              *graphFlag  `json:"flag"`
	HasAttachments    bool        `json:"hasAttachments"`
	InternetMessageID string      `json:"internetMessageId"`
	From              *graphAddr  `json:"from"`
	ToRecipients      []graphAddr `json:"toRecipients"`
	ParentFolderID    string      `json:"parentFolderId"`
}

type graphFlag struct {
	FlagStatus string `json:"flagStatus"`
}

type graphAddr struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type graphEvent struct {
	ID             string `json:"id"`
	ICalUID        string `json:"iCalUId"`
	SeriesMasterID string `json:"seriesMasterId"`
	Type           string `json:"type"`
	Subject        string `json:"subject"`
	BodyPreview    string `json:"bodyPreview"`
	Body           *struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	Location *struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Start         *graphDateTime  `json:"start"`
	End           *graphDateTime  `json:"end"`
	OriginalStart *graphDateTime  `json:"originalStart"`
	IsAllDay      bool            `json:"isAllDay"`
	ETag          string          `json:"@odata.etag"`
	ChangeKey     string          `json:"changeKey"`
	Attendees     []graphAttendee `json:"attendees"`
}

type graphAttendee struct {
	EmailAddress struct {
		Address string `json:"address"`
	} `json:"emailAddress"`
}

type graphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

func Search(ctx context.Context, connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return postmail.SearchResult{}, err
	}
	if options.Limit <= 0 {
		options.Limit = 25
	}
	endpoint := APIBase + mailFolderPath(options.Folder) + "/messages"
	values := url.Values{}
	values.Set("$top", fmt.Sprint(options.Limit+1))
	values.Set("$select", "id,subject,bodyPreview,receivedDateTime,isRead,flag,hasAttachments,internetMessageId,from,toRecipients,parentFolderId")
	if options.Query == "" {
		values.Set("$orderby", "receivedDateTime desc")
		if options.Unread || !options.Since.IsZero() || !options.Before.IsZero() {
			// Graph InefficientFilter: $orderby properties must appear first in $filter.
			since := "1970-01-01T00:00:00Z"
			if !options.Since.IsZero() {
				since = options.Since.UTC().Format(time.RFC3339)
			}
			filters := []string{"receivedDateTime ge " + since}
			if !options.Before.IsZero() {
				filters = append(filters, "receivedDateTime lt "+options.Before.UTC().Format(time.RFC3339))
			}
			if options.Unread {
				filters = append(filters, "isRead eq false")
			}
			values.Set("$filter", strings.Join(filters, " and "))
		}
	} else {
		values.Set("$search", `"`+strings.ReplaceAll(options.Query, `"`, ``)+`"`)
	}
	endpoint += "?" + values.Encode()
	messages := make([]model.Message, 0, options.Limit+1)
	listedFolder := canonicalMailFolder(options.Folder)
	for {
		var listed struct {
			Value    []graphMessage `json:"value"`
			NextLink string         `json:"@odata.nextLink"`
		}
		if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, &listed); err != nil {
			return postmail.SearchResult{}, err
		}
		for _, item := range listed.Value {
			message := item.model(connection.ID, listedFolder)
			if options.Query != "" {
				if options.Unread && !message.Unread {
					continue
				}
				if !options.Since.IsZero() && message.ReceivedAt.Before(options.Since) {
					continue
				}
				if !options.Before.IsZero() && !message.ReceivedAt.Before(options.Before) {
					continue
				}
			}
			if options.CursorTime.IsZero() && options.CursorID == "" || afterCursor(message, options) {
				messages = append(messages, message)
				if len(messages) > options.Limit {
					break
				}
			}
		}
		if len(listed.Value) == 0 || len(messages) > options.Limit || listed.NextLink == "" {
			hasMore := listed.NextLink != "" || len(messages) > options.Limit
			if len(messages) > options.Limit {
				messages = messages[:options.Limit]
			}
			return postmail.SearchResult{Messages: messages, HasMore: hasMore, UIDValidity: nativeUIDValidity}, nil
		}
		endpoint = listed.NextLink
	}
}

func Get(ctx context.Context, connection model.Connection, id string) (postmail.FetchedMessage, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	var meta graphMessage
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me/messages/"+url.PathEscape(id)+"?$select=id,parentFolderId,isRead,flag,receivedDateTime,internetMessageId", nil, &meta); err != nil {
		return postmail.FetchedMessage{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/me/messages/"+url.PathEscape(id)+"/$value", nil)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	applyGraphHeaders(request, false)
	response, err := client.Do(request)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	defer response.Body.Close()
	raw, err := postmail.ReadBounded(response.Body, postmail.MaxMessageBytes)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	if response.StatusCode >= 300 {
		return postmail.FetchedMessage{}, fmt.Errorf("graph API %s: %s", response.Status, strings.TrimSpace(string(raw)))
	}
	fetched, err := postmail.ParseRFC822(raw)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	fetched.Detail.ConnectionID = connection.ID
	fetched.Detail.ID = id
	fetched.Detail.Folder = folderFromParent(ctx, client, connection.ID, meta.ParentFolderID)
	fetched.Detail.Unread = !meta.IsRead
	fetched.Detail.Flagged = meta.Flag != nil && meta.Flag.FlagStatus == "flagged"
	fetched.Detail.MessageID = meta.InternetMessageID
	if parsed, err := time.Parse(time.RFC3339, meta.ReceivedDateTime); err == nil {
		fetched.Detail.ReceivedAt = parsed.UTC()
		if fetched.Detail.Date.IsZero() {
			fetched.Detail.Date = parsed.UTC()
		}
	}
	return fetched, nil
}

func Send(ctx context.Context, connection model.Connection, raw []byte) error {
	if err := RejectOversizedMIME(raw); err != nil {
		return err
	}
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBase+"/me/sendMail", strings.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "text/plain")
	applyGraphHeaders(request, false)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("graph API %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	return nil
}

func Mark(ctx context.Context, connection model.Connection, id string, seen, flagged *bool) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	patch := map[string]any{}
	if seen != nil {
		patch["isRead"] = *seen
	}
	if flagged != nil {
		status := "notFlagged"
		if *flagged {
			status = "flagged"
		}
		patch["flag"] = map[string]string{"flagStatus": status}
	}
	return doJSON(ctx, client, http.MethodPatch, APIBase+"/me/messages/"+url.PathEscape(id), patch, nil)
}

func Move(ctx context.Context, connection model.Connection, id, destination string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodPost, APIBase+"/me/messages/"+url.PathEscape(id)+"/move", map[string]string{"destinationId": graphDestination(destination)}, nil)
}

func CreateDraft(ctx context.Context, connection model.Connection, raw []byte) (string, error) {
	if err := RejectOversizedMIME(raw); err != nil {
		return "", err
	}
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, APIBase+"/me/messages", strings.NewReader(encoded))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "text/plain")
	applyGraphHeaders(request, false)
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode >= 300 {
		return "", fmt.Errorf("graph API %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	var created graphMessage
	if err := json.Unmarshal(payload, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

func DeleteMessage(ctx context.Context, connection model.Connection, id string) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	return doJSON(ctx, client, http.MethodDelete, APIBase+"/me/messages/"+url.PathEscape(id), nil, nil)
}

func RejectOversizedMIME(raw []byte) error {
	if base64.StdEncoding.EncodedLen(len(raw)) > graphSimpleMIMELimit {
		return fmt.Errorf("Microsoft Graph simple send is limited to 4 MiB; reduce attachments")
	}
	return nil
}

func rejectUnsupportedGraphStatus(status string) error {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "", "CONFIRMED":
		return nil
	default:
		return fmt.Errorf("Microsoft Graph calendar writes do not serialize event status %q; omit status or delete the event", status)
	}
}

func Ping(ctx context.Context, connection model.Connection) error {
	_, _, err := ProfileEmail(ctx, connection)
	return err
}

func ProfileEmail(ctx context.Context, connection model.Connection) (mail, upn string, err error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", "", err
	}
	var me struct {
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me?$select=mail,userPrincipalName", nil, &me); err != nil {
		return "", "", err
	}
	mail = strings.TrimSpace(me.Mail)
	upn = strings.TrimSpace(me.UserPrincipalName)
	if mail == "" && upn == "" {
		return "", "", fmt.Errorf("graph profile did not include an email address")
	}
	return mail, upn, nil
}

func UnreadCount(ctx context.Context, connection model.Connection, folder string) (int, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return 0, err
	}
	var payload struct {
		Unread int `json:"unreadItemCount"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+mailFolderPath(folder)+"?$select=unreadItemCount", nil, &payload); err != nil {
		return 0, err
	}
	return payload.Unread, nil
}

func Snapshot(ctx context.Context, connection model.Connection, id string) (string, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return "", err
	}
	var payload struct {
		ChangeKey string `json:"changeKey"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me/messages/"+url.PathEscape(id)+"?$select=id,changeKey", nil, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.ChangeKey) == "" {
		return "", fmt.Errorf("graph message %s is missing a change key", id)
	}
	return payload.ChangeKey, nil
}

func ListEvents(ctx context.Context, connection model.Connection, start, end time.Time, query string) ([]model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	values.Set("$select", "id,iCalUId,seriesMasterId,type,originalStart,subject,body,bodyPreview,location,start,end,isAllDay,changeKey,attendees")
	values.Set("$top", "1000")
	endpoint := APIBase + "/me/events"
	if !start.IsZero() && !end.IsZero() {
		endpoint = APIBase + "/me/calendarView"
		values.Set("startDateTime", start.UTC().Format(time.RFC3339))
		values.Set("endDateTime", end.UTC().Format(time.RFC3339))
	}
	if query != "" && start.IsZero() && end.IsZero() {
		values.Set("$search", `"`+strings.ReplaceAll(query, `"`, ``)+`"`)
	}
	endpoint += "?" + values.Encode()
	events := make([]model.Event, 0)
	for page := 0; page < maxEventPages; page++ {
		var listed struct {
			Value    []graphEvent `json:"value"`
			NextLink string       `json:"@odata.nextLink"`
		}
		if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, &listed); err != nil {
			return nil, err
		}
		for _, item := range listed.Value {
			if query != "" && !graphEventMatchesQuery(item, query) {
				continue
			}
			events = append(events, item.model(connection.ID))
		}
		if listed.NextLink == "" {
			return events, nil
		}
		endpoint = listed.NextLink
	}
	return nil, fmt.Errorf("graph calendar listing exceeded %d pages", maxEventPages)
}

func PutEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return model.Event{}, err
	}
	if event.RecurrenceRule != "" || len(event.RecurrenceDates) > 0 || len(event.ExceptionDates) > 0 || len(event.RecurrencePeriods) > 0 || event.RecurrenceRange != "" || event.RecurrenceID != "" {
		return model.Event{}, fmt.Errorf("Microsoft Graph calendar writes do not serialize recurrence; omit recurrence fields")
	}
	if err := rejectUnsupportedGraphStatus(event.Status); err != nil {
		return model.Event{}, err
	}
	start, end := graphEventTimes(event)
	payload := map[string]any{
		"subject":  event.Title,
		"body":     map[string]string{"contentType": "Text", "content": event.Description},
		"start":    start,
		"end":      end,
		"isAllDay": event.AllDay,
	}
	if !create || event.Location != "" {
		payload["location"] = map[string]string{"displayName": event.Location}
	}
	if attendees := graphAttendees(event.Attendees); len(attendees) > 0 {
		payload["attendees"] = attendees
	} else if !create {
		payload["attendees"] = []map[string]any{}
	}
	method, path := http.MethodPost, APIBase+"/me/events"
	var headers http.Header
	if !create {
		id := event.Href
		if id == "" {
			id = event.ID
		}
		method, path = http.MethodPatch, APIBase+"/me/events/"+url.PathEscape(id)
		if strings.TrimSpace(event.ETag) != "" {
			headers = http.Header{"If-Match": []string{event.ETag}}
		}
	}
	var saved graphEvent
	if err := doJSONHeaders(ctx, client, method, path, payload, &saved, headers); err != nil {
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
	return doJSONHeaders(ctx, client, http.MethodDelete, APIBase+"/me/events/"+url.PathEscape(id), nil, nil, headers)
}

func (item graphMessage) model(connectionID, folder string) model.Message {
	if folder == "" {
		folder = "INBOX"
	}
	message := model.Message{ConnectionID: connectionID, ID: item.ID, Folder: folder, Subject: item.Subject, Preview: item.BodyPreview, Unread: !item.IsRead, HasAttachments: item.HasAttachments, MessageID: item.InternetMessageID}
	if item.Flag != nil {
		message.Flagged = item.Flag.FlagStatus == "flagged"
	}
	if parsed, err := time.Parse(time.RFC3339, item.ReceivedDateTime); err == nil {
		message.ReceivedAt = parsed.UTC()
		message.Date = parsed.UTC()
	}
	if item.From != nil {
		message.From = []model.Address{{Name: item.From.EmailAddress.Name, Email: item.From.EmailAddress.Address}}
	}
	for _, to := range item.ToRecipients {
		message.To = append(message.To, model.Address{Name: to.EmailAddress.Name, Email: to.EmailAddress.Address})
	}
	return message
}

func (item graphEvent) model(connectionID string) model.Event {
	id := item.ICalUID
	if id == "" {
		id = item.ID
	}
	etag := item.ETag
	if etag == "" {
		etag = item.ChangeKey
	}
	event := model.Event{ConnectionID: connectionID, ID: id, SeriesID: id, Href: item.ID, Title: item.Subject, Description: item.description(), AllDay: item.IsAllDay, ETag: etag}
	if item.Location != nil {
		event.Location = item.Location.DisplayName
	}
	event.Start = parseGraphTime(item.Start)
	event.End = parseGraphTime(item.End)
	if item.Type == "occurrence" || item.Type == "exception" || (item.SeriesMasterID != "" && item.Type != "seriesMaster" && item.Type != "singleInstance") {
		original := parseGraphTime(item.OriginalStart)
		if original.IsZero() {
			original = event.Start
		}
		event.RecurrenceID = original.UTC().Format(time.RFC3339)
	}
	for _, attendee := range item.Attendees {
		if email := strings.TrimSpace(attendee.EmailAddress.Address); email != "" {
			event.Attendees = append(event.Attendees, email)
		}
	}
	return event
}

func (item graphEvent) description() string {
	if item.Body != nil && strings.TrimSpace(item.Body.Content) != "" {
		if strings.EqualFold(strings.TrimSpace(item.Body.ContentType), "html") {
			return postmail.HTMLToText(item.Body.Content)
		}
		return item.Body.Content
	}
	return item.BodyPreview
}

func graphEventMatchesQuery(item graphEvent, query string) bool {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	haystack := strings.ToLower(item.Subject + " " + item.description())
	if item.Location != nil {
		haystack += " " + strings.ToLower(item.Location.DisplayName)
	}
	return strings.Contains(haystack, needle)
}

func graphAttendees(values []string) []map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if email := strings.TrimSpace(value); email != "" {
			out = append(out, map[string]any{"emailAddress": map[string]string{"address": email}, "type": "required"})
		}
	}
	return out
}

func graphEventTimes(event model.Event) (map[string]string, map[string]string) {
	start := event.Start
	end := event.End
	if event.AllDay {
		start = time.Date(event.Start.Year(), event.Start.Month(), event.Start.Day(), 0, 0, 0, 0, time.UTC)
		end = time.Date(event.End.Year(), event.End.Month(), event.End.Day(), 0, 0, 0, 0, time.UTC)
	} else {
		start = event.Start.UTC()
		end = event.End.UTC()
	}
	return map[string]string{"dateTime": start.Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		map[string]string{"dateTime": end.Format("2006-01-02T15:04:05"), "timeZone": "UTC"}
}

func parseGraphTime(value *graphDateTime) time.Time {
	if value == nil {
		return time.Time{}
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05", value.DateTime); err == nil {
		return parsed.UTC()
	}
	if parsed, err := time.Parse(time.RFC3339, value.DateTime); err == nil {
		return parsed.UTC()
	}
	return time.Time{}
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

func canonicalMailFolder(folder string) string {
	switch strings.ToUpper(strings.TrimSpace(folder)) {
	case "", "INBOX":
		return "INBOX"
	case "SENT", "SENT ITEMS", "SENTITEMS":
		return "SENT"
	case "DRAFTS", "DRAFT":
		return "DRAFTS"
	case "TRASH", "DELETEDITEMS", "DELETED ITEMS":
		return "TRASH"
	case "ARCHIVE":
		return "ARCHIVE"
	case "JUNK", "SPAM", "JUNKEMAIL":
		return "JUNK"
	default:
		return folder
	}
}

func folderFromParent(ctx context.Context, client *http.Client, connectionID, parentID string) string {
	if strings.TrimSpace(parentID) == "" {
		return "INBOX"
	}
	names, err := loadWellKnownFolders(ctx, client, connectionID)
	if err != nil {
		return "INBOX"
	}
	if name, ok := names[parentID]; ok {
		return name
	}
	return "INBOX"
}

func loadWellKnownFolders(ctx context.Context, client *http.Client, connectionID string) (map[string]string, error) {
	key := APIBase + "\x00" + connectionID
	if cached, ok := wellKnownFolders.Load(key); ok {
		return cached.(map[string]string), nil
	}
	names := map[string]string{}
	for _, item := range []struct{ path, name string }{
		{"inbox", "INBOX"},
		{"sentitems", "SENT"},
		{"drafts", "DRAFTS"},
		{"deleteditems", "TRASH"},
		{"junkemail", "JUNK"},
		{"archive", "ARCHIVE"},
	} {
		var folder struct {
			ID string `json:"id"`
		}
		if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me/mailFolders/"+item.path+"?$select=id", nil, &folder); err != nil {
			continue
		}
		if folder.ID != "" {
			names[folder.ID] = item.name
		}
	}
	wellKnownFolders.Store(key, names)
	return names, nil
}

func mailFolderPath(folder string) string {
	switch strings.ToUpper(strings.TrimSpace(folder)) {
	case "", "INBOX":
		return "/me/mailFolders/inbox"
	case "SENT", "SENT ITEMS", "SENTITEMS":
		return "/me/mailFolders/sentitems"
	case "DRAFTS", "DRAFT":
		return "/me/mailFolders/drafts"
	case "TRASH", "DELETEDITEMS", "DELETED ITEMS":
		return "/me/mailFolders/deleteditems"
	case "ARCHIVE":
		return "/me/mailFolders/archive"
	case "JUNK", "SPAM", "JUNKEMAIL":
		return "/me/mailFolders/junkemail"
	default:
		return "/me/mailFolders/" + url.PathEscape(folder)
	}
}

func graphDestination(folder string) string {
	switch strings.ToUpper(strings.TrimSpace(folder)) {
	case "", "INBOX":
		return "inbox"
	case "SENT", "SENT ITEMS", "SENTITEMS":
		return "sentitems"
	case "DRAFTS", "DRAFT":
		return "drafts"
	case "TRASH", "DELETEDITEMS", "DELETED ITEMS":
		return "deleteditems"
	case "ARCHIVE":
		return "archive"
	case "JUNK", "SPAM", "JUNKEMAIL":
		return "junkemail"
	default:
		return folder
	}
}

func httpClient(ctx context.Context, connection model.Connection) (*http.Client, error) {
	secret := ""
	if connection.Mail != nil {
		secret = connection.Mail.ResolvedSecret
	}
	if secret == "" && connection.Calendar != nil {
		secret = connection.Calendar.ResolvedSecret
	}
	if secret == "" {
		return nil, fmt.Errorf("connection %s is missing an OAuth refresh token", connection.ID)
	}
	creds, err := oauth.CredentialsFor(oauth.ProviderMicrosoft)
	if err != nil {
		return nil, err
	}
	endpoint := oauth.MicrosoftEndpoint
	if TokenURL != "" {
		endpoint.TokenURL = TokenURL
	}
	return oauth.HTTPClient(ctx, oauth.Config{
		Credentials:    creds,
		Endpoint:       endpoint,
		Scopes:         oauth.MailScopes(oauth.ProviderMicrosoft, true),
		PersistRefresh: config.PersistOAuthRefresh(connection),
	}, secret)
}

func doJSON(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any) error {
	return doJSONHeaders(ctx, client, method, rawURL, body, dest, nil)
}

func doJSONHeaders(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any, headers http.Header) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	applyGraphHeaders(request, strings.Contains(rawURL, "$search=") || strings.Contains(rawURL, "%24search="))
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
	payload, err := postmail.ReadBounded(response.Body, graphJSONLimit)
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("graph API unauthorized; refresh the connection token")
	}
	if response.StatusCode >= 300 {
		return fmt.Errorf("graph API %s: %s", response.Status, strings.TrimSpace(string(payload)))
	}
	if dest == nil || len(payload) == 0 {
		return nil
	}
	return json.Unmarshal(payload, dest)
}

func applyGraphHeaders(request *http.Request, search bool) {
	request.Header.Set("Prefer", immutableIDPrefer)
	if search {
		request.Header.Set("ConsistencyLevel", "eventual")
	}
}

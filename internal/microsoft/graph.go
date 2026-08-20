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

	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
)

var APIBase = "https://graph.microsoft.com/v1.0"
var TokenURL string

const nativeUIDValidity uint32 = 1

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
	ID          string `json:"id"`
	ICalUID     string `json:"iCalUId"`
	Subject     string `json:"subject"`
	BodyPreview string `json:"bodyPreview"`
	Location    *struct {
		DisplayName string `json:"displayName"`
	} `json:"location"`
	Start    *graphDateTime `json:"start"`
	End      *graphDateTime `json:"end"`
	IsAllDay bool           `json:"isAllDay"`
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
	if options.PageToken != "" {
		endpoint = options.PageToken
	} else {
		values := url.Values{}
		values.Set("$top", fmt.Sprint(options.Limit+1))
		values.Set("$select", "id,subject,bodyPreview,receivedDateTime,isRead,flag,hasAttachments,internetMessageId,from,toRecipients,parentFolderId")
		if options.Query == "" {
			values.Set("$orderby", "receivedDateTime desc")
			filters := make([]string, 0, 3)
			if options.Unread {
				filters = append(filters, "isRead eq false")
			}
			if !options.Since.IsZero() {
				filters = append(filters, "receivedDateTime ge "+options.Since.UTC().Format(time.RFC3339))
			}
			if !options.Before.IsZero() {
				filters = append(filters, "receivedDateTime lt "+options.Before.UTC().Format(time.RFC3339))
			}
			if len(filters) > 0 {
				values.Set("$filter", strings.Join(filters, " and "))
			}
		} else {
			values.Set("$search", `"`+strings.ReplaceAll(options.Query, `"`, ``)+`"`)
			endpoint = APIBase + "/me/messages"
		}
		endpoint += "?" + values.Encode()
	}
	var listed struct {
		Value    []graphMessage `json:"value"`
		NextLink string         `json:"@odata.nextLink"`
	}
	if err := doJSON(ctx, client, http.MethodGet, endpoint, nil, &listed); err != nil {
		return postmail.SearchResult{}, err
	}
	messages := make([]model.Message, 0, len(listed.Value))
	listedFolder := canonicalMailFolder(options.Folder)
	for _, item := range listed.Value {
		folder := listedFolder
		if options.Query != "" {
			folder = folderFromParent(ctx, client, connection.ID, item.ParentFolderID)
		}
		message := item.model(connection.ID, folder)
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
		}
	}
	hasMore := listed.NextLink != "" || len(messages) > options.Limit
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
	var meta graphMessage
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me/messages/"+url.PathEscape(id)+"?$select=id,parentFolderId,isRead,flag,receivedDateTime,internetMessageId", nil, &meta); err != nil {
		return postmail.FetchedMessage{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, APIBase+"/me/messages/"+url.PathEscape(id)+"/$value", nil)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return postmail.FetchedMessage{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
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
	}
	return fetched, nil
}

func Send(ctx context.Context, connection model.Connection, raw []byte) error {
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

func Ping(ctx context.Context, connection model.Connection) error {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return err
	}
	var me struct {
		ID string `json:"id"`
	}
	return doJSON(ctx, client, http.MethodGet, APIBase+"/me?$select=id", nil, &me)
}

func ListEvents(ctx context.Context, connection model.Connection, start, end time.Time, query string) ([]model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return nil, err
	}
	values := url.Values{}
	values.Set("$select", "id,iCalUId,subject,bodyPreview,location,start,end,isAllDay")
	filters := make([]string, 0, 2)
	if !start.IsZero() {
		filters = append(filters, "start/dateTime ge '"+start.UTC().Format("2006-01-02T15:04:05")+`'`)
	}
	if !end.IsZero() {
		filters = append(filters, "start/dateTime lt '"+end.UTC().Format("2006-01-02T15:04:05")+`'`)
	}
	if len(filters) > 0 {
		values.Set("$filter", strings.Join(filters, " and "))
	}
	if query != "" {
		values.Set("$search", `"`+strings.ReplaceAll(query, `"`, ``)+`"`)
	}
	var listed struct {
		Value []graphEvent `json:"value"`
	}
	if err := doJSON(ctx, client, http.MethodGet, APIBase+"/me/events?"+values.Encode(), nil, &listed); err != nil {
		return nil, err
	}
	events := make([]model.Event, 0, len(listed.Value))
	for _, item := range listed.Value {
		if query != "" && !strings.Contains(strings.ToLower(item.Subject+" "+item.BodyPreview), strings.ToLower(query)) {
			continue
		}
		events = append(events, item.model(connection.ID))
	}
	return events, nil
}

func PutEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	client, err := httpClient(ctx, connection)
	if err != nil {
		return model.Event{}, err
	}
	payload := map[string]any{
		"subject":  event.Title,
		"body":     map[string]string{"contentType": "Text", "content": event.Description},
		"start":    map[string]string{"dateTime": event.Start.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"end":      map[string]string{"dateTime": event.End.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"},
		"isAllDay": event.AllDay,
	}
	if event.Location != "" {
		payload["location"] = map[string]string{"displayName": event.Location}
	}
	method, path := http.MethodPost, APIBase+"/me/events"
	if !create {
		id := event.Href
		if id == "" {
			id = event.ID
		}
		method, path = http.MethodPatch, APIBase+"/me/events/"+url.PathEscape(id)
	}
	var saved graphEvent
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
	return doJSON(ctx, client, http.MethodDelete, APIBase+"/me/events/"+url.PathEscape(id), nil, nil)
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
	event := model.Event{ConnectionID: connectionID, ID: id, Href: item.ID, Title: item.Subject, Description: item.BodyPreview, AllDay: item.IsAllDay}
	if item.Location != nil {
		event.Location = item.Location.DisplayName
	}
	event.Start = parseGraphTime(item.Start)
	event.End = parseGraphTime(item.End)
	return event
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
	return message.ID != options.CursorID
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
	return oauth.HTTPClient(ctx, oauth.Config{Credentials: creds, Endpoint: endpoint, Scopes: oauth.MailScopes(oauth.ProviderMicrosoft, true)}, secret)
}

func doJSON(ctx context.Context, client *http.Client, method, rawURL string, body any, dest any) error {
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
	if strings.Contains(rawURL, "$search=") {
		request.Header.Set("ConsistencyLevel", "eventual")
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

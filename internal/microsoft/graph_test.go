package microsoft

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
)

func TestSearchGetSendAndLabelAgainstFixture(t *testing.T) {
	raw := "From: a@example.test\r\nTo: b@example.test\r\nSubject: Hello\r\nDate: Mon, 02 Jan 2006 15:04:05 -0700\r\nMessage-ID: <hello@example.test>\r\n\r\nBody"
	var sent, patched bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Header.Get("Authorization") != "Bearer graph-access" {
			http.Error(writer, "missing bearer", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/mailFolders/inbox/messages"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "AAMkAG", "subject": "Hello", "bodyPreview": "Body", "receivedDateTime": "2026-08-17T12:00:00Z",
				"isRead": false, "internetMessageId": "<hello@example.test>",
				"from": map[string]any{"emailAddress": map[string]string{"address": "a@example.test"}},
			}}})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/$value"):
			_, _ = writer.Write([]byte(raw))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "AAMkAG", "parentFolderId": "", "isRead": false,
				"receivedDateTime": "2026-08-17T12:00:00Z", "internetMessageId": "<hello@example.test>",
			})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/sendMail"):
			sent = true
			body, _ := io.ReadAll(request.Body)
			if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(body))); err != nil {
				http.Error(writer, "expected base64 MIME", http.StatusBadRequest)
				return
			}
			writer.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodPatch && strings.Contains(request.URL.Path, "/messages/"):
			patched = true
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "AAMkAG", "isRead": true})
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/me"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "user-1", "mail": "me@contoso.test", "userPrincipalName": "me@contoso.test"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "ms", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}
	result, err := Search(context.Background(), connection, postmail.SearchOptions{Query: "", Limit: 10})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].ID != "AAMkAG" || !result.Messages[0].Unread || result.Messages[0].Folder != "INBOX" {
		t.Fatalf("Search = %#v, %v", result, err)
	}
	fetched, err := Get(context.Background(), connection, "AAMkAG")
	if err != nil || fetched.Detail.Subject != "Hello" || fetched.Detail.Folder != "INBOX" || !fetched.Detail.Unread {
		t.Fatalf("Get = %#v, %v", fetched, err)
	}
	if err := Send(context.Background(), connection, []byte(raw)); err != nil || !sent {
		t.Fatalf("Send = %v sent=%v", err, sent)
	}
	seen := true
	if err := Mark(context.Background(), connection, "AAMkAG", &seen, nil); err != nil || !patched {
		t.Fatalf("Mark = %v patched=%v", err, patched)
	}
	if err := Ping(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
}

func TestListAndPutCalendarEvents(t *testing.T) {
	var created bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/me/events") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "evt-1", "iCalUId": "uid-1", "subject": "Planning",
				"start": map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
				"end":   map[string]string{"dateTime": "2026-08-17T10:00:00", "timeZone": "UTC"},
			}}})
			return
		}
		if request.Method == http.MethodPost && request.URL.Path == "/me/events" {
			created = true
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "evt-2", "iCalUId": "uid-2", "subject": "New",
				"start": map[string]string{"dateTime": "2026-08-18T09:00:00", "timeZone": "UTC"},
				"end":   map[string]string{"dateTime": "2026-08-18T10:00:00", "timeZone": "UTC"},
			})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}
	events, err := ListEvents(context.Background(), connection, time.Time{}, time.Time{}, "")
	if err != nil || len(events) != 1 || events[0].ID != "uid-1" {
		t.Fatalf("ListEvents = %#v, %v", events, err)
	}
	written, err := PutEvent(context.Background(), connection, model.Event{Title: "New", Start: events[0].Start, End: events[0].End}, true)
	if err != nil || !created || written.Href != "evt-2" {
		t.Fatalf("PutEvent = %#v, %v created=%v", written, err, created)
	}
}

func TestGetReportsFolderFromParentFolderId(t *testing.T) {
	raw := "From: a@example.test\r\nTo: b@example.test\r\nSubject: Sent\r\n\r\nBody"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/$value"):
			_, _ = writer.Write([]byte(raw))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "sent-1", "parentFolderId": "folder-sent", "isRead": true,
				"receivedDateTime": "2026-08-17T12:00:00Z",
			})
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/me/mailFolders/"):
			ids := map[string]string{"inbox": "folder-inbox", "sentitems": "folder-sent", "drafts": "folder-drafts", "deleteditems": "folder-trash", "archive": "folder-archive"}
			name := strings.TrimPrefix(request.URL.Path, "/me/mailFolders/")
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": ids[name]})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "ms-sent", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}
	fetched, err := Get(context.Background(), connection, "sent-1")
	if err != nil || fetched.Detail.Folder != "SENT" || fetched.Detail.Unread {
		t.Fatalf("Get = %#v, %v", fetched, err)
	}
}

func TestSearchSentFolderKeepsSentLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/mailFolders/sentitems/messages") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "AAMkSent", "subject": "Sent", "receivedDateTime": "2026-08-17T12:00:00Z", "isRead": true,
			}}})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "ms-sent-list", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}
	result, err := Search(context.Background(), connection, postmail.SearchOptions{Folder: "SENT", Limit: 10})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].Folder != "SENT" {
		t.Fatalf("Search SENT = %#v, %v", result, err)
	}
}

func TestSearchQueryStaysInRequestedFolder(t *testing.T) {
	var sawInbox, sawAllMail bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Header.Get("Prefer") != `IdType="ImmutableId"` {
			http.Error(writer, "missing immutable id prefer", http.StatusBadRequest)
			return
		}
		switch {
		case strings.Contains(request.URL.Path, "/mailFolders/sentitems/messages"):
			sawInbox = true
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "sent-hit", "subject": "renewal", "receivedDateTime": "2026-08-17T12:00:00Z", "isRead": true,
				"parentFolderId": "folder-sent",
			}}})
		case request.URL.Path == "/me/messages":
			sawAllMail = true
			http.Error(writer, "search escaped the folder", http.StatusTeapot)
		case strings.HasPrefix(request.URL.Path, "/me/mailFolders/"):
			ids := map[string]string{"inbox": "folder-inbox", "sentitems": "folder-sent", "drafts": "folder-drafts", "deleteditems": "folder-trash", "archive": "folder-archive"}
			name := strings.TrimPrefix(request.URL.Path, "/me/mailFolders/")
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": ids[name]})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	result, err := Search(context.Background(), model.Connection{ID: "ms", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, postmail.SearchOptions{Folder: "SENT", Query: "renewal", Limit: 10})
	if err != nil || !sawInbox || sawAllMail || len(result.Messages) != 1 || result.Messages[0].Folder != "SENT" {
		t.Fatalf("Search SENT query = %#v err=%v inbox=%v all=%v", result, err, sawInbox, sawAllMail)
	}
}

func TestListEventsUsesCalendarViewAndFollowsNextLink(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		pages++
		if request.URL.Path == "/me/events" {
			http.Error(writer, "ranged lists must use calendarView", http.StatusTeapot)
			return
		}
		if request.URL.Path != "/me/calendarView" {
			http.NotFound(writer, request)
			return
		}
		if request.URL.Query().Get("$skiptoken") == "page-2" || strings.Contains(request.URL.RawQuery, "skiptoken") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "evt-2", "iCalUId": "uid-2", "subject": "Two",
				"start": map[string]string{"dateTime": "2026-08-17T11:00:00", "timeZone": "UTC"},
				"end":   map[string]string{"dateTime": "2026-08-17T12:00:00", "timeZone": "UTC"},
			}}})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"value": []map[string]any{{
				"id": "evt-1", "iCalUId": "uid-1", "subject": "One",
				"start": map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
				"end":   map[string]string{"dateTime": "2026-08-17T10:00:00", "timeZone": "UTC"},
			}},
			"@odata.nextLink": APIBase + "/me/calendarView?startDateTime=2026-08-17T00:00:00Z&endDateTime=2026-08-18T00:00:00Z&$skiptoken=page-2",
		})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	events, err := ListEvents(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, start, start.Add(24*time.Hour), "")
	if err != nil || pages != 2 || len(events) != 2 || events[1].ID != "uid-2" {
		t.Fatalf("ListEvents = %#v pages=%d err=%v", events, pages, err)
	}
}

func TestPutEventSerializesAttendeesAndAllDay(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "evt-2", "iCalUId": "uid-2", "subject": "Offsite", "isAllDay": true,
			"start":     map[string]string{"dateTime": "2026-08-17T00:00:00", "timeZone": "UTC"},
			"end":       map[string]string{"dateTime": "2026-08-18T00:00:00", "timeZone": "UTC"},
			"attendees": []map[string]any{{"emailAddress": map[string]string{"address": "a@example.test"}}},
		})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	written, err := PutEvent(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, model.Event{
		Title: "Offsite", Start: start, End: start.Add(24 * time.Hour), AllDay: true, Attendees: []string{"a@example.test"},
	}, true)
	if err != nil || !written.AllDay || len(written.Attendees) != 1 {
		t.Fatalf("PutEvent = %#v err=%v", written, err)
	}
	if body["isAllDay"] != true {
		t.Fatalf("payload = %#v", body)
	}
}

func TestListEventsUsesFullBodyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/me/events") {
			if !strings.Contains(request.URL.RawQuery, "body") {
				http.Error(writer, "missing body select", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "evt-1", "iCalUId": "uid-1", "subject": "Planning",
				"bodyPreview": "Dana, this is the time",
				"body":        map[string]string{"contentType": "text", "content": "Dana, this is the time you selected for our orientation. Please bring the notes."},
				"start":       map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
				"end":         map[string]string{"dateTime": "2026-08-17T10:00:00", "timeZone": "UTC"},
			}}})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	events, err := ListEvents(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, time.Time{}, time.Time{}, "")
	if err != nil || len(events) != 1 || !strings.Contains(events[0].Description, "Please bring the notes") {
		t.Fatalf("ListEvents body = %#v, %v", events, err)
	}
}

func TestSearchKeepsCustomFolderQueryHits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.Contains(request.URL.Path, "/mailFolders/AAMkCustom/messages") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "custom-1", "subject": "renewal", "receivedDateTime": "2026-08-17T12:00:00Z", "isRead": true,
				"parentFolderId": "folder-custom",
			}}})
			return
		}
		if strings.HasPrefix(request.URL.Path, "/me/mailFolders/") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "folder-well-known"})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	result, err := Search(context.Background(), model.Connection{ID: "ms", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, postmail.SearchOptions{Folder: "AAMkCustom", Query: "renewal", Limit: 10})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].ID != "custom-1" || result.Messages[0].Folder != "AAMkCustom" {
		t.Fatalf("Search custom folder = %#v, %v", result, err)
	}
}

func TestSearchUnreadFilterIncludesOrderedReceivedDateTime(t *testing.T) {
	var filter, order string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.Contains(request.URL.Path, "/mailFolders/inbox/messages") {
			filter = request.URL.Query().Get("$filter")
			order = request.URL.Query().Get("$orderby")
			if strings.Contains(filter, "isRead") && (!strings.Contains(filter, "receivedDateTime") || strings.Index(filter, "receivedDateTime") > strings.Index(filter, "isRead")) {
				http.Error(writer, `{"error":{"code":"InefficientFilter","message":"The restriction or sort order is too complex for this operation."}}`, http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
				"id": "unread-1", "subject": "Hello", "receivedDateTime": "2026-08-17T12:00:00Z", "isRead": false,
			}}})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	result, err := Search(context.Background(), model.Connection{ID: "ms", Mail: &model.MailConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, postmail.SearchOptions{Unread: true, Limit: 10})
	if err != nil || len(result.Messages) != 1 {
		t.Fatalf("Search unread = %#v, %v filter=%q order=%q", result, err, filter, order)
	}
	if !strings.Contains(order, "receivedDateTime") || !strings.Contains(filter, "isRead eq false") || !strings.Contains(filter, "receivedDateTime ge") {
		t.Fatalf("unread query filter=%q order=%q", filter, order)
	}
}

func TestAfterCursorEqualTimeUsesTotalOrder(t *testing.T) {
	stamp := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	options := postmail.SearchOptions{CursorTime: stamp, CursorID: "bbb"}
	if afterCursor(model.Message{ID: "aaa", ReceivedAt: stamp}, options) {
		t.Fatal("equal-time cursor re-admitted an earlier ID")
	}
	if afterCursor(model.Message{ID: "bbb", ReceivedAt: stamp}, options) {
		t.Fatal("cursor ID itself was re-admitted")
	}
	if !afterCursor(model.Message{ID: "ccc", ReceivedAt: stamp}, options) {
		t.Fatal("equal-time listing dropped a later ID")
	}
	if !afterCursor(model.Message{ID: "aaa", ReceivedAt: stamp.Add(-time.Hour)}, options) {
		t.Fatal("older message was dropped")
	}
	if afterCursor(model.Message{ID: "zzz", ReceivedAt: stamp.Add(time.Hour)}, options) {
		t.Fatal("newer message was kept")
	}
}

func TestPutEventAllDayKeepsLocalDate(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "evt-2", "iCalUId": "uid-2", "subject": "Offsite", "isAllDay": true,
			"start": map[string]string{"dateTime": "2026-08-17T00:00:00", "timeZone": "UTC"},
			"end":   map[string]string{"dateTime": "2026-08-18T00:00:00", "timeZone": "UTC"},
		})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	zone := time.FixedZone("AEST", 10*3600)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, zone)
	_, err := PutEvent(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, model.Event{
		Title: "Offsite", Start: start, End: start.Add(24 * time.Hour), AllDay: true,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	startPayload, _ := body["start"].(map[string]any)
	endPayload, _ := body["end"].(map[string]any)
	if startPayload["dateTime"] != "2026-08-17T00:00:00" || endPayload["dateTime"] != "2026-08-18T00:00:00" {
		t.Fatalf("all-day graph times = start %#v end %#v", body["start"], body["end"])
	}
}

func TestPutEventRejectsCancelledStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		http.Error(writer, "cancelled status must be rejected locally", http.StatusTeapot)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	_, err := PutEvent(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, model.Event{
		Title: "Planning", Start: start, End: start.Add(time.Hour), Status: "CANCELLED",
	}, true)
	if err == nil || !strings.Contains(err.Error(), "do not serialize event status") {
		t.Fatalf("PutEvent cancelled = %v", err)
	}
}

func TestPutEventUpdateClearsEmptyLocationAndAttendees(t *testing.T) {
	var body map[string]any
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		method, path = request.Method, request.URL.Path
		_ = json.NewDecoder(request.Body).Decode(&body)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id": "evt-1", "iCalUId": "uid-1", "subject": "Cleared",
			"start": map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
			"end":   map[string]string{"dateTime": "2026-08-17T10:00:00", "timeZone": "UTC"},
		})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	_, err := PutEvent(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, model.Event{
		Href: "evt-1", Title: "Cleared", Start: start, End: start.Add(time.Hour), ETag: `"etag-1"`,
	}, false)
	if err != nil || method != http.MethodPatch || !strings.HasSuffix(path, "/events/evt-1") {
		t.Fatalf("PutEvent update method=%s path=%s err=%v", method, path, err)
	}
	location, _ := body["location"].(map[string]any)
	if location["displayName"] != "" {
		t.Fatalf("update omitted empty location: %#v", body["location"])
	}
	attendees, ok := body["attendees"].([]any)
	if !ok || len(attendees) != 0 {
		t.Fatalf("update omitted empty attendees: %#v", body["attendees"])
	}
}

func TestRejectOversizedMIME(t *testing.T) {
	if err := RejectOversizedMIME(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	if err := RejectOversizedMIME(make([]byte, 3<<20+1)); err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("RejectOversizedMIME = %v", err)
	}
}

func TestMailFolderPathMapsJunk(t *testing.T) {
	if mailFolderPath("JUNK") != "/me/mailFolders/junkemail" || graphDestination("JUNK") != "junkemail" || canonicalMailFolder("JUNKEMAIL") != "JUNK" {
		t.Fatalf("junk mapping path=%s dest=%s folder=%s", mailFolderPath("JUNK"), graphDestination("JUNK"), canonicalMailFolder("JUNKEMAIL"))
	}
}

func TestListEventsNormalizesHTMLBodyAndMarksOccurrences(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"value": []map[string]any{{
			"id": "occ-1", "iCalUId": "uid-series", "seriesMasterId": "master-1", "type": "occurrence",
			"subject":       "Standup",
			"body":          map[string]string{"contentType": "HTML", "content": "<p>Bring the <b>notes</b>.</p>"},
			"originalStart": map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
			"start":         map[string]string{"dateTime": "2026-08-17T09:00:00", "timeZone": "UTC"},
			"end":           map[string]string{"dateTime": "2026-08-17T09:30:00", "timeZone": "UTC"},
		}}})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL, server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	events, err := ListEvents(context.Background(), model.Connection{ID: "ms", Calendar: &model.CalendarConfig{Kind: "microsoft", ResolvedSecret: "refresh-ms"}}, time.Time{}, time.Time{}, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("ListEvents = %#v, %v", events, err)
	}
	if events[0].RecurrenceID != "2026-08-17T09:00:00Z" || events[0].SeriesID != "uid-series" {
		t.Fatalf("occurrence identity = %#v", events[0])
	}
	if strings.Contains(events[0].Description, "<") || !strings.Contains(events[0].Description, "notes") {
		t.Fatalf("HTML body was not normalized: %q", events[0].Description)
	}
}

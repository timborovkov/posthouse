package gmail

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
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
	var sent, modified bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		auth := request.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(writer, "missing bearer", http.StatusUnauthorized)
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/gmail/v1/users/me/messages":
			_ = json.NewEncoder(writer).Encode(map[string]any{"messages": []map[string]string{{"id": "msg-1"}}})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/msg-1") && request.URL.Query().Get("format") == "metadata":
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "msg-1", "internalDate": "1152194645000", "labelIds": []string{"INBOX", "UNREAD"},
				"payload": map[string]any{"headers": []map[string]string{{"name": "Subject", "value": "Hello"}, {"name": "From", "value": "a@example.test"}}},
			})
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/msg-1"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "raw": encoded, "labelIds": []string{"INBOX"}, "internalDate": "1152194645000"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/messages/send"):
			sent = true
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "raw") {
				http.Error(writer, "missing raw", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "sent-1"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/modify"):
			modified = true
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1"})
		case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/profile"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"emailAddress": "you@gmail.com"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh-secret"}}
	result, err := Search(context.Background(), connection, postmail.SearchOptions{Query: "hello", Limit: 10})
	if err != nil || len(result.Messages) != 1 || result.Messages[0].ID != "msg-1" || result.Messages[0].Subject != "Hello" {
		t.Fatalf("Search = %#v, %v", result, err)
	}
	fetched, err := Get(context.Background(), connection, "msg-1")
	if err != nil || fetched.Detail.ID != "msg-1" || fetched.Detail.Subject != "Hello" {
		t.Fatalf("Get = %#v, %v", fetched, err)
	}
	if err := Send(context.Background(), connection, []byte(raw)); err != nil || !sent {
		t.Fatalf("Send = %v sent=%v", err, sent)
	}
	if err := Modify(context.Background(), connection, "msg-1", nil, []string{"UNREAD"}); err != nil || !modified {
		t.Fatalf("Modify = %v modified=%v", err, modified)
	}
	if err := Ping(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
}

func TestSearchBatchesMetadataForMultipleIDs(t *testing.T) {
	var batchHits, metaHits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/gmail/v1/users/me/messages":
			_ = json.NewEncoder(writer).Encode(map[string]any{"messages": []map[string]string{{"id": "msg-1"}, {"id": "msg-2"}}})
		case request.Method == http.MethodPost && request.URL.Path == "/batch/gmail/v1":
			batchHits++
			writer.Header().Set("Content-Type", "multipart/mixed; boundary=batch_fix")
			_, _ = writer.Write([]byte("--batch_fix\r\nContent-Type: application/http\r\nContent-ID: <0>\r\n\r\nHTTP/1.1 200 OK\r\n\r\n{\"id\":\"msg-1\",\"internalDate\":\"1152194645000\",\"labelIds\":[\"INBOX\"],\"payload\":{\"headers\":[{\"name\":\"Subject\",\"value\":\"One\"}]}}\r\n--batch_fix\r\nContent-Type: application/http\r\nContent-ID: <1>\r\n\r\nHTTP/1.1 200 OK\r\n\r\n{\"id\":\"msg-2\",\"internalDate\":\"1152194645000\",\"labelIds\":[\"SENT\"],\"payload\":{\"headers\":[{\"name\":\"Subject\",\"value\":\"Two\"}]}}\r\n--batch_fix--\r\n"))
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/") && request.URL.Query().Get("format") == "metadata":
			metaHits++
			http.Error(writer, "search must batch metadata", http.StatusTeapot)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	connection := model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh-secret"}}
	result, err := Search(context.Background(), connection, postmail.SearchOptions{Limit: 10})
	if err != nil || len(result.Messages) != 2 || result.Messages[0].Subject != "One" || result.Messages[1].Folder != "SENT" {
		t.Fatalf("Search = %#v, %v", result, err)
	}
	if batchHits != 1 || metaHits != 0 {
		t.Fatalf("batchHits=%d metaHits=%d", batchHits, metaHits)
	}
}

func TestListAndPutCalendarEvents(t *testing.T) {
	var created bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/calendars/primary/events") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"items": []map[string]any{{
				"id": "evt-1", "iCalUID": "uid-1", "summary": "Planning",
				"start": map[string]string{"dateTime": "2026-08-17T09:00:00Z"},
				"end":   map[string]string{"dateTime": "2026-08-17T10:00:00Z"},
			}}})
			return
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/events") {
			created = true
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "evt-2", "iCalUID": "uid-2", "summary": "New",
				"start": map[string]string{"dateTime": "2026-08-18T09:00:00Z"},
				"end":   map[string]string{"dateTime": "2026-08-18T10:00:00Z"},
			})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origCal, origToken := CalendarAPIBase, TokenURL
	CalendarAPIBase, TokenURL = server.URL+"/calendar/v3", server.URL+"/token"
	defer func() { CalendarAPIBase, TokenURL = origCal, origToken }()
	connection := model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh-secret"}, Calendar: &model.CalendarConfig{Kind: "gmail"}}
	events, err := ListEvents(context.Background(), connection, time.Time{}, time.Time{}, "")
	if err != nil || len(events) != 1 || events[0].ID != "uid-1" || events[0].Href != "evt-1" {
		t.Fatalf("ListEvents = %#v, %v", events, err)
	}
	written, err := PutEvent(context.Background(), connection, model.Event{Title: "New", Start: events[0].Start, End: events[0].End}, true)
	if err != nil || !created || written.ID != "uid-2" {
		t.Fatalf("PutEvent = %#v, %v created=%v", written, err, created)
	}
}

func TestPutEventOmitsGeneratedIDAndKeepsAttendeesAllDay(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/events") {
			_ = json.NewDecoder(request.Body).Decode(&body)
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "generated", "iCalUID": "posthouse-abc", "summary": "Offsite",
				"start": map[string]string{"date": "2026-08-17"}, "end": map[string]string{"date": "2026-08-18"},
				"attendees": []map[string]string{{"email": "a@example.test"}},
			})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origCal, origToken := CalendarAPIBase, TokenURL
	CalendarAPIBase, TokenURL = server.URL+"/calendar/v3", server.URL+"/token"
	defer func() { CalendarAPIBase, TokenURL = origCal, origToken }()
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	written, err := PutEvent(context.Background(), model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh"}}, model.Event{
		ID: "posthouse-abc", Title: "Offsite", Start: start, End: start.Add(24 * time.Hour), AllDay: true,
		Attendees: []string{"a@example.test"}, RecurrenceRule: "FREQ=WEEKLY",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := body["id"]; ok {
		t.Fatalf("create payload included provider id: %#v", body)
	}
	if body["iCalUID"] != "posthouse-abc" {
		t.Fatalf("iCalUID = %#v", body["iCalUID"])
	}
	startPayload, _ := body["start"].(map[string]any)
	if startPayload["date"] != "2026-08-17" || startPayload["dateTime"] != nil {
		t.Fatalf("all-day start = %#v", body["start"])
	}
	if written.AllDay != true || len(written.Attendees) != 1 {
		t.Fatalf("written = %#v", written)
	}
}

func TestListEventsFollowsNextPageToken(t *testing.T) {
	pages := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		pages++
		if request.URL.Query().Get("pageToken") == "" {
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"nextPageToken": "page-2",
				"items":         []map[string]any{{"id": "evt-1", "iCalUID": "uid-1", "summary": "One", "start": map[string]string{"dateTime": "2026-08-17T09:00:00Z"}, "end": map[string]string{"dateTime": "2026-08-17T10:00:00Z"}}},
			})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"items": []map[string]any{{"id": "evt-2", "iCalUID": "uid-2", "summary": "Two", "start": map[string]string{"dateTime": "2026-08-17T11:00:00Z"}, "end": map[string]string{"dateTime": "2026-08-17T12:00:00Z"}}},
		})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origCal, origToken := CalendarAPIBase, TokenURL
	CalendarAPIBase, TokenURL = server.URL+"/calendar/v3", server.URL+"/token"
	defer func() { CalendarAPIBase, TokenURL = origCal, origToken }()
	events, err := ListEvents(context.Background(), model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh"}}, time.Time{}, time.Time{}, "")
	if err != nil || pages != 2 || len(events) != 2 || events[1].ID != "uid-2" {
		t.Fatalf("ListEvents = %#v pages=%d err=%v", events, pages, err)
	}
}

func TestUnreadCountUsesLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if !strings.HasSuffix(request.URL.Path, "/labels/INBOX") {
			http.NotFound(writer, request)
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"messagesUnread": 4})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	origBase, origToken := APIBase, TokenURL
	APIBase, TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { APIBase, TokenURL = origBase, origToken }()
	count, err := UnreadCount(context.Background(), model.Connection{ID: "gmail-work", Mail: &model.MailConfig{Kind: "gmail", ResolvedSecret: "refresh"}}, "")
	if err != nil || count != 4 {
		t.Fatalf("UnreadCount = %d, %v", count, err)
	}
}

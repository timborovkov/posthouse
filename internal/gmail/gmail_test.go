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

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
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "user-1"})
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
	if err != nil || len(result.Messages) != 1 || result.Messages[0].ID != "AAMkAG" || !result.Messages[0].Unread {
		t.Fatalf("Search = %#v, %v", result, err)
	}
	fetched, err := Get(context.Background(), connection, "AAMkAG")
	if err != nil || fetched.Detail.Subject != "Hello" {
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

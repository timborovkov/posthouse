package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timborovkov/posthouse/internal/gmail"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/microsoft"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/oauth"
	"github.com/zalando/go-keyring"
)

func TestMixedConnectionSearchFansInOnePage(t *testing.T) {
	t.Setenv("ICLOUD_PASSWORD", "app-password")
	t.Setenv("GMAIL_WORK_REFRESH", "refresh-work")
	t.Setenv("GMAIL_HOME_REFRESH", "refresh-home")
	t.Setenv("MS_REFRESH", "refresh-ms")
	application := serviceWithConnections(t,
		model.Connection{ID: "icloud", Name: "iCloud", Identity: model.Identity{Email: "me@icloud.com"}, Mail: &model.MailConfig{Username: "me@icloud.com", Secret: model.SecretRef{Env: "ICLOUD_PASSWORD"}, IMAP: model.IMAPConfig{Address: "localhost:1993", Insecure: true}}},
		model.Connection{ID: "gmail-home", Name: "Gmail home", Identity: model.Identity{Email: "me@gmail.com"}, Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_HOME_REFRESH"}}},
		model.Connection{ID: "gmail-work", Name: "Gmail work", Identity: model.Identity{Email: "me@acme.test"}, Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_WORK_REFRESH"}}},
		model.Connection{ID: "microsoft", Name: "Microsoft", Identity: model.Identity{Email: "me@contoso.test"}, Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Env: "MS_REFRESH"}}},
	)
	application.mailSearch = func(connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		if options.Query != "renewal" {
			t.Fatalf("backend saw provider-specific query language: %#v", options)
		}
		filter := func(messages []model.Message) []model.Message {
			if options.CursorTime.IsZero() && options.CursorID == "" && options.CursorUID == 0 {
				return messages
			}
			kept := make([]model.Message, 0, len(messages))
			boundary := model.Message{ConnectionID: connection.ID, ID: options.CursorID, ReceivedAt: options.CursorTime, UID: options.CursorUID}
			for _, message := range messages {
				if messageBefore(boundary, message) {
					kept = append(kept, message)
				}
			}
			return kept
		}
		switch connection.ID {
		case "icloud":
			return postmail.SearchResult{UIDValidity: 1, Messages: filter([]model.Message{{ConnectionID: "icloud", ID: postmail.EncodeIMAPID("INBOX", 1, 7), Folder: "INBOX", UID: 7, Subject: "iCloud renewal", ReceivedAt: instant(12)}})}, nil
		case "gmail-home":
			return postmail.SearchResult{UIDValidity: 1, Messages: filter([]model.Message{{ConnectionID: "gmail-home", ID: "home-1", Subject: "home renewal", ReceivedAt: instant(11)}})}, nil
		case "gmail-work":
			return postmail.SearchResult{UIDValidity: 1, HasMore: true, Messages: filter([]model.Message{
				{ConnectionID: "gmail-work", ID: "work-1", Subject: "work renewal", ReceivedAt: instant(14)},
				{ConnectionID: "gmail-work", ID: "work-2", Subject: "older work", ReceivedAt: instant(9)},
			})}, nil
		case "microsoft":
			return postmail.SearchResult{}, fmt.Errorf("microsoft graph timeout")
		default:
			t.Fatalf("unexpected connection %s", connection.ID)
			return postmail.SearchResult{}, nil
		}
	}
	page, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{Query: "renewal"}, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].ID != "work-1" || page.Messages[1].UID != 7 || page.NextCursor == "" {
		t.Fatalf("first page = %#v", page)
	}
	foundMicrosoft := false
	for _, item := range page.Errors {
		if item.ConnectionID == "microsoft" && item.Code == "mail_unavailable" {
			foundMicrosoft = true
		}
	}
	if !foundMicrosoft {
		t.Fatalf("expected microsoft source error, got %#v", page.Errors)
	}
	if strings.Contains(page.NextCursor, "https://") || strings.Contains(page.NextCursor, "pageToken") || strings.Contains(page.NextCursor, "nextLink") {
		t.Fatalf("public cursor leaked provider paging: %s", page.NextCursor)
	}
	second, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{Query: "renewal"}, 2, page.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Messages) != 2 || second.Messages[0].ID != "home-1" || second.Messages[1].ID != "work-2" {
		t.Fatalf("second page = %#v", second)
	}
	for _, message := range second.Messages {
		if message.ID == "work-1" || message.UID == 7 {
			t.Fatalf("cursor replayed first page: %#v", second.Messages)
		}
	}
}

func TestAuthorizeConnectionStoresKeychainRefWithoutToken(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Mail: &model.MailConfig{Kind: "gmail"}})
	application.authorizeOAuth = func(ctx context.Context, cfg oauth.Config, device bool) (string, error) {
		if !device || cfg.Credentials.ClientID == "" {
			t.Fatalf("authorize cfg=%#v device=%v", cfg, device)
		}
		return "refresh-secret-value", nil
	}
	result, err := application.AuthorizeConnection(context.Background(), "gmail-work", true)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "refresh-secret-value") {
		t.Fatalf("auth result leaked refresh token: %s", encoded)
	}
	cfg, err := application.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Connections[0].Mail.Secret.Keychain != "posthouse-gmail-work" || cfg.Connections[0].Mail.Secret.Env != "" {
		t.Fatalf("connection secret = %#v", cfg.Connections[0].Mail.Secret)
	}
	if data, _ := os.ReadFile(application.store.Path()); strings.Contains(string(data), "refresh-secret-value") {
		t.Fatal("config.json persisted refresh token")
	}
}

func TestDoctorNativeDoesNotPrintTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.secret-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"emailAddress": "you@gmail.com"})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret-value")
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}}})
	result, err := application.DoctorConnection(context.Background(), "gmail-work")
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "refresh-secret-value") || strings.Contains(string(encoded), "ya29.secret-access") {
		t.Fatalf("doctor leaked token material: %s", encoded)
	}
	if !result.OK {
		t.Fatalf("doctor failed: %#v", result)
	}
}

func TestGmailPrepareSendAndLabelExecuteAgainstFixture(t *testing.T) {
	raw := "From: me@acme.test\r\nTo: teammate@example.test\r\nSubject: Status\r\n\r\nHello"
	var sent, trashed bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/messages/send"):
			sent = true
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), "raw") {
				http.Error(writer, "missing raw", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "sent-1"})
		case strings.HasSuffix(request.URL.Path, "/trash"):
			trashed = true
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret")
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}},
	})
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "gmail-work", To: []string{"teammate@example.test"}, Subject: "Status", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), prepared.Token); err != nil || !sent {
		t.Fatalf("execute send = %v sent=%v", err, sent)
	}
	action, err := application.PrepareMailAction(context.Background(), "gmail-work", "mail.trash", MailAction{ID: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), action.Token); err != nil || !trashed {
		t.Fatalf("execute trash = %v trashed=%v", err, trashed)
	}
	_ = raw
}

func TestMicrosoftPrepareSendAgainstFixture(t *testing.T) {
	var sent bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "graph-access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if strings.HasSuffix(request.URL.Path, "/sendMail") {
			sent = true
			writer.WriteHeader(http.StatusAccepted)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	t.Setenv("MS_REFRESH", "refresh-ms")
	origBase, origToken := microsoft.APIBase, microsoft.TokenURL
	microsoft.APIBase, microsoft.TokenURL = server.URL, server.URL+"/token"
	defer func() { microsoft.APIBase, microsoft.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "microsoft", Name: "Microsoft", Identity: model.Identity{Email: "me@contoso.test"},
		Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Env: "MS_REFRESH"}},
	})
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "microsoft", To: []string{"teammate@example.test"}, Subject: "Status", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), prepared.Token); err != nil || !sent {
		t.Fatalf("execute send = %v sent=%v", err, sent)
	}
}

func TestNativeCalendarPrepareExecuteAgainstFixture(t *testing.T) {
	var created, deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/events") {
			created = true
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"id": "evt-1", "iCalUID": "uid-1", "summary": "Planning",
				"start": map[string]string{"dateTime": "2026-08-17T09:00:00Z"},
				"end":   map[string]string{"dateTime": "2026-08-17T10:00:00Z"},
			})
			return
		}
		if request.Method == http.MethodDelete {
			deleted = true
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret")
	origCal, origToken := gmail.CalendarAPIBase, gmail.TokenURL
	gmail.CalendarAPIBase, gmail.TokenURL = server.URL+"/calendar/v3", server.URL+"/token"
	defer func() { gmail.CalendarAPIBase, gmail.TokenURL = origCal, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Mail:     &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}},
		Calendar: &model.CalendarConfig{Kind: "gmail"},
	})
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	prepared, err := application.PrepareCalendarWrite(context.Background(), "gmail-work", "calendar.create", model.Event{Title: "Planning", Start: start, End: start.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), prepared.Token); err != nil || !created {
		t.Fatalf("execute create = %v created=%v", err, created)
	}
	deletion, err := application.PrepareCalendarDelete(context.Background(), "gmail-work", "", "evt-1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), deletion.Token); err != nil || !deleted {
		t.Fatalf("execute delete = %v deleted=%v", err, deleted)
	}
}

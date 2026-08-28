package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/timborovkov/posthouse/internal/config"
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
	application.nativeIdentity = func(context.Context, model.Connection) (string, error) {
		return "", nil
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
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "historyId": "1001"})
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
	deletion, err := application.PrepareCalendarDelete(context.Background(), "gmail-work", "", "evt-1", `"etag-1"`, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), deletion.Token); err != nil || !deleted {
		t.Fatalf("execute delete = %v deleted=%v", err, deleted)
	}
}

func TestNativeSearchCursorUsesOpaqueID(t *testing.T) {
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_WORK_REFRESH"}}})
	t.Setenv("GMAIL_WORK_REFRESH", "refresh")
	stamp := instant(10)
	application.mailSearch = func(connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		messages := []model.Message{
			{ConnectionID: "gmail-work", ID: "a", Subject: "A", ReceivedAt: stamp},
			{ConnectionID: "gmail-work", ID: "b", Subject: "B", ReceivedAt: stamp},
		}
		if options.CursorID == "" {
			return postmail.SearchResult{UIDValidity: 1, HasMore: true, Messages: messages}, nil
		}
		kept := make([]model.Message, 0)
		for _, message := range messages {
			if message.ID != options.CursorID && (options.CursorTime.IsZero() || !message.ReceivedAt.After(options.CursorTime)) {
				if messageBefore(model.Message{ID: options.CursorID, ReceivedAt: options.CursorTime, ConnectionID: connection.ID}, message) {
					kept = append(kept, message)
				}
			}
		}
		return postmail.SearchResult{UIDValidity: 1, Messages: kept}, nil
	}
	page, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, "")
	if err != nil || len(page.Messages) != 1 || page.NextCursor == "" {
		t.Fatalf("first page = %#v err=%v", page, err)
	}
	second, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, page.NextCursor)
	if err != nil || len(second.Messages) != 1 || second.Messages[0].ID == page.Messages[0].ID {
		t.Fatalf("second page replayed %q: %#v err=%v", page.Messages[0].ID, second, err)
	}
}

func TestAuthorizeConnectionRejectsMismatchedIdentity(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "you@gmail.com"}, Mail: &model.MailConfig{Kind: "gmail"}})
	application.authorizeOAuth = func(context.Context, oauth.Config, bool) (string, error) { return "refresh-secret-value", nil }
	application.nativeIdentity = func(context.Context, model.Connection) (string, error) { return "other@gmail.com", nil }
	if _, err := application.AuthorizeConnection(context.Background(), "gmail-work", true); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("AuthorizeConnection error = %v", err)
	}
}

func TestRemoveNativeConnectionDeletesOwnedToken(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_SECRETS_DIR", t.TempDir())
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Mail: &model.MailConfig{Kind: "gmail"}})
	if err := config.SetKeychainSecret("posthouse-gmail-work", "refresh-secret-value"); err != nil {
		t.Fatal(err)
	}
	if err := application.RemoveConnection("gmail-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolveSecret(model.SecretRef{Keychain: "posthouse-gmail-work"}); err == nil {
		t.Fatal("owned OAuth token remained after RemoveConnection")
	}
}

func TestNativeSendTransportLossIsUncertain(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "0000000000000000000000000000000000000000000000000000000000000000")
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			http.Error(writer, "no hijack", http.StatusInternalServerError)
			return
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}},
	})
	application.mailBuild = func(model.Connection, model.SendMessage) ([]byte, error) { return []byte("raw"), nil }
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "gmail-work", To: []string{"a@example.test"}, Subject: "Hi", Text: "body"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.ExecuteOperation(context.Background(), prepared.Token)
	if err == nil || result.Status != "uncertain" {
		t.Fatalf("result = %#v err=%v", result, err)
	}
}

func TestNativeUnreadCountsUseBackend(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"messagesUnread": 7})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh")
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}}})
	summaries, err := application.UnreadCounts(context.Background(), model.Selector{}, "")
	if err != nil || len(summaries) != 1 || summaries[0].Unread != 7 || summaries[0].Error != "" {
		t.Fatalf("UnreadCounts = %#v err=%v", summaries, err)
	}
}

func TestRemoveNativeConnectionKeepsSharedToken(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_SECRETS_DIR", t.TempDir())
	application := serviceWithConnections(t,
		model.Connection{ID: "ms-work", Name: "Work", Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Keychain: "shared-ms"}}},
		model.Connection{ID: "ms-home", Name: "Home", Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Keychain: "shared-ms"}}},
	)
	if err := config.SetKeychainSecret("shared-ms", "refresh-shared"); err != nil {
		t.Fatal(err)
	}
	if err := application.RemoveConnection("ms-work"); err != nil {
		t.Fatal(err)
	}
	if _, err := config.ResolveSecret(model.SecretRef{Keychain: "shared-ms"}); err != nil {
		t.Fatal("shared OAuth token was deleted while another connection still referenced it")
	}
}

func TestNativeMailActionRejectsStaleSnapshot(t *testing.T) {
	var history string
	var trashed bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "historyId": history})
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
	history = "1001"
	action, err := application.PrepareMailAction(context.Background(), "gmail-work", "mail.trash", MailAction{ID: "msg-1"})
	if err != nil {
		t.Fatal(err)
	}
	history = "2002"
	if _, err := application.ExecuteOperation(context.Background(), action.Token); err == nil || trashed {
		t.Fatalf("stale native mail action executed: err=%v trashed=%v", err, trashed)
	}
}

func TestNativeAttachmentSnapshotReturnsCursor(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", "0000000000000000000000000000000000000000000000000000000000000000")
	raw := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=part\r\n\r\n--part\r\nContent-Type: text/plain\r\n\r\nmessage body\r\n--part\r\nContent-Type: text/plain; name=notes.txt\r\nContent-Disposition: attachment; filename=notes.txt\r\n\r\nattached notes\r\n--part--\r\n")
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/msg-1") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "raw": encoded, "labelIds": []string{"INBOX"}, "internalDate": "1152194645000"})
			return
		}
		http.NotFound(writer, request)
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
	ctx := context.Background()
	detail, err := application.GetMessageContext(ctx, "gmail-work", MessageLocator{ID: "msg-1"})
	if err != nil || len(detail.Attachments) != 1 {
		t.Fatalf("GetMessage = %#v, %v", detail, err)
	}
	attachmentID := detail.Attachments[0].ID
	_, data, cursor, err := application.GetAttachmentSnapshotMode(ctx, "gmail-work", MessageLocator{ID: "msg-1"}, attachmentID, "", "")
	if err != nil || cursor == "" || string(data) != "attached notes" {
		t.Fatalf("native attachment snapshot data=%q cursor=%q err=%v", data, cursor, err)
	}
	_, continued, continuedCursor, err := application.GetAttachmentSnapshotMode(ctx, "gmail-work", MessageLocator{ID: "msg-1"}, attachmentID, "", cursor)
	if err != nil || continuedCursor != cursor || string(continued) != "attached notes" {
		t.Fatalf("native attachment continuation data=%q cursor=%q err=%v", continued, continuedCursor, err)
	}
}

func TestAuthorizeCalendarOnlyGmailUsesUserInfo(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_SECRETS_DIR", t.TempDir())
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	var profileHits int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		if request.URL.Path == "/userinfo" {
			_ = json.NewEncoder(writer).Encode(map[string]any{"email": "you@gmail.com"})
			return
		}
		if strings.Contains(request.URL.Path, "/users/me/profile") {
			profileHits++
			http.Error(writer, "gmail profile requires mail scope", http.StatusForbidden)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	origBase, origToken, origUser := gmail.APIBase, gmail.TokenURL, gmail.UserInfoURL
	gmail.APIBase, gmail.TokenURL, gmail.UserInfoURL = server.URL+"/gmail/v1", server.URL+"/token", server.URL+"/userinfo"
	defer func() { gmail.APIBase, gmail.TokenURL, gmail.UserInfoURL = origBase, origToken, origUser }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-cal", Name: "Gmail calendar", Identity: model.Identity{Email: "you@gmail.com"},
		Calendar: &model.CalendarConfig{Kind: "gmail"},
	})
	application.authorizeOAuth = func(context.Context, oauth.Config, bool) (string, error) { return "refresh-secret-value", nil }
	if _, err := application.AuthorizeConnection(context.Background(), "gmail-cal", false); err != nil || profileHits != 0 {
		t.Fatalf("AuthorizeConnection calendar-only = %v profileHits=%d", err, profileHits)
	}
}

func TestMicrosoftClientPersistsRotatedRefreshToken(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_SECRETS_DIR", t.TempDir())
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	if err := config.SetKeychainSecret("posthouse-ms", "refresh-old"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{
				"access_token": "graph-access", "refresh_token": "refresh-rotated", "token_type": "Bearer", "expires_in": 3600,
			})
			return
		}
		if request.URL.Path == "/me" || strings.HasPrefix(request.URL.Path, "/me") {
			_ = json.NewEncoder(writer).Encode(map[string]any{"mail": "me@contoso.test", "userPrincipalName": "me@contoso.test"})
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	origBase, origToken := microsoft.APIBase, microsoft.TokenURL
	microsoft.APIBase, microsoft.TokenURL = server.URL, server.URL+"/token"
	defer func() { microsoft.APIBase, microsoft.TokenURL = origBase, origToken }()
	connection := model.Connection{
		ID: "ms", Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Keychain: "posthouse-ms"}, ResolvedSecret: "refresh-old"},
	}
	if err := microsoft.Ping(context.Background(), connection); err != nil {
		t.Fatal(err)
	}
	got, err := config.ResolveSecret(model.SecretRef{Keychain: "posthouse-ms"})
	if err != nil || got != "refresh-rotated" {
		t.Fatal("rotated Microsoft refresh token was not persisted")
	}
}

func TestMicrosoftPrepareSendRejectsOversizedMIME(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	t.Setenv("MS_REFRESH", "refresh-ms")
	application := serviceWithConnections(t, model.Connection{
		ID: "microsoft", Name: "Microsoft", Identity: model.Identity{Email: "me@contoso.test"},
		Mail: &model.MailConfig{Kind: "microsoft", Secret: model.SecretRef{Env: "MS_REFRESH"}},
	})
	application.mailBuild = func(model.Connection, model.SendMessage) ([]byte, error) {
		return make([]byte, 3<<20+1), nil
	}
	_, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "microsoft", To: []string{"teammate@example.test"}, Subject: "Status", Text: "Hello"})
	if err == nil || !strings.Contains(err.Error(), "4 MiB") {
		t.Fatalf("PrepareSend oversized = %v", err)
	}
}

func TestMicrosoftPrepareCalendarWriteRejectsCancelledStatus(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_MICROSOFT_CLIENT_ID", "public-client")
	t.Setenv("MS_REFRESH", "refresh-ms")
	application := serviceWithConnections(t, model.Connection{
		ID: "microsoft", Name: "Microsoft", Identity: model.Identity{Email: "me@contoso.test"},
		Calendar: &model.CalendarConfig{Kind: "microsoft", Secret: model.SecretRef{Env: "MS_REFRESH"}},
	})
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	_, err := application.PrepareCalendarWrite(context.Background(), "microsoft", "calendar.create", model.Event{Title: "Planning", Start: start, End: start.Add(time.Hour), Status: "CANCELLED"})
	if err == nil || !strings.Contains(err.Error(), "do not serialize event status") {
		t.Fatalf("PrepareCalendarWrite cancelled = %v", err)
	}
}

func TestGmailMoveRemovesSourceLabelAndUntrashes(t *testing.T) {
	var modifyBody map[string]any
	var untrashed bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "historyId": "1001"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/untrash"):
			untrashed = true
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1"})
		case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/modify"):
			_ = json.NewDecoder(request.Body).Decode(&modifyBody)
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1"})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret")
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}},
	})
	action, err := application.PrepareMailAction(context.Background(), "gmail-work", "mail.move", MailAction{ID: "msg-1", Folder: "Projects", Destination: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), action.Token); err != nil {
		t.Fatal(err)
	}
	add, _ := modifyBody["addLabelIds"].([]any)
	remove, _ := modifyBody["removeLabelIds"].([]any)
	if len(add) != 1 || add[0] != "INBOX" || len(remove) != 1 || remove[0] != "Projects" {
		t.Fatalf("custom-label move modify = %#v", modifyBody)
	}
	restore, err := application.PrepareMailAction(context.Background(), "gmail-work", "mail.move", MailAction{ID: "msg-1", Folder: "TRASH", Destination: "INBOX"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), restore.Token); err != nil || !untrashed {
		t.Fatalf("execute untrash = %v untrashed=%v", err, untrashed)
	}
}

func TestGmailDraftUpdateAndDeleteUseDraftResources(t *testing.T) {
	var putPath, deletePath string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		switch {
		case request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/messages/"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "msg-1", "historyId": "1001"})
		case request.Method == http.MethodGet && request.URL.Path == "/gmail/v1/users/me/drafts/draft-1":
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "draft-1", "message": map[string]any{"id": "msg-1", "historyId": "1001"}})
		case request.Method == http.MethodPut && strings.Contains(request.URL.Path, "/drafts/"):
			putPath = request.URL.Path
			_ = json.NewEncoder(writer).Encode(map[string]any{"id": "draft-1"})
		case request.Method == http.MethodDelete && strings.Contains(request.URL.Path, "/drafts/"):
			deletePath = request.URL.Path
			writer.WriteHeader(http.StatusNoContent)
		case strings.Contains(request.URL.Path, "/trash"):
			http.Error(writer, "draft delete must not trash a message", http.StatusTeapot)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret")
	origBase, origToken := gmail.APIBase, gmail.TokenURL
	gmail.APIBase, gmail.TokenURL = server.URL+"/gmail/v1", server.URL+"/token"
	defer func() { gmail.APIBase, gmail.TokenURL = origBase, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Mail: &model.MailConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}, Folders: model.FolderConfig{Drafts: "DRAFTS"}},
	})
	update, err := application.PrepareDraft(context.Background(), "gmail-work", "mail.draft.update", MessageLocator{ID: "draft-1", Folder: "DRAFTS"}, model.SendMessage{To: []string{"teammate@example.test"}, Subject: "Updated", Text: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), update.Token); err != nil || !strings.HasSuffix(putPath, "/drafts/draft-1") {
		t.Fatalf("execute draft update path=%s err=%v", putPath, err)
	}
	deletion, err := application.PrepareDraft(context.Background(), "gmail-work", "mail.draft.delete", MessageLocator{ID: "draft-1", Folder: "DRAFTS"}, model.SendMessage{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), deletion.Token); err != nil || !strings.HasSuffix(deletePath, "/drafts/draft-1") {
		t.Fatalf("execute draft delete path=%s err=%v", deletePath, err)
	}
}

func TestAuthorizeConnectionRejectsIdentityChangeDuringConsent(t *testing.T) {
	keyring.MockInit()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	application := serviceWithConnections(t, model.Connection{ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "a@gmail.com"}, Mail: &model.MailConfig{Kind: "gmail"}})
	application.authorizeOAuth = func(context.Context, oauth.Config, bool) (string, error) {
		if err := application.UpsertConnection(model.Connection{ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "b@gmail.com"}, Mail: &model.MailConfig{Kind: "gmail"}}, true); err != nil {
			return "", err
		}
		return "refresh-secret-value", nil
	}
	application.nativeIdentity = func(context.Context, model.Connection) (string, error) { return "a@gmail.com", nil }
	if _, err := application.AuthorizeConnection(context.Background(), "gmail-work", true); err == nil || !strings.Contains(err.Error(), "changed during authorization") {
		t.Fatalf("AuthorizeConnection during edit = %v", err)
	}
}

func TestGmailPrepareCalendarWriteRejectsRecurrencePeriods(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("GMAIL_REFRESH", "refresh-secret")
	application := serviceWithConnections(t, model.Connection{
		ID: "gmail-work", Name: "Gmail", Identity: model.Identity{Email: "me@acme.test"},
		Calendar: &model.CalendarConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_REFRESH"}},
	})
	start := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	_, err := application.PrepareCalendarWrite(context.Background(), "gmail-work", "calendar.create", model.Event{
		Title: "Range", Start: start, End: start.Add(time.Hour), RecurrencePeriods: []model.RecurrencePeriod{{Start: start, End: start.Add(24 * time.Hour)}},
	})
	if err == nil || !strings.Contains(err.Error(), "period-form recurrence") {
		t.Fatalf("PrepareCalendarWrite periods = %v", err)
	}
}

func TestDoctorMixedIMAPCalendarUsesCalendarOAuthSecret(t *testing.T) {
	var refresh string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			_ = request.ParseForm()
			refresh = request.Form.Get("refresh_token")
			if refresh == "imap-password" {
				http.Error(writer, "imap password is not an oauth token", http.StatusBadRequest)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]any{"access_token": "ya29.access", "token_type": "Bearer", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"items": []map[string]any{}})
	}))
	defer server.Close()
	t.Setenv("POSTHOUSE_GOOGLE_CLIENT_ID", "desktop.apps.googleusercontent.com")
	t.Setenv("IMAP_PASSWORD", "imap-password")
	t.Setenv("GMAIL_CAL_REFRESH", "refresh-secret")
	origCal, origToken := gmail.CalendarAPIBase, gmail.TokenURL
	gmail.CalendarAPIBase, gmail.TokenURL = server.URL+"/calendar/v3", server.URL+"/token"
	defer func() { gmail.CalendarAPIBase, gmail.TokenURL = origCal, origToken }()
	application := serviceWithConnections(t, model.Connection{
		ID: "mixed", Name: "Mixed", Identity: model.Identity{Email: "me@example.test"},
		Mail:     &model.MailConfig{Username: "me@example.test", Secret: model.SecretRef{Env: "IMAP_PASSWORD"}, IMAP: model.IMAPConfig{Address: "localhost:1993", Insecure: true}},
		Calendar: &model.CalendarConfig{Kind: "gmail", Secret: model.SecretRef{Env: "GMAIL_CAL_REFRESH"}},
	})
	result, err := application.DoctorConnection(context.Background(), "mixed")
	if err != nil {
		t.Fatal(err)
	}
	if refresh != "refresh-secret" {
		t.Fatalf("doctor used refresh %q", refresh)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "imap-password") || strings.Contains(string(encoded), "refresh-secret") {
		t.Fatalf("doctor leaked secret: %s", encoded)
	}
}

package mcpserver

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

const testAccessKey = "test-access-key-1"

var errExecute = errors.New("provider execute")

func TestRESTRequiresAuthAndServesCatalog(t *testing.T) {
	handler := testHTTPHandler(t)
	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/v1", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated /v1 status = %d", unauthorized.Code)
	}

	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", health.Code)
	}

	catalog := httptest.NewRecorder()
	handler.ServeHTTP(catalog, authorizedRequest(http.MethodGet, "/v1", ""))
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status = %d body=%s", catalog.Code, catalog.Body.String())
	}
	if !strings.Contains(catalog.Body.String(), `/v1/mail/send`) || !strings.Contains(catalog.Body.String(), `"/mcp"`) {
		t.Fatalf("catalog body = %s", catalog.Body.String())
	}
}

func TestRESTListsConnectionsAndGeneratesICS(t *testing.T) {
	handler := testHTTPHandler(t)
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, authorizedRequest(http.MethodGet, "/v1/connections?page_size=1", ""))
	if listed.Code != http.StatusOK {
		t.Fatalf("connections status = %d body=%s", listed.Code, listed.Body.String())
	}
	var page model.ConnectionPage
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Connections) != 1 || page.NextCursor == "" {
		t.Fatalf("connection page = %#v", page)
	}

	ics := httptest.NewRecorder()
	handler.ServeHTTP(ics, authorizedRequest(http.MethodPost, "/v1/calendar/ics", `{"title":"Planning","start":"2026-08-17T09:00:00Z","end":"2026-08-17T10:00:00Z"}`))
	if ics.Code != http.StatusOK || !strings.Contains(ics.Body.String(), "BEGIN:VCALENDAR") {
		t.Fatalf("ics status=%d body=%s", ics.Code, ics.Body.String())
	}
}

func TestRESTRejectsUnknownMailMode(t *testing.T) {
	handler := testHTTPHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, "/v1/mail/search", `{"mode":"explode"}`))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "offline or refresh") {
		t.Fatalf("invalid mode status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRESTSyncAcceptsEmptyBody(t *testing.T) {
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(service.New(store), "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.HTTPHandler(testAccessKey, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	empty := httptest.NewRecorder()
	handler.ServeHTTP(empty, authorizedRequest(http.MethodPost, "/v1/sync", ""))
	if empty.Code != http.StatusOK {
		t.Fatalf("empty-body sync status=%d body=%s", empty.Code, empty.Body.String())
	}

	unknownLength := httptest.NewRecorder()
	request := authorizedRequest(http.MethodPost, "/v1/sync", "")
	request.Body = io.NopCloser(strings.NewReader(""))
	request.ContentLength = -1
	handler.ServeHTTP(unknownLength, request)
	if unknownLength.Code != http.StatusOK {
		t.Fatalf("unknown-length sync status=%d body=%s", unknownLength.Code, unknownLength.Body.String())
	}
}

func TestRESTConnectionsRejectsInvalidPageSize(t *testing.T) {
	handler := testHTTPHandler(t)
	for _, raw := range []string{"nope", "-1", "1abc"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, authorizedRequest(http.MethodGet, "/v1/connections?page_size="+raw, ""))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("page_size %q status=%d body=%s", raw, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRESTExecuteUnknownTokenIsBadRequest(t *testing.T) {
	handler := testHTTPHandler(t)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authorizedRequest(http.MethodPost, "/v1/operations/execute", `{"token":"missing"}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown token status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRESTExecuteStatusKeepsProviderFailuresAsOK(t *testing.T) {
	if restExecuteStatus(model.OperationResult{Status: "succeeded"}, nil) != http.StatusOK {
		t.Fatal("succeeded execute should be 200")
	}
	if restExecuteStatus(model.OperationResult{Status: "failed"}, errExecute) != http.StatusOK {
		t.Fatal("failed execute should be 200 with the result body")
	}
	if restExecuteStatus(model.OperationResult{Status: "uncertain"}, errExecute) != http.StatusOK {
		t.Fatal("uncertain execute should be 200 with the result body")
	}
	if restExecuteStatus(model.OperationResult{}, errExecute) != http.StatusBadRequest {
		t.Fatal("missing/expired token should be 400")
	}
}

func testHTTPHandler(t *testing.T) http.Handler {
	t.Helper()
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	application := service.New(store)
	if err := application.UpsertConnection(model.Connection{
		ID: "work", Name: "Work", Category: "work", Labels: []string{"primary"},
		Mail: &model.MailConfig{Username: "me@example.com", SecretEnv: "WORK_PASSWORD", IMAP: model.IMAPConfig{Address: "imap.example.com:993", TLS: true}},
	}, false); err != nil {
		t.Fatal(err)
	}
	if err := application.UpsertConnection(model.Connection{
		ID: "personal", Name: "Personal", Category: "personal",
		Mail: &model.MailConfig{Username: "personal@example.com", SecretEnv: "PERSONAL_PASSWORD", IMAP: model.IMAPConfig{Address: "imap.example.com:993", TLS: true}},
	}, false); err != nil {
		t.Fatal(err)
	}
	server, err := New(application, "")
	if err != nil {
		t.Fatal(err)
	}
	handler, err := server.HTTPHandler(testAccessKey, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func authorizedRequest(method, target, body string) *http.Request {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Authorization", "Bearer "+testAccessKey)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

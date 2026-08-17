package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

func TestServerListsAndCallsReadOnlyConnectionTool(t *testing.T) {
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.New returned error: %v", err)
	}
	application := service.New(store)
	if err := application.UpsertConnection(model.Connection{
		ID: "work", Name: "Work", Category: "work", Labels: []string{"primary"},
		Mail: &model.MailConfig{Username: "me@example.com", SecretEnv: "WORK_PASSWORD", IMAP: model.IMAPConfig{Address: "imap.example.com:993", TLS: true}},
	}, false); err != nil {
		t.Fatalf("UpsertConnection returned error: %v", err)
	}
	if err := application.UpsertConnection(model.Connection{
		ID: "personal", Name: "Personal", Category: "personal",
		Mail: &model.MailConfig{Username: "personal@example.com", SecretEnv: "PERSONAL_PASSWORD", IMAP: model.IMAPConfig{Address: "imap.example.com:993", TLS: true}},
	}, false); err != nil {
		t.Fatalf("second UpsertConnection returned error: %v", err)
	}
	server, err := New(application, "")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect returned error: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect returned error: %v", err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	names := make([]string, 0, len(listed.Tools))
	var executeTool, sendTool, draftTool, attachmentTool *mcp.Tool
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		if tool.Name == "operation_execute" {
			executeTool = tool
		}
		if tool.Name == "messages_send_prepare" {
			sendTool = tool
		}
		if tool.Name == "messages_draft_prepare" {
			draftTool = tool
		}
		if tool.Name == "messages_attachment_get" {
			attachmentTool = tool
		}
	}
	for _, want := range []string{"connections_list", "messages_search", "messages_triage", "messages_unread_counts", "messages_get", "messages_attachment_get", "messages_send_prepare", "messages_reply_prepare", "messages_forward_prepare", "messages_draft_prepare", "messages_action_prepare", "events_list", "event_ics_generate", "event_create_prepare", "operation_execute", "connection_doctor", "sync", "cache_status"} {
		if !slices.Contains(names, want) {
			t.Fatalf("tools %v do not contain %s", names, want)
		}
	}
	if slices.Contains(names, "messages_send") {
		t.Fatal("direct messages_send tool bypasses the prepared-operation safety boundary")
	}
	if executeTool == nil || executeTool.Annotations == nil || executeTool.Annotations.DestructiveHint == nil || !*executeTool.Annotations.DestructiveHint || !executeTool.Annotations.IdempotentHint {
		t.Fatalf("operation_execute annotations=%#v", executeTool)
	}
	for _, tool := range []*mcp.Tool{sendTool, draftTool} {
		schema, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(schema), `"path"`) {
			t.Fatalf("remote attachment schema exposes host filesystem paths: %s", schema)
		}
		if !strings.Contains(string(schema), `"html"`) {
			t.Fatalf("%s schema lacks html body field: %s", tool.Name, schema)
		}
	}
	attachmentSchema, _ := json.Marshal(attachmentTool.InputSchema)
	if !strings.Contains(string(attachmentSchema), `"cursor"`) {
		t.Fatalf("attachment schema lacks snapshot cursor: %s", attachmentSchema)
	}
	missingCursor, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "messages_attachment_get", Arguments: map[string]any{"connection": "work", "uid": 1, "attachment_id": "file", "offset": 1}})
	errorText := ""
	if missingCursor != nil && len(missingCursor.Content) > 0 {
		if content, ok := missingCursor.Content[0].(*mcp.TextContent); ok {
			errorText = content.Text
		}
	}
	if err != nil || missingCursor == nil || !missingCursor.IsError || !strings.Contains(errorText, "cursor is required") {
		t.Fatalf("attachment continuation without cursor returned %#v, %v", missingCursor, err)
	}

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "connections_list", Arguments: map[string]any{"page_size": 1}})
	if err != nil {
		t.Fatalf("CallTool returned error: %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool returned tool error: %#v", result.Content)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent has type %T", result.StructuredContent)
	}
	connections, ok := structured["connections"].([]any)
	if !ok || len(connections) != 1 {
		t.Fatalf("connections output is %#v", structured["connections"])
	}
	nextCursor, ok := structured["next_cursor"].(string)
	if !ok || nextCursor == "" {
		t.Fatalf("next_cursor is %#v", structured["next_cursor"])
	}
	secondPage, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "connections_list", Arguments: map[string]any{"page_size": 1, "cursor": nextCursor}})
	if err != nil || secondPage.IsError {
		t.Fatalf("second connections_list returned %#v, %v", secondPage, err)
	}

	icsResult, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_ics_generate", Arguments: map[string]any{
		"title": "Planning", "start": "2026-08-17T09:00:00Z", "end": "2026-08-17T10:00:00Z",
	}})
	if err != nil || icsResult.IsError {
		t.Fatalf("event_ics_generate returned %#v, %v", icsResult, err)
	}
	if len(icsResult.Content) != 1 {
		t.Fatalf("event_ics_generate returned %d content blocks", len(icsResult.Content))
	}
	resource, ok := icsResult.Content[0].(*mcp.EmbeddedResource)
	if !ok || resource.Resource.MIMEType != "text/calendar" || !strings.Contains(resource.Resource.Text, "BEGIN:VCALENDAR") {
		t.Fatalf("embedded resource is %#v", icsResult.Content[0])
	}
	cancelResult, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "event_ics_generate", Arguments: map[string]any{
		"id": "planning-uid", "title": "Planning", "start": "2026-08-17T09:00:00Z", "end": "2026-08-17T10:00:00Z", "method": "cancel", "sequence": 3,
	}})
	if err != nil || cancelResult.IsError {
		t.Fatalf("event_ics_generate cancel returned %#v, %v", cancelResult, err)
	}
	cancelResource, ok := cancelResult.Content[0].(*mcp.EmbeddedResource)
	if !ok || !strings.Contains(cancelResource.Resource.Text, "METHOD:CANCEL") || !strings.Contains(cancelResource.Resource.Text, "SEQUENCE:3") {
		t.Fatalf("cancel invitation is %#v", cancelResult.Content[0])
	}
}

func TestAuthenticate(t *testing.T) {
	handler := testHTTPHandler(t)

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status is %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1", nil)
	request.Header.Set("Authorization", "Bearer "+testAccessKey)
	handler.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status is %d body=%s", authorized.Code, authorized.Body.String())
	}
}

func TestRunHTTPRejectsNonLoopbackEvenWithToken(t *testing.T) {
	server := &Server{}
	err := server.RunHTTP(context.Background(), "0.0.0.0:8791", testAccessKey, false, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "must listen on loopback") {
		t.Fatalf("RunHTTP non-loopback error = %v", err)
	}
}

func TestRunHTTPContainerListenerStillRequiresToken(t *testing.T) {
	server := &Server{}
	err := server.RunHTTP(context.Background(), "0.0.0.0:8791", "", true, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("RunHTTP container-listener error = %v", err)
	}
}

func TestRunHTTPLoopbackStillRequiresToken(t *testing.T) {
	server := &Server{}
	err := server.RunHTTP(context.Background(), "127.0.0.1:8791", "", false, slog.Default())
	if err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("RunHTTP loopback error = %v", err)
	}
}

func TestHTTPBodyLimitAccommodatesDocumentedAttachment(t *testing.T) {
	encodedAttachment := base64.StdEncoding.EncodedLen(25 << 20)
	if maxMCPHTTPRequestBytes < int64(encodedAttachment+(1<<20)) {
		t.Fatalf("HTTP body limit %d cannot carry a base64 25 MiB attachment plus JSON envelope", maxMCPHTTPRequestBytes)
	}
}

func TestHTTPShutdownWaitsForInFlightHandler(t *testing.T) {
	shutdownStarted := make(chan struct{})
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- serveHTTPUntilShutdown(ctx, func() error {
			<-shutdownStarted
			return http.ErrServerClosed
		}, func(context.Context) error {
			close(shutdownStarted)
			<-release
			return nil
		})
	}()
	cancel()
	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP shutdown did not start")
	}
	select {
	case err := <-finished:
		t.Fatalf("server returned before in-flight handler completed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestReadonlyProfileOmitsWriteTools(t *testing.T) {
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.New returned error: %v", err)
	}
	application := service.New(store)
	server, err := New(application, "readonly")
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.mcp.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect returned error: %v", err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect returned error: %v", err)
	}
	defer clientSession.Close()

	listed, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools returned error: %v", err)
	}
	names := make([]string, 0, len(listed.Tools))
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
	}
	for _, want := range []string{"connections_list", "messages_search", "messages_triage", "messages_get", "events_list", "event_ics_generate", "cache_status", "operation_show"} {
		if !slices.Contains(names, want) {
			t.Fatalf("readonly tools %v do not contain %s", names, want)
		}
	}
	for _, blocked := range []string{"messages_send_prepare", "messages_action_prepare", "messages_draft_prepare", "event_create_prepare", "operation_execute"} {
		if slices.Contains(names, blocked) {
			t.Fatalf("readonly tools unexpectedly include %s", blocked)
		}
	}
	for _, want := range []string{"messages_triage", "messages_unread_counts"} {
		if !slices.Contains(names, want) {
			t.Fatalf("readonly tools %v do not contain %s", names, want)
		}
	}
}

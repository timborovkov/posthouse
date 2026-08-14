//go:build e2e

package e2e_test

// These tests exercise the built binary across process boundaries so CLI JSON,
// encrypted prepared writes, authentication, and HTTP readiness stay aligned.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
)

func TestBuiltBinaryMultiConnectionPreparedMailAndCalendar(t *testing.T) {
	binary := requiredEnv(t, "POSTHOUSE_TEST_BINARY")
	configPath := filepath.Join(t.TempDir(), "config.json")
	endpoint := requiredEnv(t, "POSTHOUSE_TEST_RADICALE")
	feedStart := time.Now().UTC().Add(90 * time.Minute).Truncate(time.Second)
	feedServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/calendar")
		_, _ = fmt.Fprintf(writer, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:e2e-feed\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:E2E feed calendar\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n", feedStart.Format("20060102T150405Z"), feedStart.Add(time.Hour).Format("20060102T150405Z"))
	}))
	defer feedServer.Close()
	ensureCalendar(t, endpoint, "/work/work-calendar/", "work", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	ensureCalendar(t, endpoint, "/personal/personal-calendar/", "personal", requiredEnv(t, "POSTHOUSE_TEST_PERSONAL_PASSWORD"))
	for _, mailbox := range []string{"Sent", "Drafts", "Archive", "Trash", "Target"} {
		ensureMailbox(t, "work@work.test", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"), mailbox)
		ensureMailbox(t, "personal@personal.test", requiredEnv(t, "POSTHOUSE_TEST_PERSONAL_PASSWORD"), mailbox)
	}
	store, err := config.New(configPath)
	if err != nil {
		t.Fatal(err)
	}
	connections := []model.Connection{
		testConnection("work", "work@work.test", "POSTHOUSE_TEST_WORK_PASSWORD", endpoint),
		testConnection("personal", "personal@personal.test", "POSTHOUSE_TEST_PERSONAL_PASSWORD", endpoint),
		{ID: "feed", Name: "Feed", Category: "shared", Calendar: &model.CalendarConfig{Kind: "feed", URL: feedServer.URL}},
	}
	connections[0].Mail.SentCopy = "always"
	if err := store.Save(model.Config{Connections: connections}); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "POSTHOUSE_CONFIG="+configPath)
	mcpCommand := exec.Command(binary, "--config", configPath, "mcp", "stdio")
	mcpCommand.Env = env
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "posthouse-e2e", Version: "0.2.0"}, nil)
	mcpSession, err := mcpClient.Connect(context.Background(), &mcp.CommandTransport{Command: mcpCommand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stdioResult, err := mcpSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "connections_list", Arguments: map[string]any{"page_size": 10}})
	if err != nil || stdioResult.IsError {
		t.Fatalf("MCP stdio connections_list: %#v, %v", stdioResult, err)
	}
	stdioPrepared := callToolMap(t, mcpSession, "messages_send_prepare", map[string]any{"connection": "work", "to": []string{"personal@personal.test"}, "subject": "MCP stdio prepared", "text": "stdio body"})
	stdioExecuted := callToolMap(t, mcpSession, "operation_execute", map[string]any{"token": stdioPrepared["token"]})
	if stdioExecuted["status"] != "succeeded" {
		t.Fatalf("MCP stdio execution: %#v", stdioExecuted)
	}
	_ = mcpSession.Close()
	waitMessage(t, env, binary, "personal", "INBOX", "MCP stdio prepared")
	listed := runJSON(t, env, binary, "connection", "list")
	if len(listed["connections"].([]any)) != 3 {
		t.Fatalf("connections: %#v", listed)
	}
	attachmentPath := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(attachmentPath, []byte("attachment body"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared := runJSON(t, env, binary, "mail", "send", "--connection", "work", "--to", "personal@personal.test", "--subject", "E2E prepared", "--body", "hello", "--attachment", attachmentPath)
	token := prepared["token"].(string)
	for index, result := range runJSONConcurrently(t, env, binary, []string{"operation", "execute", token}, []string{"operation", "execute", token}) {
		if result["status"] != "succeeded" {
			t.Fatalf("concurrent execute %d: %#v", index, result)
		}
	}
	// Idempotent replay returns the original successful result.
	replayed := runJSON(t, env, binary, "operation", "execute", token)
	if replayed["status"] != "succeeded" {
		t.Fatalf("replay: %#v", replayed)
	}
	received := waitMessage(t, env, binary, "personal", "INBOX", "E2E prepared")
	page := runJSON(t, env, binary, "mail", "search", "--connection", "personal", "--folder", "INBOX", "--query", "E2E prepared")
	if len(page["messages"].([]any)) != 1 {
		t.Fatalf("concurrent operation delivered %d copies", len(page["messages"].([]any)))
	}
	uid := uint32(received["uid"].(float64))
	detail := runJSON(t, env, binary, "mail", "get", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid))
	if detail["text"] != "hello" || len(detail["attachments"].([]any)) != 1 {
		t.Fatalf("message detail: %#v", detail)
	}
	attachment := detail["attachments"].([]any)[0].(map[string]any)
	download := filepath.Join(t.TempDir(), "download.txt")
	runJSON(t, env, binary, "mail", "attachment", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid), "--id", attachment["id"].(string), "--output", download)
	if data, err := os.ReadFile(download); err != nil || string(data) != "attachment body" {
		t.Fatalf("downloaded attachment = %q, %v", data, err)
	}
	offline := runJSON(t, env, binary, "mail", "get", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid), "--offline")
	if offline["stale"] != true {
		t.Fatalf("offline body did not report stale cache: %#v", offline)
	}

	for _, command := range [][]string{
		{"mail", "reply", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid), "--body", "reply body"},
		{"mail", "forward", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid), "--to", "work@work.test", "--body", "forward body"},
		{"mail", "mark", "--connection", "personal", "--folder", "INBOX", "--uid", fmt.Sprint(uid), "--read", "--flagged"},
	} {
		operation := runJSON(t, env, binary, command...)
		if executed := runJSON(t, env, binary, "operation", "execute", operation["token"].(string)); executed["status"] != "succeeded" {
			t.Fatalf("%v execution: %#v", command, executed)
		}
	}

	draftPath := filepath.Join(t.TempDir(), "draft.json")
	draftData, _ := json.Marshal(model.SendMessage{To: []string{"work@work.test"}, Subject: "E2E draft", Text: "draft body"})
	if err := os.WriteFile(draftPath, draftData, 0o600); err != nil {
		t.Fatal(err)
	}
	draft := runJSON(t, env, binary, "mail", "draft", "create", "--connection", "personal", "--file", draftPath)
	if executed := runJSON(t, env, binary, "operation", "execute", draft["token"].(string)); executed["status"] != "succeeded" {
		t.Fatalf("draft execution: %#v", executed)
	}

	currentFolder := "INBOX"
	for _, action := range []struct{ command, destination string }{{"archive", "Archive"}, {"move", "Target"}, {"trash", "Trash"}} {
		current := waitMessage(t, env, binary, "personal", currentFolder, "E2E prepared")
		currentUID := fmt.Sprint(uint32(current["uid"].(float64)))
		args := []string{"mail", action.command, "--connection", "personal", "--folder", currentFolder, "--uid", currentUID}
		if action.command == "move" {
			args = append(args, "--destination", action.destination)
		}
		operation := runJSON(t, env, binary, args...)
		if executed := runJSON(t, env, binary, "operation", "execute", operation["token"].(string)); executed["status"] != "succeeded" {
			t.Fatalf("%s execution: %#v", action.command, executed)
		}
		currentFolder = action.destination
	}
	if sent := waitMessage(t, env, binary, "work", "Sent", "E2E prepared"); sent["subject"] != "E2E prepared" {
		t.Fatalf("sent copy: %#v", sent)
	}

	runJSON(t, env, binary, "connection", "discover", "work")
	runJSON(t, env, binary, "connection", "discover", "personal")
	cfg, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	collections := map[string]string{}
	for _, connection := range cfg.Connections {
		if connection.Calendar != nil && len(connection.Calendar.Collections) > 0 {
			collections[connection.ID] = connection.Calendar.Collections[0].ID
		}
	}
	collection := collections["work"]
	if collection == "" {
		t.Fatal("connection discovery did not persist a calendar collection")
	}
	eventPath := filepath.Join(t.TempDir(), "event.json")
	event := model.Event{ID: "e2e-event", CollectionID: collection, Title: "E2E calendar", Start: time.Now().UTC().Add(time.Hour).Truncate(time.Second), End: time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)}
	data, _ := json.Marshal(event)
	if err := os.WriteFile(eventPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	calendarPrepared := runJSON(t, env, binary, "calendar", "create", "--connection", "work", "--file", eventPath)
	calendarResult := runJSON(t, env, binary, "operation", "execute", calendarPrepared["token"].(string))
	if calendarResult["status"] != "succeeded" {
		t.Fatalf("calendar execute: %#v", calendarResult)
	}
	created := calendarResult["result"].(map[string]any)["event"].(map[string]any)
	personalEventPath := filepath.Join(t.TempDir(), "personal-event.json")
	personalEvent := model.Event{ID: "e2e-personal-event", CollectionID: collections["personal"], Title: "E2E personal calendar", Start: event.Start, End: event.End}
	personalData, _ := json.Marshal(personalEvent)
	if err := os.WriteFile(personalEventPath, personalData, 0o600); err != nil {
		t.Fatal(err)
	}
	personalPrepared := runJSON(t, env, binary, "calendar", "create", "--connection", "personal", "--file", personalEventPath)
	if result := runJSON(t, env, binary, "operation", "execute", personalPrepared["token"].(string)); result["status"] != "succeeded" {
		t.Fatalf("personal calendar execute: %#v", result)
	}
	agenda := runJSON(t, env, binary, "calendar", "list", "--start", event.Start.Add(-time.Hour).Format(time.RFC3339), "--end", event.End.Add(time.Hour).Format(time.RFC3339), "--query", "E2E")
	if len(agenda["events"].([]any)) != 3 {
		t.Fatalf("multi-account agenda: %#v", agenda)
	}
	event.Title = "E2E calendar updated"
	event.Href = created["href"].(string)
	event.ETag = created["etag"].(string)
	stale := event
	stale.ETag = "stale-etag"
	staleData, _ := json.Marshal(stale)
	if err := os.WriteFile(eventPath, staleData, 0o600); err != nil {
		t.Fatal(err)
	}
	staleUpdate := runJSON(t, env, binary, "calendar", "update", "--connection", "work", "--file", eventPath)
	failed, stderr := runJSONFailure(t, env, binary, "operation", "execute", staleUpdate["token"].(string))
	if failed["status"] != "failed" || !strings.Contains(stderr, "refresh and prepare") {
		t.Fatalf("stale update = %#v, stderr=%s", failed, stderr)
	}
	data, _ = json.Marshal(event)
	if err := os.WriteFile(eventPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	calendarUpdate := runJSON(t, env, binary, "calendar", "update", "--connection", "work", "--file", eventPath)
	updatedResult := runJSON(t, env, binary, "operation", "execute", calendarUpdate["token"].(string))
	updated := updatedResult["result"].(map[string]any)["event"].(map[string]any)
	calendarDelete := runJSON(t, env, binary, "calendar", "delete", "--connection", "work", "--collection", collection, "--href", updated["href"].(string), "--etag", updated["etag"].(string))
	if deleted := runJSON(t, env, binary, "operation", "execute", calendarDelete["token"].(string)); deleted["status"] != "succeeded" {
		t.Fatalf("calendar delete: %#v", deleted)
	}

	invitationPath := filepath.Join(t.TempDir(), "invitation.ics")
	runJSON(t, env, binary, "calendar", "ics", "--title", "E2E invitation", "--start", time.Now().UTC().Add(time.Hour).Format(time.RFC3339), "--end", time.Now().UTC().Add(2*time.Hour).Format(time.RFC3339), "--organizer", "work@work.test", "--attendee", "personal@personal.test", "--method", "request", "--output", invitationPath)
	invitation := runJSON(t, env, binary, "mail", "send", "--connection", "work", "--to", "personal@personal.test", "--subject", "E2E invitation", "--body", "calendar invitation", "--attachment", invitationPath)
	if sent := runJSON(t, env, binary, "operation", "execute", invitation["token"].(string)); sent["status"] != "succeeded" {
		t.Fatalf("invitation send: %#v", sent)
	}
	waitMessage(t, env, binary, "personal", "INBOX", "E2E invitation")

	badEnv := replaceEnv(env, "POSTHOUSE_TEST_PERSONAL_PASSWORD", "definitely-wrong")
	doctor := runJSON(t, badEnv, binary, "connection", "doctor", "personal")
	if doctor["ok"] != false || strings.Contains(fmt.Sprint(doctor), "definitely-wrong") {
		t.Fatalf("authentication doctor did not fail safely: %#v", doctor)
	}
}

func TestBuiltBinaryHTTPHealthReadinessAndAuthentication(t *testing.T) {
	binary := requiredEnv(t, "POSTHOUSE_TEST_BINARY")
	configPath := filepath.Join(t.TempDir(), "config.json")
	store, _ := config.New(configPath)
	endpoint := requiredEnv(t, "POSTHOUSE_TEST_RADICALE")
	if err := store.Save(model.Config{Connections: []model.Connection{testConnection("work", "work@work.test", "POSTHOUSE_TEST_WORK_PASSWORD", endpoint), testConnection("personal", "personal@personal.test", "POSTHOUSE_TEST_PERSONAL_PASSWORD", endpoint)}}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--config", configPath, "mcp", "http", "--address", address)
	command.Env = append(os.Environ(), "POSTHOUSE_MCP_TOKEN=e2e-token")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = command.Wait() }()
	waitHTTP(t, "http://"+address+"/healthz")
	for _, path := range []string{"/healthz", "/readyz"} {
		response, err := http.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status %d; stderr=%s", path, response.StatusCode, stderr.String())
		}
	}
	request, _ := http.NewRequest(http.MethodPost, "http://"+address+"/mcp", strings.NewReader(`{}`))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP status %d", response.StatusCode)
	}
	httpMCPClient := mcp.NewClient(&mcp.Implementation{Name: "posthouse-http-e2e", Version: "0.2.0"}, nil)
	httpSession, err := httpMCPClient.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: "http://" + address + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: "e2e-token"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cacheResult, err := httpSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "cache_status", Arguments: map[string]any{}})
	if err != nil || cacheResult.IsError {
		t.Fatalf("authenticated HTTP cache_status: %#v, %v", cacheResult, err)
	}
	httpPrepared := callToolMap(t, httpSession, "messages_send_prepare", map[string]any{"connection": "work", "to": []string{"personal@personal.test"}, "subject": "MCP HTTP prepared", "text": "http body"})
	httpExecuted := callToolMap(t, httpSession, "operation_execute", map[string]any{"token": httpPrepared["token"]})
	if httpExecuted["status"] != "succeeded" {
		t.Fatalf("MCP HTTP execution: %#v", httpExecuted)
	}
	_ = httpSession.Close()
	waitMessage(t, append(os.Environ(), "POSTHOUSE_CONFIG="+configPath), binary, "personal", "INBOX", "MCP HTTP prepared")
}

func callToolMap(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil || result.IsError {
		t.Fatalf("MCP %s: %#v, %v", name, result, err)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("MCP %s structured content has type %T", name, result.StructuredContent)
	}
	return structured
}

func testConnection(id, email, secretEnv, calendarEndpoint string) model.Connection {
	return model.Connection{ID: id, Name: id, Category: id, Identity: model.Identity{Email: email}, Mail: &model.MailConfig{Username: email, Secret: model.SecretRef{Env: secretEnv}, IMAP: model.IMAPConfig{Address: required("POSTHOUSE_TEST_GREENMAIL_IMAP"), Insecure: true}, SMTP: model.SMTPConfig{Address: required("POSTHOUSE_TEST_GREENMAIL_SMTP"), Insecure: true}, Folders: model.FolderConfig{Inbox: "INBOX", Sent: "Sent", Drafts: "Drafts", Archive: "Archive", Trash: "Trash"}, SentCopy: "provider-managed"}, Calendar: &model.CalendarConfig{Kind: "caldav", URL: calendarEndpoint, Username: id, Secret: model.SecretRef{Env: secretEnv}}}
}

func waitMessage(t *testing.T, env []string, binary, connection, folder, subject string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		page := runJSON(t, env, binary, "mail", "search", "--connection", connection, "--folder", folder, "--query", subject)
		for _, item := range page["messages"].([]any) {
			message := item.(map[string]any)
			if message["subject"] == subject {
				return message
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("message %q did not arrive in %s/%s", subject, connection, folder)
	return nil
}

func ensureMailbox(t *testing.T, username, password, mailbox string) {
	t.Helper()
	client, err := imapclient.DialInsecure(requiredEnv(t, "POSTHOUSE_TEST_GREENMAIL_IMAP"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Login(username, password).Wait(); err != nil {
		t.Fatal(err)
	}
	if err := client.Create(mailbox, nil).Wait(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "exist") {
		t.Fatalf("create mailbox %s: %v", mailbox, err)
	}
	_ = client.Logout().Wait()
}

func runJSON(t *testing.T, env []string, binary string, args ...string) map[string]any {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env = env
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, output)
	}
	var value map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatalf("decode %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return value
}

func runJSONConcurrently(t *testing.T, env []string, binary string, argumentSets ...[]string) []map[string]any {
	t.Helper()
	type execution struct {
		stdout bytes.Buffer
		stderr bytes.Buffer
		cmd    *exec.Cmd
	}
	executions := make([]execution, len(argumentSets))
	for index, args := range argumentSets {
		executions[index].cmd = exec.Command(binary, args...)
		executions[index].cmd.Env = env
		executions[index].cmd.Stdout = &executions[index].stdout
		executions[index].cmd.Stderr = &executions[index].stderr
		if err := executions[index].cmd.Start(); err != nil {
			t.Fatalf("start %v: %v", args, err)
		}
	}
	results := make([]map[string]any, len(executions))
	for index := range executions {
		if err := executions[index].cmd.Wait(); err != nil {
			t.Fatalf("concurrent command %v failed: %v\nstderr: %s\nstdout: %s", argumentSets[index], err, executions[index].stderr.String(), executions[index].stdout.String())
		}
		if err := json.Unmarshal(executions[index].stdout.Bytes(), &results[index]); err != nil {
			t.Fatalf("decode concurrent output %q: %v", executions[index].stdout.String(), err)
		}
	}
	return results
}

func runJSONFailure(t *testing.T, env []string, binary string, args ...string) (map[string]any, string) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Env = env
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err == nil {
		t.Fatalf("%s unexpectedly succeeded", strings.Join(args, " "))
	}
	var value map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		t.Fatalf("decode failed %s: %v\nstdout=%s\nstderr=%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return value, stderr.String()
}

func replaceEnv(env []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}

type bearerTransport struct{ token string }

func (transport bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+transport.token)
	return http.DefaultTransport.RoundTrip(clone)
}
func waitHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(url)
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server did not start at %s", url)
}
func ensureCalendar(t *testing.T, endpoint, path, user, password string) {
	t.Helper()
	request, _ := http.NewRequest("MKCALENDAR", strings.TrimRight(endpoint, "/")+path, nil)
	request.SetBasicAuth(user, password)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("MKCALENDAR %s", response.Status)
	}
}
func requiredEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required", name)
	}
	return value
}
func required(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic(fmt.Sprintf("%s is required", name))
	}
	return value
}

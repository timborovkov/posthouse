//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
)

const surfacesAccessKey = "e2e-access-key-1"

func TestBuiltBinaryRESTAndMCPMailCalendarSurfaces(t *testing.T) {
	binary := requiredEnv(t, "POSTHOUSE_TEST_BINARY")
	endpoint := requiredEnv(t, "POSTHOUSE_TEST_RADICALE")
	configPath := filepath.Join(t.TempDir(), "config.json")
	store, err := config.New(configPath)
	if err != nil {
		t.Fatal(err)
	}
	ensureCalendar(t, endpoint, "/work/work-calendar/", "work", requiredEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	ensureCalendar(t, endpoint, "/personal/personal-calendar/", "personal", requiredEnv(t, "POSTHOUSE_TEST_PERSONAL_PASSWORD"))
	if err := store.Save(model.Config{Connections: []model.Connection{
		testConnection("work", "work@work.test", "POSTHOUSE_TEST_WORK_PASSWORD", endpoint),
		testConnection("personal", "personal@personal.test", "POSTHOUSE_TEST_PERSONAL_PASSWORD", endpoint),
	}}); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "POSTHOUSE_CONFIG="+configPath, "POSTHOUSE_ACCESS_KEY="+surfacesAccessKey)
	runJSON(t, env, binary, "connection", "discover", "work")
	runJSON(t, env, binary, "connection", "discover", "personal")

	address := serveHTTP(t, binary, configPath, env)
	base := "http://" + address

	subject := fmt.Sprintf("REST surface mail %d", time.Now().UnixNano())
	prepared := restJSON(t, http.MethodPost, base+"/v1/mail/send", `{"connection":"work","to":["personal@personal.test"],"subject":"`+subject+`","text":"rest body"}`)
	token, _ := prepared["token"].(string)
	if token == "" {
		t.Fatalf("REST send prepare = %#v", prepared)
	}
	executed := restJSON(t, http.MethodPost, base+"/v1/operations/execute", `{"token":"`+token+`"}`)
	if executed["status"] != "succeeded" {
		t.Fatalf("REST execute = %#v", executed)
	}

	received := waitRESTMessage(t, base, "personal", subject)
	if received["unread"] != true {
		t.Fatalf("new REST message should be unread: %#v", received)
	}
	uid := uint32(received["uid"].(float64))

	unread := restJSON(t, http.MethodPost, base+"/v1/mail/search", `{"connections":["personal"],"folder":"INBOX","unread":true,"query":"`+subject+`"}`)
	if !searchContainsSubject(unread, subject) {
		t.Fatalf("unread filter missed message: %#v", unread)
	}
	detail := restJSON(t, http.MethodPost, base+"/v1/mail/get", fmt.Sprintf(`{"connection":"personal","folder":"INBOX","uid":%d}`, uid))
	if detail["text"] != "rest body" {
		t.Fatalf("REST get = %#v", detail)
	}

	listed := restJSON(t, http.MethodGet, base+"/v1/connections?page_size=10", "")
	collection := firstWritableCollection(t, listed, "work")
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	title := fmt.Sprintf("REST surface event %d", time.Now().UnixNano())
	eventBody, _ := json.Marshal(map[string]any{
		"connection": "work",
		"event": map[string]any{
			"id":            fmt.Sprintf("rest-event-%d", time.Now().UnixNano()),
			"collection_id": collection,
			"title":         title,
			"start":         start.Format(time.RFC3339),
			"end":           end.Format(time.RFC3339),
		},
	})
	calPrepared := restJSON(t, http.MethodPost, base+"/v1/calendar/create", string(eventBody))
	calToken, _ := calPrepared["token"].(string)
	if calToken == "" {
		t.Fatalf("REST calendar prepare = %#v", calPrepared)
	}
	calExecuted := restJSON(t, http.MethodPost, base+"/v1/operations/execute", `{"token":"`+calToken+`"}`)
	if calExecuted["status"] != "succeeded" {
		t.Fatalf("REST calendar execute = %#v", calExecuted)
	}
	agendaBody, _ := json.Marshal(map[string]any{
		"connections": []string{"work"},
		"query":       title,
		"start":       start.Add(-time.Hour).Format(time.RFC3339),
		"end":         end.Add(time.Hour).Format(time.RFC3339),
	})
	agenda := waitRESTEvent(t, base, string(agendaBody), title)
	if agenda["title"] != title {
		t.Fatalf("REST calendar list = %#v", agenda)
	}

	cliPage := runJSON(t, env, binary, "mail", "search", "--connection", "personal", "--folder", "INBOX", "--query", subject, "--unread")
	if !searchContainsSubject(cliPage, subject) {
		t.Fatalf("CLI unread search missed REST-sent message: %#v", cliPage)
	}

	httpMCPClient := mcp.NewClient(&mcp.Implementation{Name: "posthouse-surfaces-e2e", Version: "0.2.0"}, nil)
	httpSession, err := httpMCPClient.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: surfacesAccessKey}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer httpSession.Close()
	searched := callToolMap(t, httpSession, "messages_search", map[string]any{"connections": []string{"personal"}, "folder": "INBOX", "query": subject, "unread": true})
	if !searchContainsSubject(searched, subject) {
		t.Fatalf("MCP search missed REST-sent message: %#v", searched)
	}
	events := callToolMap(t, httpSession, "events_list", map[string]any{
		"connections": []string{"work"},
		"query":       title,
		"start":       start.Add(-time.Hour).Format(time.RFC3339),
		"end":         end.Add(time.Hour).Format(time.RFC3339),
	})
	if !eventListContainsTitle(events, title) {
		t.Fatalf("MCP events_list missed REST-created event: %#v", events)
	}
}

func serveHTTP(t *testing.T, binary, configPath string, env []string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, binary, "--config", configPath, "serve", "--address", address)
	command.Env = env
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = command.Wait() })
	waitHTTP(t, "http://"+address+"/healthz")
	return address
}

func restJSON(t *testing.T, method, url, body string) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+surfacesAccessKey)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s %s status=%d body=%s", method, url, response.StatusCode, payload)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("decode %s: %v\n%s", url, err, payload)
	}
	return value
}

func waitRESTMessage(t *testing.T, base, connection, subject string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"connections":[%q],"folder":"INBOX","query":%q}`, connection, subject)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page := restJSON(t, http.MethodPost, base+"/v1/mail/search", body)
		for _, item := range asObjects(page["messages"]) {
			if item["subject"] == subject {
				return item
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("REST search did not find %q", subject)
	return nil
}

func waitRESTEvent(t *testing.T, base, body, title string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		page := restJSON(t, http.MethodPost, base+"/v1/calendar/events", body)
		for _, item := range asObjects(page["events"]) {
			if item["title"] == title {
				return item
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("REST calendar list did not find %q", title)
	return nil
}

func firstWritableCollection(t *testing.T, listed map[string]any, connectionID string) string {
	t.Helper()
	for _, item := range asObjects(listed["connections"]) {
		if item["id"] != connectionID {
			continue
		}
		calendar, _ := item["calendar"].(map[string]any)
		for _, collection := range asObjects(calendar["collections"]) {
			if collection["read_only"] == true {
				continue
			}
			id, _ := collection["id"].(string)
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("no writable collection on %s in %#v", connectionID, listed)
	return ""
}

func searchContainsSubject(page map[string]any, subject string) bool {
	for _, item := range asObjects(page["messages"]) {
		if item["subject"] == subject {
			return true
		}
	}
	return false
}

func eventListContainsTitle(page map[string]any, title string) bool {
	for _, item := range asObjects(page["events"]) {
		if item["title"] == title {
			return true
		}
	}
	return false
}

func asObjects(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if ok {
			result = append(result, object)
		}
	}
	return result
}

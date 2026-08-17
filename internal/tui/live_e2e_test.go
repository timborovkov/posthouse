//go:build e2e

package tuiapp

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tui "github.com/grindlemire/go-tui"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

func TestTUISendSearchAndCreateEventAgainstGenericServers(t *testing.T) {
	endpoint := requiredLiveEnv(t, "POSTHOUSE_TEST_RADICALE")
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(bytes32()))
	t.Setenv("POSTHOUSE_TEST_WORK_PASSWORD", requiredLiveEnv(t, "POSTHOUSE_TEST_WORK_PASSWORD"))
	t.Setenv("POSTHOUSE_TEST_PERSONAL_PASSWORD", requiredLiveEnv(t, "POSTHOUSE_TEST_PERSONAL_PASSWORD"))

	ensureLiveCalendar(t, endpoint, "/work/work-calendar/", "work", os.Getenv("POSTHOUSE_TEST_WORK_PASSWORD"))
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	connections := []model.Connection{
		liveMailCalendarConnection("work", "work@work.test", "POSTHOUSE_TEST_WORK_PASSWORD", endpoint),
		liveMailCalendarConnection("personal", "personal@personal.test", "POSTHOUSE_TEST_PERSONAL_PASSWORD", endpoint),
	}
	if err := store.Save(model.Config{Connections: connections}); err != nil {
		t.Fatal(err)
	}
	application := service.New(store)
	if _, err := application.DiscoverConnection(t.Context(), "work"); err != nil {
		t.Fatalf("discover work: %v", err)
	}
	app := New(application)
	defer app.close()
	waitSnapshot(t, app)

	subject := fmt.Sprintf("TUI surface mail %d", time.Now().UnixNano())
	app.beginEditor("mail", []string{"work", "send", "personal@personal.test", subject, "tui body", ""})
	app.submitEditor()
	if app.pendingToken.Get() == "" {
		t.Fatalf("TUI send prepare failed: %s", app.errorText.Get())
	}
	confirmLiveOperation(t, app)
	if got := app.lastOperation.Get(); got.Status != "succeeded" {
		t.Fatalf("TUI send execute status=%q error=%q", got.Status, app.lastOperationError.Get())
	}

	if !waitTUIMessage(t, app, subject) {
		t.Fatalf("TUI inbox did not show %q; error=%q", subject, app.errorText.Get())
	}
	app.query.Set(subject)
	app.refresh()
	if !waitTUIMessage(t, app, subject) {
		t.Fatalf("TUI search for %q missed the message", subject)
	}
	app.query.Set("no-such-tui-subject-filter")
	app.refresh()
	snap := waitSnapshot(t, app)
	for _, message := range snap.messages {
		if message.Subject == subject {
			t.Fatalf("TUI negative search still listed %q", subject)
		}
	}

	collection := ""
	for _, connection := range app.connections.Get() {
		if connection.ID == "work" && connection.Calendar != nil {
			for _, candidate := range connection.Calendar.Collections {
				if !candidate.ReadOnly {
					collection = candidate.ID
					break
				}
			}
		}
	}
	if collection == "" {
		t.Fatal("discover did not persist a writable work calendar collection")
	}
	start := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	end := start.Add(time.Hour)
	title := fmt.Sprintf("TUI surface event %d", time.Now().UnixNano())
	app.beginEditor("event", []string{"work", collection, title, start.Format(time.RFC3339), end.Format(time.RFC3339)})
	app.submitEditor()
	if app.pendingToken.Get() == "" {
		t.Fatalf("TUI event prepare failed: %s", app.errorText.Get())
	}
	confirmLiveOperation(t, app)
	if got := app.lastOperation.Get(); got.Status != "succeeded" {
		t.Fatalf("TUI event execute status=%q error=%q", got.Status, app.lastOperationError.Get())
	}
	app.query.Set(title)
	if !waitTUIEvent(t, app, title) {
		t.Fatalf("TUI agenda did not show %q; error=%q", title, app.errorText.Get())
	}
}

func confirmLiveOperation(t *testing.T, app *posthouseApp) {
	t.Helper()
	app.confirmModal(tui.KeyEvent{})
	select {
	case next := <-app.operationUpdates:
		app.applyOperation(next)
	case <-time.After(20 * time.Second):
		t.Fatal("timed out waiting for TUI operation execution")
	}
}

func waitSnapshot(t *testing.T, app *posthouseApp) snapshot {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case next := <-app.updates:
			app.applySnapshot(next)
			if next.generation == app.refreshGeneration.Load() {
				return next
			}
		case <-time.After(time.Until(deadline)):
			t.Fatal("timed out waiting for TUI refresh")
			return snapshot{}
		}
	}
	t.Fatal("timed out waiting for current TUI snapshot")
	return snapshot{}
}

func waitTUIMessage(t *testing.T, app *posthouseApp, subject string) bool {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range app.messages.Get() {
			if message.Subject == subject {
				return true
			}
		}
		app.refresh()
		waitSnapshot(t, app)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func waitTUIEvent(t *testing.T, app *posthouseApp, title string) bool {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, event := range app.events.Get() {
			if event.Title == title {
				return true
			}
		}
		app.refresh()
		waitSnapshot(t, app)
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func liveMailCalendarConnection(id, email, secretEnv, calendarEndpoint string) model.Connection {
	return model.Connection{
		ID: id, Name: id, Category: id, Identity: model.Identity{Email: email},
		Mail: &model.MailConfig{
			Username: email, Secret: model.SecretRef{Env: secretEnv},
			IMAP:     model.IMAPConfig{Address: requiredLive("POSTHOUSE_TEST_GREENMAIL_IMAP"), Insecure: true},
			SMTP:     model.SMTPConfig{Address: requiredLive("POSTHOUSE_TEST_GREENMAIL_SMTP"), Insecure: true},
			Folders:  model.FolderConfig{Inbox: "INBOX"},
			SentCopy: "provider-managed",
		},
		Calendar: &model.CalendarConfig{Kind: "caldav", URL: calendarEndpoint, Username: id, Secret: model.SecretRef{Env: secretEnv}},
	}
}

func ensureLiveCalendar(t *testing.T, endpoint, collectionPath, username, password string) {
	t.Helper()
	request, err := http.NewRequest("MKCALENDAR", strings.TrimRight(endpoint, "/")+collectionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(username, password)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("MKCALENDAR: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusConflict {
		t.Fatalf("MKCALENDAR status %s", response.Status)
	}
}

func requiredLiveEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required for live TUI e2e", name)
	}
	return value
}

func requiredLive(name string) string {
	value := os.Getenv(name)
	if value == "" {
		panic(name + " is required")
	}
	return value
}

func bytes32() []byte {
	return make([]byte, 32)
}

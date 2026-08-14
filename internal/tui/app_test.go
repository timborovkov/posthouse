package tuiapp

// These tests protect the keyboard-first navigation and write-confirmation
// state machine without coupling correctness to terminal escape snapshots.

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tui "github.com/grindlemire/go-tui"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/service"
)

func TestViewNavigationWrapsAndClampsSelection(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.view.Set(len(viewNames) - 1)
	app.selected.Set(9)
	app.switchView(1)
	if app.view.Get() != 0 || app.selected.Get() != 0 {
		t.Fatalf("switchView returned view=%d selected=%d", app.view.Get(), app.selected.Get())
	}
	app.switchView(-1)
	if app.view.Get() != len(viewNames)-1 {
		t.Fatalf("backward switch returned view=%d", app.view.Get())
	}
}

func TestSearchModeConsumesTextAndEscapeCancels(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.searching.Set(true)
	bindings := app.KeyMap()
	dispatch(bindings, tui.KeyEvent{Key: tui.KeyRune, Rune: 'x'})
	if app.query.Get() != "x" {
		t.Fatalf("query=%q", app.query.Get())
	}
	dispatch(bindings, tui.KeyEvent{Key: tui.KeyEscape})
	if app.searching.Get() || app.query.Get() != "" {
		t.Fatalf("escape left search active with %q", app.query.Get())
	}
}

func TestModalEscapeNeverExecutesPendingToken(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.modal.Set(true)
	app.pendingToken.Set("opaque")
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.modal.Get() || app.pendingToken.Get() != "" {
		t.Fatal("escape did not cancel pending modal")
	}
}

func TestComposePreparesExactPreviewBeforeConfirmation(t *testing.T) {
	app := testAppWithMailConnection(t)
	defer app.close()
	app.beginEditor("mail", []string{"work", "send", "person@example.test", "Status", "Private body", ""})
	app.submitEditor()
	if app.editor.Get() || !app.modal.Get() || app.pendingToken.Get() == "" {
		t.Fatalf("compose state editor=%v modal=%v token=%q error=%q", app.editor.Get(), app.modal.Get(), app.pendingToken.Get(), app.errorText.Get())
	}
	preview := app.modalText.Get()
	for _, expected := range []string{"Connection: work", "operator@example.test", "person@example.test", "Status"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("preview %q does not contain %q", preview, expected)
		}
	}
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.modal.Get() || app.pendingToken.Get() != "" {
		t.Fatal("escape did not cancel the real prepared operation modal")
	}
}

func TestComposeEditorIsKeyboardNavigableAndCancellable(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("mail", []string{"work", "", "", ""})
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyRune, Rune: 'a'})
	if app.editorValues.Get()[0] != "worka" {
		t.Fatalf("editor value = %q", app.editorValues.Get()[0])
	}
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyTab})
	if app.editorStep.Get() != 1 {
		t.Fatalf("editor step = %d", app.editorStep.Get())
	}
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.editor.Get() || app.modal.Get() {
		t.Fatal("escape did not cancel editor")
	}
}

func TestMessageDetailNavigatesAttachments(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.view.Set(2)
	app.detail.Set(model.MessageDetail{Attachments: []model.Attachment{{ID: "one"}, {ID: "two"}}})
	app.move(1)
	if app.selected.Get() != 1 || app.itemCount() != 2 {
		t.Fatalf("attachment navigation selected=%d count=%d", app.selected.Get(), app.itemCount())
	}
}

func TestSplitValuesTrimsCommaSeparatedInput(t *testing.T) {
	got := splitValues(" one@example.test, ,two@example.test ")
	if len(got) != 2 || got[0] != "one@example.test" || got[1] != "two@example.test" {
		t.Fatalf("splitValues returned %#v", got)
	}
}

func TestCapabilityDetectionSkipsUnavailableAggregateReads(t *testing.T) {
	connections := []model.Connection{{ID: "calendar", Capabilities: []string{"calendar.read"}}}
	if connectionsHaveCapability(connections, "mail.read") || !connectionsHaveCapability(connections, "calendar.read") {
		t.Fatalf("capability detection failed for %#v", connections)
	}
}

func TestConnectionEditorSupportsSMTPOnlyOnboarding(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("connection", []string{"smtp", "SMTP", "work", "sender@example.test", "sender@example.test", "SMTP_PASSWORD", "", "smtp.example.test:465", "", "", ""})
	app.submitEditor()
	if app.errorText.Get() != "" {
		t.Fatalf("SMTP-only onboarding failed: %s", app.errorText.Get())
	}
	connections, err := app.service.Connections(model.Selector{})
	if err != nil || len(connections) != 1 || connections[0].Mail == nil || connections[0].Mail.SMTP.Address != "smtp.example.test:465" || connectionsHaveCapability(connections, "mail.read") {
		t.Fatalf("connections=%#v err=%v", connections, err)
	}
}

func TestSeriesUpdateRejectsExpandedOccurrence(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.view.Set(3)
	app.events.Set([]model.Event{{ID: "series#20260815T090000Z", SeriesID: "series", ConnectionID: "work", RecurrenceID: "2026-08-15T09:00:00Z", Start: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}})
	app.beginEditor("event-action", []string{"update-series", "Changed", "2026-08-15T09:00:00Z", "2026-08-15T10:00:00Z"})
	app.submitEditor()
	if !strings.Contains(app.errorText.Get(), "series master") || app.pendingToken.Get() != "" {
		t.Fatalf("error=%q token=%q", app.errorText.Get(), app.pendingToken.Get())
	}
}

func testApp(t *testing.T) *posthouseApp {
	t.Helper()
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(service.New(store))
}

func testAppWithMailConnection(t *testing.T) *posthouseApp {
	t.Helper()
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("TEST_MAIL_PASSWORD", "disposable")
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	connection := model.Connection{
		ID: "work", Name: "Work", Identity: model.Identity{Email: "operator@example.test"},
		Mail: &model.MailConfig{
			Username: "operator@example.test", Secret: model.SecretRef{Env: "TEST_MAIL_PASSWORD"},
			IMAP: model.IMAPConfig{Address: "127.0.0.1:1", Insecure: true},
			SMTP: model.SMTPConfig{Address: "127.0.0.1:1", Insecure: true}, SentCopy: "provider-managed",
		},
	}
	if err := store.Save(model.Config{Connections: []model.Connection{connection}}); err != nil {
		t.Fatal(err)
	}
	return New(service.New(store))
}

func dispatch(bindings tui.KeyMap, event tui.KeyEvent) {
	for _, binding := range bindings {
		if event.IsRune() && (binding.Pattern.AnyRune || binding.Pattern.Rune == event.Rune) {
			binding.Handler(event)
			return
		}
		if !event.IsRune() && binding.Pattern.Key == event.Key {
			binding.Handler(event)
			return
		}
	}
}

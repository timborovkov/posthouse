package tuiapp

// These tests protect the keyboard-first navigation and write-confirmation
// state machine without coupling correctness to terminal escape snapshots.

import (
	"context"
	"encoding/base64"
	"errors"
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

func TestOperationExecutionRunsOffEventLoopAndIsCancellable(t *testing.T) {
	app := testApp(t)
	defer app.close()
	started := make(chan struct{})
	app.executeOperation = func(ctx context.Context, token string) (model.OperationResult, error) {
		close(started)
		<-ctx.Done()
		return model.OperationResult{Token: token, Status: "failed"}, ctx.Err()
	}
	app.modal.Set(true)
	app.pendingToken.Set("opaque")
	app.confirmModal(tui.KeyEvent{})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background operation did not start")
	}
	if app.executingToken.Get() != "opaque" || app.pendingToken.Get() != "" || !app.modal.Get() {
		t.Fatalf("execution state token=%q pending=%q modal=%v", app.executingToken.Get(), app.pendingToken.Get(), app.modal.Get())
	}
	dispatch(app.KeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.modal.Get() {
		t.Fatal("Escape did not close executing operation modal")
	}
	select {
	case next := <-app.operationUpdates:
		app.applyOperation(next)
		if app.executingToken.Get() != "" || !strings.Contains(app.errorText.Get(), "canceled") {
			t.Fatalf("completed cancellation token=%q error=%q", app.executingToken.Get(), app.errorText.Get())
		}
	case <-time.After(time.Second):
		t.Fatal("canceled operation did not report completion")
	}
}

func TestProviderReadsRunOffEventLoopAndAreCancellable(t *testing.T) {
	for _, kind := range []string{"doctor", "message", "attachment"} {
		t.Run(kind, func(t *testing.T) {
			app := testApp(t)
			defer app.close()
			if app.refreshCancel != nil {
				app.refreshCancel()
				app.wg.Wait()
			}
			started, canceled := make(chan struct{}), make(chan struct{})
			wait := func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(canceled)
			}
			switch kind {
			case "doctor":
				app.view.Set(0)
				app.connections.Set([]model.Connection{{ID: "work"}})
				app.doctorConnection = func(ctx context.Context, _ string) (model.DoctorResult, error) {
					wait(ctx)
					return model.DoctorResult{}, ctx.Err()
				}
			case "message":
				app.view.Set(1)
				app.messages.Set([]model.Message{{ConnectionID: "work", Folder: "INBOX", UID: 1}})
				app.getMessage = func(ctx context.Context, _, _ string, _ uint32) (model.MessageDetail, error) {
					wait(ctx)
					return model.MessageDetail{}, ctx.Err()
				}
			case "attachment":
				app.view.Set(2)
				app.detail.Set(model.MessageDetail{Message: model.Message{ConnectionID: "work", Folder: "INBOX", UID: 1}, Attachments: []model.Attachment{{ID: "one"}}})
				app.getAttachment = func(ctx context.Context, _, _ string, _ uint32, _ string) (model.Attachment, []byte, error) {
					wait(ctx)
					return model.Attachment{}, nil, ctx.Err()
				}
			}
			app.openSelected(tui.KeyEvent{})
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("provider read did not start in the background")
			}
			if !app.modal.Get() || !app.loading.Get() {
				t.Fatalf("provider read state modal=%v loading=%v", app.modal.Get(), app.loading.Get())
			}
			app.cancelModal()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("provider read context was not canceled")
			}
			if app.modal.Get() || app.loading.Get() {
				t.Fatalf("canceled provider read state modal=%v loading=%v", app.modal.Get(), app.loading.Get())
			}
		})
	}
}

func TestLateOperationResultDoesNotReplaceNewConfirmationPreview(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.executingToken.Set("old")
	app.pendingToken.Set("new")
	app.modal.Set(true)
	app.modalText.Set("new operation preview")
	app.applyOperation(operationSnapshot{token: "old", result: model.OperationResult{Token: "old", Status: "succeeded"}})
	if app.modalText.Get() != "new operation preview" || app.pendingToken.Get() != "new" || app.executingToken.Get() != "" {
		t.Fatalf("late result changed preview=%q pending=%q executing=%q", app.modalText.Get(), app.pendingToken.Get(), app.executingToken.Get())
	}
}

func TestUncertainOperationRemainsVisible(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.executingToken.Set("uncertain-token")
	app.modal.Set(true)
	result := model.OperationResult{Token: "uncertain-token", Status: "uncertain", Result: map[string]any{"sent": true}}
	app.applyOperation(operationSnapshot{token: "uncertain-token", result: result, err: errors.New("response lost")})
	if app.lastOperation.Get().Status != "uncertain" || !strings.Contains(app.modalText.Get(), "uncertain-token") || !strings.Contains(app.modalText.Get(), "sent:true") || !strings.Contains(app.modalText.Get(), "response lost") {
		t.Fatalf("uncertain result was not retained: last=%#v modal=%q", app.lastOperation.Get(), app.modalText.Get())
	}
}

func TestPartialSourceErrorsAreVisible(t *testing.T) {
	got := appendSourceErrors("", []model.SourceError{{ConnectionID: "work", Code: "mail_unavailable", Message: "timeout"}, {ConnectionID: "calendar", CollectionID: "team", Code: "calendar_unavailable", Message: "offline"}})
	if !strings.Contains(got, "work: timeout") || !strings.Contains(got, "calendar/team: offline") {
		t.Fatalf("partial source errors = %q", got)
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

func TestCanceledRefreshSnapshotCannotReplaceCurrentState(t *testing.T) {
	app := testApp(t)
	defer app.close()
	if app.refreshCancel != nil {
		app.refreshCancel()
	}
	app.wg.Wait()
	app.refreshGeneration.Store(10)
	app.loading.Set(true)
	app.messages.Set([]model.Message{{Subject: "current"}})
	app.applySnapshot(snapshot{generation: 9, messages: []model.Message{{Subject: "stale"}}})
	if got := app.messages.Get(); len(got) != 1 || got[0].Subject != "current" || !app.loading.Get() {
		t.Fatalf("stale snapshot changed state: %#v loading=%v", got, app.loading.Get())
	}
	app.applySnapshot(snapshot{generation: 10, messages: []model.Message{{Subject: "fresh"}}})
	if got := app.messages.Get(); len(got) != 1 || got[0].Subject != "fresh" || app.loading.Get() {
		t.Fatalf("current snapshot state: %#v loading=%v", got, app.loading.Get())
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

func TestDeleteRejectsExpandedOccurrence(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.view.Set(3)
	app.events.Set([]model.Event{{ID: "series#20260815T090000Z", SeriesID: "series", ConnectionID: "work", RecurrenceID: "2026-08-15T09:00:00Z", Start: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}})
	app.beginEditor("event-action", []string{"delete", "", "", ""})
	app.submitEditor()
	if !strings.Contains(app.errorText.Get(), "cannot delete one expanded occurrence") || app.pendingToken.Get() != "" {
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

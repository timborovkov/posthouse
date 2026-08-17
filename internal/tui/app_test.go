package tuiapp

// These tests protect the keyboard-first navigation and write-confirmation
// state machine without coupling correctness to terminal escape snapshots.

import (
	"context"
	"encoding/base64"
	"errors"
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
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
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
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
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
	if app.lastOperation.Get().Status != "uncertain" || app.lastOperationError.Get() != "response lost" || !strings.Contains(app.modalText.Get(), "uncertain-token") || !strings.Contains(app.modalText.Get(), "sent:true") || !strings.Contains(app.modalText.Get(), "response lost") {
		t.Fatalf("uncertain result was not retained: last=%#v error=%q modal=%q", app.lastOperation.Get(), app.lastOperationError.Get(), app.modalText.Get())
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
	app.beginEditor("mail", []string{"work", "send", "person@example.test", "cc@example.test", "", "Status", "text", "Private body", ""})
	app.submitEditor()
	if app.editor.Get() || !app.modal.Get() || app.pendingToken.Get() == "" {
		t.Fatalf("compose state editor=%v modal=%v token=%q error=%q", app.editor.Get(), app.modal.Get(), app.pendingToken.Get(), app.errorText.Get())
	}
	preview := app.modalText.Get()
	for _, expected := range []string{"Connection: work", "operator@example.test", "person@example.test", "cc@example.test", "Status", "Private body"} {
		if !strings.Contains(preview, expected) {
			t.Fatalf("preview %q does not contain %q", preview, expected)
		}
	}
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.modal.Get() || app.pendingToken.Get() != "" {
		t.Fatal("escape did not cancel the real prepared operation modal")
	}
}

func TestComposeHTMLBodyTypeReachesPreview(t *testing.T) {
	app := testAppWithMailConnection(t)
	defer app.close()
	app.beginEditor("mail", []string{"work", "send", "person@example.test", "", "bcc@example.test", "Status", "html", "<p>Hello</p>", ""})
	app.submitEditor()
	if app.pendingToken.Get() == "" || !strings.Contains(app.modalText.Get(), "<p>Hello</p>") || !strings.Contains(app.modalText.Get(), "bcc@example.test") {
		t.Fatalf("html preview token=%q modal=%q error=%q", app.pendingToken.Get(), app.modalText.Get(), app.errorText.Get())
	}
}

func TestMountedModalKeyMapCancelsEditorAfterOpen(t *testing.T) {
	app := testApp(t)
	defer app.close()
	frozen := app.modalKeyMap()
	app.beginEditor("mail", []string{"work", "send", "", "", "", "", "text", "", ""})
	dispatch(frozen, tui.KeyEvent{Key: tui.KeyEscape})
	if app.editor.Get() || app.modal.Get() {
		t.Fatal("mounted modal keymap did not cancel editor")
	}
}

func TestCancelModalClearsEditorState(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("mail", []string{"work", "send", "", "", "", "", "text", "", ""})
	app.cancelModal()
	if app.editor.Get() || app.modal.Get() || app.editorKind.Get() != "" {
		t.Fatal("cancelModal left editor state")
	}
}

func TestComposeEditorBindsFieldStateAndCancelsFromModalKeys(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("mail", []string{"work", "send", "", "", "", "", "text", "", ""})
	if len(app.editorFields) != 9 || app.editorValue(0) != "work" || !app.editorFieldIsBody(7) {
		t.Fatalf("editor fields=%d body=%v value=%q", len(app.editorFields), app.editorFieldIsBody(7), app.editorValue(0))
	}
	app.editorFields[0].Set("work-mail")
	if app.editorValue(0) != "work-mail" {
		t.Fatalf("editor value = %q", app.editorValue(0))
	}
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.editor.Get() || app.modal.Get() {
		t.Fatal("escape did not cancel editor")
	}
}

func TestAttachmentSaveUsesSelectedMessageAttachment(t *testing.T) {
	app := testApp(t)
	defer app.close()
	if app.refreshCancel != nil {
		app.refreshCancel()
		app.wg.Wait()
	}
	app.view.Set(2)
	app.detail.Set(model.MessageDetail{
		Message:     model.Message{ConnectionID: "work", Folder: "INBOX", UID: 1},
		Attachments: []model.Attachment{{ID: "one", Name: "a.txt"}, {ID: "two", Name: "b.txt"}},
	})
	app.selected.Set(1)
	app.attachment.Set(model.Attachment{ID: "one", Name: "a.txt"})
	app.attachmentData.Set([]byte("old"))
	fetched := make(chan string, 1)
	app.getAttachment = func(_ context.Context, _, _ string, _ uint32, id string) (model.Attachment, []byte, error) {
		fetched <- id
		return model.Attachment{ID: id, Name: "b.txt"}, []byte("new"), nil
	}
	app.beginAttachmentSave()
	select {
	case id := <-fetched:
		if id != "two" {
			t.Fatalf("fetched %q, want selected attachment two", id)
		}
	case <-time.After(time.Second):
		t.Fatal("did not fetch the selected attachment")
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
	app.applySnapshot(snapshot{generation: 9, scope: "mail", messages: []model.Message{{Subject: "stale"}}})
	if got := app.messages.Get(); len(got) != 1 || got[0].Subject != "current" || !app.loading.Get() {
		t.Fatalf("stale snapshot changed state: %#v loading=%v", got, app.loading.Get())
	}
	app.applySnapshot(snapshot{generation: 10, scope: "mail", messages: []model.Message{{Subject: "fresh"}}})
	if got := app.messages.Get(); len(got) != 1 || got[0].Subject != "fresh" || app.loading.Get() {
		t.Fatalf("current snapshot state: %#v loading=%v", got, app.loading.Get())
	}
}

func TestEditorLabelsFitReservedColumn(t *testing.T) {
	app := testApp(t)
	defer app.close()
	for _, kind := range []string{"connection", "action", "event-action", "mail", "save", "event"} {
		app.editorKind.Set(kind)
		for _, label := range app.editorLabels() {
			if n := len([]rune(label)); n > editorLabelCells {
				t.Errorf("kind %s label %q is %d cells, want <= %d so the form stays inside the modal", kind, label, n, editorLabelCells)
			}
		}
	}
}

func TestConnectionEditorHelpAndProbePlaceholders(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.editorKind.Set("connection")
	if !strings.Contains(app.editorHelp(), "Esc cancels") || !strings.Contains(app.editorHelp(), "Enter saves") {
		t.Fatalf("connection help=%q", app.editorHelp())
	}
	if app.editorPlaceholder(6) != "blank to probe" || app.editorPlaceholder(7) != "blank to probe" || app.editorPlaceholder(8) != "blank to probe" {
		t.Fatalf("probe placeholders imap=%q smtp=%q caldav=%q", app.editorPlaceholder(6), app.editorPlaceholder(7), app.editorPlaceholder(8))
	}
}

func TestEditorFieldEscapeCancelsWhileFocused(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("connection", []string{"id", "name", "", "", "", "", "", "", "", "", ""})
	field := app.newEditorField(0)
	dispatch(field.KeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.editor.Get() || app.modal.Get() {
		t.Fatal("escape from a focused editor field did not cancel without saving")
	}
	connections, err := app.service.Connections(model.Selector{})
	if err != nil || len(connections) != 0 {
		t.Fatalf("cancel created a connection: %#v err=%v", connections, err)
	}
}

func TestConnectionEditorUsesSubmissionStartTLS(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("connection", []string{"smtp", "SMTP", "work", "sender@example.test", "sender@example.test", "SMTP_PASSWORD", "", "smtp.example.test:587", "", "", ""})
	app.submitEditor()
	if app.errorText.Get() != "" {
		t.Fatalf("SMTP 587 onboarding failed: %s", app.errorText.Get())
	}
	connections, err := app.service.Connections(model.Selector{})
	if err != nil || len(connections) != 1 || connections[0].Mail == nil || !connections[0].Mail.SMTP.StartTLS || connections[0].Mail.SMTP.TLS {
		t.Fatalf("connections=%#v err=%v", connections, err)
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
	if app.pendingDiscover.Get() != "smtp" || !strings.Contains(app.modalText.Get(), "Enter discovers") {
		t.Fatalf("onboarding did not offer discover: pending=%q modal=%q", app.pendingDiscover.Get(), app.modalText.Get())
	}
	connections, err := app.service.Connections(model.Selector{})
	if err != nil || len(connections) != 1 || connections[0].Mail == nil || connections[0].Mail.SMTP.Address != "smtp.example.test:465" || connectionsHaveCapability(connections, "mail.read") {
		t.Fatalf("connections=%#v err=%v", connections, err)
	}
}

func TestDiscoverIsInjectableCancellableAndOfferedAfterOnboard(t *testing.T) {
	app := testApp(t)
	defer app.close()
	started, canceled := make(chan struct{}), make(chan struct{})
	app.view.Set(0)
	app.connections.Set([]model.Connection{{ID: "work"}})
	app.discoverConnection = func(ctx context.Context, id string) (model.Connection, error) {
		if id != "work" {
			t.Fatalf("discover id=%q", id)
		}
		close(started)
		<-ctx.Done()
		close(canceled)
		return model.Connection{}, ctx.Err()
	}
	app.discoverSelected()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discover did not start")
	}
	app.cancelModal()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("discover was not canceled")
	}
}

func TestOnboardEnterStartsDiscoverFromModalKeys(t *testing.T) {
	app := testApp(t)
	defer app.close()
	started := make(chan struct{})
	app.discoverConnection = func(ctx context.Context, id string) (model.Connection, error) {
		if id != "smtp" {
			t.Fatalf("discover id=%q", id)
		}
		close(started)
		return model.Connection{ID: id}, nil
	}
	app.beginEditor("connection", []string{"smtp", "SMTP", "work", "sender@example.test", "sender@example.test", "SMTP_PASSWORD", "", "smtp.example.test:465", "", "", ""})
	app.submitEditor()
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEnter})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Enter after onboard did not start discover")
	}
}

func TestRFC3339MarksValidAndInvalidTimes(t *testing.T) {
	app := testApp(t)
	defer app.close()
	app.beginEditor("event", []string{"work", "team", "Planning", "2026-08-17T09:00:00Z", "not-a-time"})
	if app.rfc3339Mark(3) != "✓" || app.rfc3339Mark(4) != "×" || app.rfc3339Error(4) == "" || app.rfc3339Error(3) != "" {
		t.Fatalf("marks start=%q end=%q error=%q", app.rfc3339Mark(3), app.rfc3339Mark(4), app.rfc3339Error(4))
	}
	app.editorFields[4].Set("")
	if app.rfc3339Mark(4) != "" || app.rfc3339Error(4) != "" {
		t.Fatalf("empty end should wait for submit mark=%q error=%q", app.rfc3339Mark(4), app.rfc3339Error(4))
	}
	app.submitEditor()
	if app.pendingToken.Get() != "" || !strings.Contains(app.errorText.Get(), "RFC3339") {
		t.Fatalf("invalid time error=%q token=%q", app.errorText.Get(), app.pendingToken.Get())
	}
}

func TestAttachmentSaveWritesSecureFileAndRefusesOverwrite(t *testing.T) {
	app := testApp(t)
	defer app.close()
	dir := t.TempDir()
	path := filepath.Join(dir, "report.txt")
	app.attachment.Set(model.Attachment{Name: "report.txt"})
	app.attachmentData.Set([]byte("payload"))
	app.beginEditor("save", []string{path})
	app.submitEditor()
	if app.errorText.Get() != "" {
		t.Fatalf("save failed: %s", app.errorText.Get())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "payload" {
		t.Fatalf("saved %q %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions %v %v", info, err)
	}
	app.attachmentData.Set([]byte("other"))
	app.beginEditor("save", []string{path})
	app.submitEditor()
	if !strings.Contains(app.errorText.Get(), "already exists") {
		t.Fatalf("overwrite error=%q", app.errorText.Get())
	}
	dispatch(app.modalKeyMap(), tui.KeyEvent{Key: tui.KeyEscape})
	if app.editor.Get() {
		t.Fatal("escape did not cancel save editor")
	}
}

func TestMailPagingUsesCursorStackAndSearchResets(t *testing.T) {
	app := testApp(t)
	defer app.close()
	if app.refreshCancel != nil {
		app.refreshCancel()
		app.wg.Wait()
	}
	app.view.Set(1)
	app.messages.Set([]model.Message{{Subject: "one"}})
	app.mailCursor.Set("")
	app.mailNext.Set("cursor-2")
	app.pageList(1)
	if app.mailCursor.Get() != "cursor-2" || app.mailNext.Get() != "" || len(app.mailHistory.Get()) != 1 || app.mailHistory.Get()[0] != "" {
		t.Fatalf("next page cursor=%q next=%q history=%v", app.mailCursor.Get(), app.mailNext.Get(), app.mailHistory.Get())
	}
	app.pageList(1)
	if app.mailCursor.Get() != "cursor-2" {
		t.Fatalf("second next page advanced without a cursor cursor=%q", app.mailCursor.Get())
	}
	app.mailNext.Set("")
	app.pageList(-1)
	if app.mailCursor.Get() != "" || len(app.mailHistory.Get()) != 0 {
		t.Fatalf("previous page cursor=%q history=%v", app.mailCursor.Get(), app.mailHistory.Get())
	}
	app.mailCursor.Set("stale")
	app.mailHistory.Set([]string{""})
	app.resetMailPaging()
	if app.mailCursor.Get() != "" || len(app.mailHistory.Get()) != 0 {
		t.Fatalf("search reset cursor=%q history=%v", app.mailCursor.Get(), app.mailHistory.Get())
	}
	app.view.Set(3)
	app.eventCursor.Set("")
	app.eventNext.Set("events-2")
	app.pageList(1)
	if app.eventCursor.Get() != "events-2" || len(app.eventHistory.Get()) != 1 {
		t.Fatalf("agenda next page cursor=%q history=%v", app.eventCursor.Get(), app.eventHistory.Get())
	}
	app.resetEventPaging()
	if app.eventCursor.Get() != "" || len(app.eventHistory.Get()) != 0 {
		t.Fatalf("agenda reset cursor=%q history=%v", app.eventCursor.Get(), app.eventHistory.Get())
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

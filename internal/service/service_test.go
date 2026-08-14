package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/timborovkov/posthouse/internal/calendar"
	"github.com/timborovkov/posthouse/internal/config"
	postmail "github.com/timborovkov/posthouse/internal/mail"
	"github.com/timborovkov/posthouse/internal/model"
	"github.com/timborovkov/posthouse/internal/state"
)

func TestListConnectionsCursorPagination(t *testing.T) {
	application := serviceWithConnections(t,
		calendarConnection("c", "C"), calendarConnection("a", "A"), calendarConnection("b", "B"),
	)
	first, err := application.ListConnections(model.Selector{}, 2, "")
	if err != nil {
		t.Fatalf("first page returned error: %v", err)
	}
	if len(first.Connections) != 2 || first.Connections[0].ID != "a" || first.Connections[1].ID != "b" || first.NextCursor == "" {
		t.Fatalf("first page is %#v", first)
	}
	second, err := application.ListConnections(model.Selector{}, 2, first.NextCursor)
	if err != nil {
		t.Fatalf("second page returned error: %v", err)
	}
	if len(second.Connections) != 1 || second.Connections[0].ID != "c" || second.NextCursor != "" {
		t.Fatalf("second page is %#v", second)
	}
}

func TestConnectionsAppliesCollectionOnlySelector(t *testing.T) {
	caldav := model.Connection{ID: "calendar", Name: "Calendar", Calendar: &model.CalendarConfig{
		Kind: "caldav", URL: "http://localhost:5232", Username: "calendar", Secret: model.SecretRef{Env: "CALENDAR_PASSWORD"},
		Collections: []model.CalendarCollection{{ID: "team", Name: "Team", Path: "/calendar/team/"}},
	}}
	application := serviceWithConnections(t, mailConnection("mail"), caldav)
	connections, err := application.Connections(model.Selector{Collections: []string{"team"}})
	if err != nil || len(connections) != 1 || connections[0].ID != "calendar" {
		t.Fatalf("Connections returned %#v, %v", connections, err)
	}
}

func TestListEventsCursorPagination(t *testing.T) {
	application := serviceWithConnections(t, calendarConnection("calendar", "Calendar"))
	feed := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
		eventFixture("one", "One", "20260814T090000Z") +
		eventFixture("two", "Two", "20260815T090000Z") +
		eventFixture("three", "Three", "20260816T090000Z") + "END:VCALENDAR\r\n"
	application.calendar = calendar.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(feed))}, nil
	})})
	start := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	first, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 2, "")
	if err != nil || len(first.Events) != 2 || first.Events[0].ID != "one" || first.NextCursor == "" {
		t.Fatalf("first page is %#v, %v", first, err)
	}
	second, err := application.ListEvents(context.Background(), model.Selector{}, time.Time{}, time.Time{}, "", 2, first.NextCursor)
	if err != nil || len(second.Events) != 1 || second.Events[0].ID != "three" || second.NextCursor != "" {
		t.Fatalf("second page is %#v, %v", second, err)
	}
	if _, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "changed", 2, first.NextCursor); err == nil {
		t.Fatal("ListEvents accepted a cursor with changed filters")
	}
}

func TestListEventsDetectsSameKeyContentMutation(t *testing.T) {
	application := serviceWithConnections(t, calendarConnection("calendar", "Calendar"))
	title := "Original"
	application.calendar = calendar.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		feed := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + eventFixture("one", "One", "20260814T090000Z") + eventFixture("two", title, "20260815T090000Z") + "END:VCALENDAR\r\n"
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(feed))}, nil
	})})
	start, end := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	first, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 1, "")
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	title = "Changed"
	if _, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 1, first.NextCursor); err == nil || !strings.Contains(err.Error(), "sources changed") {
		t.Fatalf("changed source returned %v", err)
	}
}

func TestListEventsUsesEmptyStaleCache(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, calendarConnection("calendar", "Calendar"))
	available := true
	application.calendar = calendar.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if !available {
			return nil, fmt.Errorf("offline")
		}
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"))}, nil
	})})
	start, end := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if _, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 10, ""); err != nil {
		t.Fatal(err)
	}
	available = false
	page, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 10, "")
	if err != nil || len(page.Events) != 0 || len(page.Errors) != 1 || !page.Errors[0].Stale {
		t.Fatalf("stale empty page = %#v, %v", page, err)
	}
}

func TestSearchMessagesCompositeCursor(t *testing.T) {
	application := serviceWithConnections(t, mailConnection("a"), mailConnection("b"))
	messages := map[string][]model.Message{
		"a": {{ConnectionID: "a", UID: 3, ReceivedAt: instant(12)}, {ConnectionID: "a", UID: 2, ReceivedAt: instant(10)}, {ConnectionID: "a", UID: 1, ReceivedAt: instant(8)}},
		"b": {{ConnectionID: "b", UID: 2, ReceivedAt: instant(11)}, {ConnectionID: "b", UID: 1, ReceivedAt: instant(9)}},
	}
	application.mailSearch = func(connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		const uidValidity = 77
		if options.ExpectedUIDValidity != 0 && options.ExpectedUIDValidity != uidValidity {
			return postmail.SearchResult{}, fmt.Errorf("UIDVALIDITY mismatch")
		}
		var filtered []model.Message
		var uidNext uint32 = 1
		for _, message := range messages[connection.ID] {
			if message.UID >= uidNext {
				uidNext = message.UID + 1
			}
			if options.BeforeUID == 0 || message.UID < options.BeforeUID {
				filtered = append(filtered, message)
			}
		}
		hasMore := len(filtered) > options.Limit
		if hasMore {
			filtered = filtered[:options.Limit]
		}
		return postmail.SearchResult{Messages: filtered, UIDValidity: uidValidity, UIDNext: uidNext, HasMore: hasMore}, nil
	}
	selection := model.Selector{}
	options := postmail.SearchOptions{Query: "invoice"}
	first, err := application.SearchMessages(selection, options, 1, "")
	if err != nil || messageUIDs(first.Messages) != "a:3" || first.NextCursor == "" {
		t.Fatalf("first page is %#v, %v", first, err)
	}
	// A newly arrived message must not enter an already-started traversal, even
	// when that mailbox did not contribute to the first global page.
	messages["b"] = append([]model.Message{{ConnectionID: "b", UID: 3, ReceivedAt: instant(13)}}, messages["b"]...)
	second, err := application.SearchMessages(selection, options, 2, first.NextCursor)
	if err != nil || messageUIDs(second.Messages) != "b:2,a:2" || second.NextCursor == "" {
		t.Fatalf("second page is %#v, %v", second, err)
	}
	third, err := application.SearchMessages(selection, options, 2, second.NextCursor)
	if err != nil || messageUIDs(third.Messages) != "b:1,a:1" || third.NextCursor != "" {
		t.Fatalf("third page is %#v, %v", third, err)
	}
	changed := options
	changed.Query = "other"
	if _, err := application.SearchMessages(selection, changed, 2, first.NextCursor); err == nil {
		t.Fatal("SearchMessages accepted a cursor with changed filters")
	}
}

func TestSearchMessagesOfflineCachePaginatesMergedLiveTraversal(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	messages := []model.Message{
		{ConnectionID: "work", Folder: "INBOX", UID: 3, ReceivedAt: instant(12)},
		{ConnectionID: "work", Folder: "INBOX", UID: 2, ReceivedAt: instant(11)},
		{ConnectionID: "work", Folder: "INBOX", UID: 1, ReceivedAt: instant(10)},
	}
	application.mailSearch = func(_ model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		var filtered []model.Message
		for _, message := range messages {
			if options.BeforeUID == 0 || message.UID < options.BeforeUID {
				filtered = append(filtered, message)
			}
		}
		hasMore := len(filtered) > options.Limit
		if hasMore {
			filtered = filtered[:options.Limit]
		}
		return postmail.SearchResult{Messages: filtered, UIDValidity: 7, UIDNext: 4, HasMore: hasMore}, nil
	}

	liveFirst, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, "")
	if err != nil || liveFirst.NextCursor == "" {
		t.Fatalf("first live page = %#v, %v", liveFirst, err)
	}
	liveSecond, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, liveFirst.NextCursor)
	if err != nil || liveSecond.NextCursor == "" {
		t.Fatalf("second live page = %#v, %v", liveSecond, err)
	}
	if _, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, liveSecond.NextCursor); err != nil {
		t.Fatal(err)
	}

	var cursor string
	for wantUID := uint32(3); wantUID > 0; wantUID-- {
		page, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{Mode: "offline"}, 1, cursor)
		if err != nil || len(page.Messages) != 1 || page.Messages[0].UID != wantUID || !page.Messages[0].Stale {
			t.Fatalf("offline UID %d page = %#v, %v", wantUID, page, err)
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatalf("offline traversal has unexpected cursor %q", cursor)
	}
}

func TestPreparedOperationRejectsChangedConnection(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"person@example.test"}, Subject: "subject", Text: "body"})
	if err != nil {
		t.Fatal(err)
	}
	connection.Mail.SMTP.Address = "localhost:4025"
	if err := application.UpsertConnection(connection, true); err != nil {
		t.Fatal(err)
	}
	if _, err := application.ExecuteOperation(context.Background(), prepared.Token); err == nil || !strings.Contains(err.Error(), "preconditions changed") {
		t.Fatalf("ExecuteOperation error = %v", err)
	}
}

func TestOfflineMessageAndAttachmentReads(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	ledger, err := application.ensureState()
	if err != nil {
		t.Fatal(err)
	}
	detail := model.MessageDetail{Message: model.Message{ConnectionID: "work", Folder: "INBOX", UID: 7, Subject: "cached"}, Text: "offline body", Attachments: []model.Attachment{{ID: "file", Name: "file.txt", ContentType: "text/plain", Size: 7}}}
	data, _ := json.Marshal(detail)
	ctx := context.Background()
	if err := ledger.Put(ctx, state.CacheEntry{Namespace: "message_body", Key: messageCacheKey("work", "INBOX", 7), Kind: "message_body", CachedAt: time.Now(), Value: data}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Put(ctx, state.CacheEntry{Namespace: "attachment", Key: messageCacheKey("work", "INBOX", 7) + "/file", Kind: "attachment", CachedAt: time.Now(), Value: []byte("content")}); err != nil {
		t.Fatal(err)
	}
	got, err := application.GetMessageMode("work", "INBOX", 7, "offline")
	if err != nil || got.Text != "offline body" || !got.Stale || got.CachedAt.IsZero() {
		t.Fatalf("offline message = %#v, %v", got, err)
	}
	attachment, content, err := application.GetAttachmentMode(ctx, "work", "INBOX", 7, "file", "offline")
	if err != nil || attachment.Name != "file.txt" || !attachment.Stale || attachment.CachedAt.IsZero() || string(content) != "content" {
		t.Fatalf("offline attachment = %#v %q, %v", attachment, content, err)
	}
}

func TestPartialMailCursorKeepsFailedSourceSnapshot(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("a"), mailConnection("b"))
	bAvailable := false
	application.mailSearch = func(connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		if connection.ID == "b" && !bAvailable {
			return postmail.SearchResult{}, fmt.Errorf("temporary outage")
		}
		messages := []model.Message{{ConnectionID: connection.ID, UID: 2, ReceivedAt: instant(12)}, {ConnectionID: connection.ID, UID: 1, ReceivedAt: instant(10)}}
		if connection.ID == "b" {
			messages = []model.Message{{ConnectionID: "b", UID: 9, ReceivedAt: instant(13)}}
		}
		var filtered []model.Message
		for _, message := range messages {
			if options.BeforeUID == 0 || message.UID < options.BeforeUID {
				filtered = append(filtered, message)
			}
		}
		hasMore := len(filtered) > options.Limit
		if hasMore {
			filtered = filtered[:options.Limit]
		}
		return postmail.SearchResult{Messages: filtered, UIDValidity: 1, UIDNext: 10, HasMore: hasMore}, nil
	}
	first, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, "")
	if err != nil || len(first.Errors) != 1 || first.NextCursor == "" {
		t.Fatalf("first partial page = %#v, %v", first, err)
	}
	bAvailable = true
	continued, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, first.NextCursor)
	if err != nil || len(continued.Messages) != 1 || continued.Messages[0].ConnectionID != "a" || len(continued.Errors) != 1 {
		t.Fatalf("continued partial page = %#v, %v", continued, err)
	}
	fresh, err := application.SearchMessages(model.Selector{}, postmail.SearchOptions{}, 1, "")
	if err != nil || len(fresh.Messages) != 1 || fresh.Messages[0].ConnectionID != "b" {
		t.Fatalf("fresh traversal = %#v, %v", fresh, err)
	}
}

func TestPreparedOperationExpires(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"person@example.test"}, Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	application.now = func() time.Time { return prepared.ExpiresAt.Add(time.Second) }
	if _, err := application.ExecuteOperation(context.Background(), prepared.Token); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("ExecuteOperation error = %v", err)
	}
}

func TestInterruptedClaimBecomesUncertainWithoutRetry(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"person@example.test"}, Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := application.ensureState()
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := ledger.ClaimOperation(context.Background(), prepared.Token); err != nil || !claimed {
		t.Fatalf("ClaimOperation claimed=%v err=%v", claimed, err)
	}
	application.now = func() time.Time { return prepared.ExpiresAt.Add(time.Second) }
	result, err := application.ExecuteOperation(context.Background(), prepared.Token)
	if err != nil || result.Status != "uncertain" {
		t.Fatalf("ExecuteOperation result=%#v err=%v", result, err)
	}
}

func TestExecutingTokenDoesNotBlockUnrelatedOperation(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	application.mailBuild = func(model.Connection, model.SendMessage) ([]byte, error) { return []byte("message"), nil }
	application.mailSendRaw = func(model.Connection, model.SendMessage, []byte) error { return nil }
	first, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"first@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"second@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := application.ensureState()
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := ledger.ClaimOperation(context.Background(), first.Token); err != nil || !claimed {
		t.Fatalf("ClaimOperation claimed=%v err=%v", claimed, err)
	}
	enteredWait := make(chan struct{})
	var signal sync.Once
	application.now = func() time.Time {
		signal.Do(func() { close(enteredWait) })
		return time.Now().UTC()
	}
	firstContext, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, _ = application.ExecuteOperation(firstContext, first.Token)
	}()
	<-enteredWait
	secondDone := make(chan error, 1)
	go func() {
		result, executeErr := application.ExecuteOperation(context.Background(), second.Token)
		if executeErr == nil && result.Status != "succeeded" {
			executeErr = fmt.Errorf("status = %s", result.Status)
		}
		secondDone <- executeErr
	}()
	select {
	case executeErr := <-secondDone:
		if executeErr != nil {
			t.Fatal(executeErr)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("unrelated operation was blocked by executing token")
	}
	cancelFirst()
	<-firstDone
}

func TestPrepareSendPreviewIncludesExactContentAndThreading(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	application := serviceWithConnections(t, connection)
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{
		ConnectionID: "work", To: []string{"person@example.test"}, Subject: "subject", Text: "complete body",
		ReplyTo: "reply@example.test", InReplyTo: "parent-id", References: []string{"root-id", "parent-id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{"text": "complete body", "reply_to": "reply@example.test", "in_reply_to": "parent-id"} {
		if prepared.Preview[key] != want {
			t.Fatalf("preview[%q]=%#v want %#v", key, prepared.Preview[key], want)
		}
	}
	if references, ok := prepared.Preview["references"].([]string); !ok || len(references) != 2 {
		t.Fatalf("preview references=%#v", prepared.Preview["references"])
	}
}

func TestDraftUpdateCleanupFailurePreservesAppendedUIDAsUncertain(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	precondition := postmail.MessagePrecondition{UIDValidity: 7, ModSeq: 11}
	application.mailSnapshot = func(model.Connection, string, uint32) (postmail.MessagePrecondition, error) { return precondition, nil }
	application.mailAppend = func(model.Connection, string, model.SendMessage, []imap.Flag) (uint32, error) { return 42, nil }
	application.mailMarkDeleted = func(model.Connection, string, uint32, postmail.MessagePrecondition) error {
		return fmt.Errorf("concurrent modification")
	}
	prepared, err := application.PrepareDraft(context.Background(), "work", "mail.draft.update", "Drafts", 5, model.SendMessage{Subject: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.ExecuteOperation(context.Background(), prepared.Token)
	if err == nil || result.Status != "uncertain" || fmt.Sprint(result.Result["uid"]) != "42" || result.Result["cleanup"] != "failed" {
		t.Fatalf("ExecuteOperation result=%#v err=%v", result, err)
	}
	replayed, err := application.ExecuteOperation(context.Background(), prepared.Token)
	if err != nil || replayed.Status != "uncertain" || fmt.Sprint(replayed.Result["uid"]) != "42" {
		t.Fatalf("replay result=%#v err=%v", replayed, err)
	}
}

func TestSentCopyUsesDeliveredBytesAndAppendFailureIsUncertain(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := mailConnection("work")
	connection.Identity = model.Identity{Email: "work@example.test"}
	connection.Mail.SMTP = model.SMTPConfig{Address: "localhost:3025", Insecure: true}
	connection.Mail.SentCopy = "always"
	connection.Mail.Folders.Sent = "Sent"
	application := serviceWithConnections(t, connection)
	serialized := []byte("exact serialized message")
	builds, sends, appends := 0, 0, 0
	application.mailBuild = func(model.Connection, model.SendMessage) ([]byte, error) {
		builds++
		return append([]byte(nil), serialized...), nil
	}
	application.mailSendRaw = func(_ model.Connection, _ model.SendMessage, data []byte) error {
		sends++
		if string(data) != string(serialized) {
			t.Fatalf("sent data = %q", data)
		}
		return nil
	}
	application.mailAppendRaw = func(_ model.Connection, folder string, data []byte, _ []imap.Flag) (uint32, error) {
		appends++
		if folder != "Sent" || string(data) != string(serialized) {
			t.Fatalf("append folder=%q data=%q", folder, data)
		}
		return 0, fmt.Errorf("IMAP unavailable")
	}
	prepared, err := application.PrepareSend(context.Background(), model.SendMessage{ConnectionID: "work", To: []string{"person@example.test"}, Subject: "subject"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := application.ExecuteOperation(context.Background(), prepared.Token)
	if err == nil || result.Status != "uncertain" || result.Result["sent"] != true || result.Result["sent_copy"] != "failed" {
		t.Fatalf("ExecuteOperation result=%#v err=%v", result, err)
	}
	replayed, replayErr := application.ExecuteOperation(context.Background(), prepared.Token)
	if replayErr != nil || replayed.Status != "uncertain" || builds != 1 || sends != 1 || appends != 1 {
		t.Fatalf("replay=%#v err=%v counts=%d/%d/%d", replayed, replayErr, builds, sends, appends)
	}
}

func TestSyncAcceptsGranularReadCapability(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	calls := 0
	application.mailSearch = func(connection model.Connection, _ postmail.SearchOptions) (postmail.SearchResult, error) {
		calls++
		return postmail.SearchResult{Messages: []model.Message{{ConnectionID: connection.ID, UID: 1}}, UIDValidity: 1, UIDNext: 2}, nil
	}
	result, err := application.Sync(context.Background(), model.Selector{Capability: "mail.read"})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || result["messages"] != 1 {
		t.Fatalf("Sync returned %#v after %d calls", result, calls)
	}
	if _, err := application.Sync(context.Background(), model.Selector{Capability: "mail.send"}); err == nil {
		t.Fatal("Sync accepted non-readable capability")
	}
}

func TestSyncFreezesMailRangeAcrossPages(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("work"))
	clock := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	application.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	var cutoffs []time.Time
	application.mailSearch = func(connection model.Connection, options postmail.SearchOptions) (postmail.SearchResult, error) {
		cutoffs = append(cutoffs, options.Since)
		messages := make([]model.Message, 0, 101)
		for uid := uint32(101); uid > 0; uid-- {
			if options.BeforeUID == 0 || uid < options.BeforeUID {
				messages = append(messages, model.Message{ConnectionID: connection.ID, UID: uid, ReceivedAt: time.Unix(int64(uid), 0)})
			}
		}
		if len(messages) > options.Limit {
			messages = messages[:options.Limit]
		}
		return postmail.SearchResult{Messages: messages, UIDValidity: 1, UIDNext: 102}, nil
	}
	result, err := application.Sync(context.Background(), model.Selector{Capability: "mail.read"})
	if err != nil {
		t.Fatal(err)
	}
	if result["messages"] != 101 || len(cutoffs) != 2 || !cutoffs[0].Equal(cutoffs[1]) {
		t.Fatalf("Sync result=%#v cutoffs=%v", result, cutoffs)
	}
}

func TestCombinedSyncReportsFailedProtocolSources(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	application := serviceWithConnections(t, mailConnection("mail"), calendarConnection("calendar", "Calendar"))
	application.mailSearch = func(model.Connection, postmail.SearchOptions) (postmail.SearchResult, error) {
		return postmail.SearchResult{}, fmt.Errorf("mail offline")
	}
	application.calendar = calendar.NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"))}, nil
	})})
	result, err := application.Sync(context.Background(), model.Selector{})
	if err != nil {
		t.Fatal(err)
	}
	errors, ok := result["errors"].([]model.SourceError)
	if !ok || len(errors) != 1 || errors[0].ConnectionID != "mail" || errors[0].Code != "mail_sync_failed" {
		t.Fatalf("Sync result=%#v", result)
	}
}

func TestPrepareCalendarUpdateRejectsSeriesReplacementFromOccurrence(t *testing.T) {
	t.Setenv("POSTHOUSE_CACHE_KEY", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	connection := model.Connection{ID: "work", Name: "Work", Calendar: &model.CalendarConfig{
		Kind: "caldav", URL: "http://localhost:5232", Username: "work", Secret: model.SecretRef{Env: "CALENDAR_PASSWORD"},
		Collections: []model.CalendarCollection{{ID: "team", Path: "/work/team/"}},
	}}
	application := serviceWithConnections(t, connection)
	event := model.Event{ID: "series#20260815T090000Z", SeriesID: "series", CollectionID: "team", Href: "/work/team/series.ics", ETag: "etag", Start: instant(9), End: instant(10)}
	if _, err := application.PrepareCalendarWrite(context.Background(), "work", "calendar.update", event); err == nil || !strings.Contains(err.Error(), "series master") {
		t.Fatalf("PrepareCalendarWrite returned %v", err)
	}
}

func TestReplyRecipientsPreferReplyTo(t *testing.T) {
	original := model.MessageDetail{Message: model.Message{From: []model.Address{{Email: "from@example.test"}}}, ReplyTo: []model.Address{{Email: "reply@example.test"}}}
	got := replyRecipients(original)
	if len(got) != 1 || got[0] != "reply@example.test" {
		t.Fatalf("replyRecipients returned %#v", got)
	}
}

func serviceWithConnections(t *testing.T, connections ...model.Connection) *Service {
	t.Helper()
	store, err := config.New(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("config.New returned error: %v", err)
	}
	for _, connection := range connections {
		application := New(store)
		if err := application.UpsertConnection(connection, false); err != nil {
			t.Fatalf("UpsertConnection returned error: %v", err)
		}
	}
	return New(store)
}

func calendarConnection(id, name string) model.Connection {
	return model.Connection{ID: id, Name: name, Calendar: &model.CalendarConfig{URL: "http://localhost/calendar.ics"}}
}

func mailConnection(id string) model.Connection {
	return model.Connection{ID: id, Name: strings.ToUpper(id), Mail: &model.MailConfig{Username: id, SecretEnv: "PASSWORD", IMAP: model.IMAPConfig{Address: "localhost:143", Insecure: true}}}
}

func eventFixture(id, title, start string) string {
	return fmt.Sprintf("BEGIN:VEVENT\r\nUID:%s\r\nSUMMARY:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nEND:VEVENT\r\n", id, title, start, start)
}

func instant(hour int) time.Time {
	return time.Date(2026, 8, 14, hour, 0, 0, 0, time.UTC)
}

func messageUIDs(messages []model.Message) string {
	parts := make([]string, len(messages))
	for index, message := range messages {
		parts[index] = fmt.Sprintf("%s:%d", message.ConnectionID, message.UID)
	}
	return strings.Join(parts, ",")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

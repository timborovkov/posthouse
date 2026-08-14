package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/posthousehq/posthouse/internal/calendar"
	"github.com/posthousehq/posthouse/internal/config"
	postmail "github.com/posthousehq/posthouse/internal/mail"
	"github.com/posthousehq/posthouse/internal/model"
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
	second, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "", 2, first.NextCursor)
	if err != nil || len(second.Events) != 1 || second.Events[0].ID != "three" || second.NextCursor != "" {
		t.Fatalf("second page is %#v, %v", second, err)
	}
	if _, err := application.ListEvents(context.Background(), model.Selector{}, start, end, "changed", 2, first.NextCursor); err == nil {
		t.Fatal("ListEvents accepted a cursor with changed filters")
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

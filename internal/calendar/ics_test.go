package calendar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/posthousehq/posthouse/internal/model"
)

func TestGenerateAndParseRoundTrip(t *testing.T) {
	want := model.Event{ID: "standup-1", Title: "Standup, team", Description: "Line one\nLine two", Start: time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC), Attendees: []string{"one@example.com"}, Organizer: "owner@example.com"}
	generated, data, err := Generate(want)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	events, err := Parse([]byte(data))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Parse returned %d events", len(events))
	}
	got := events[0]
	if generated.ID != want.ID || got.ID != want.ID || got.Title != want.Title || got.Description != want.Description || !got.Start.Equal(want.Start) || !got.End.Equal(want.End) || got.Organizer != want.Organizer {
		t.Fatalf("round trip returned %#v, want %#v", got, want)
	}
}

func TestParseMultipleEvents(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + eventFixture("one", "First", "20260814T090000Z", "20260814T100000Z") + eventFixture("two", "Second", "20260815T090000Z", "20260815T100000Z") + "END:VCALENDAR\r\n"
	events, err := Parse([]byte(data))
	if err != nil || len(events) != 2 {
		t.Fatalf("Parse returned %#v, %v", events, err)
	}
}

func TestClientFiltersFeed(t *testing.T) {
	const secretName = "POSTHOUSE_TEST_CALENDAR_URL"
	t.Setenv(secretName, "http://localhost/calendar.ics")
	feed := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + eventFixture("one", "Planning", "20260814T090000Z", "20260814T100000Z") + eventFixture("two", "Dinner", "20260815T190000Z", "20260815T210000Z") + "END:VCALENDAR\r\n"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.Header.Get("Accept") != "text/calendar" {
			return response(http.StatusBadRequest, ""), nil
		}
		return response(http.StatusOK, feed), nil
	})
	client := NewClient(&http.Client{Transport: transport})
	connection := model.Connection{ID: "work", Calendar: &model.CalendarConfig{URLSecretEnv: secretName}}
	events, err := client.List(context.Background(), connection, time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), "plan")
	if err != nil || len(events) != 1 || events[0].ConnectionID != "work" || events[0].Title != "Planning" {
		t.Fatalf("List returned %#v, %v", events, err)
	}
}

func TestFilename(t *testing.T) {
	if got := Filename(model.Event{Title: "Team Planning / Q3"}); got != "team-planning-q3.ics" {
		t.Fatalf("Filename returned %q", got)
	}
}

func eventFixture(id, title, start, end string) string {
	return fmt.Sprintf("BEGIN:VEVENT\r\nUID:%s\r\nSUMMARY:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nEND:VEVENT\r\n", id, title, start, end)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

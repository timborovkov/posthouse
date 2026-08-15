package calendar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/timborovkov/posthouse/internal/model"
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

func TestGenerateUsesDateValuesForAllDayRecurrenceProperties(t *testing.T) {
	event := model.Event{
		ID: "days", Title: "Days", AllDay: true,
		Start: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 16, 0, 0, 0, 0, time.UTC),
		RecurrenceID:    "2026-08-15T00:00:00Z",
		RecurrenceDates: []time.Time{time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)},
		ExceptionDates:  []time.Time{time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC)},
	}
	_, generated, err := Generate(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"RECURRENCE-ID;VALUE=DATE:20260815", "RDATE;VALUE=DATE:20260817", "EXDATE;VALUE=DATE:20260818"} {
		if !strings.Contains(generated, line) {
			t.Fatalf("generated calendar lacks %q:\n%s", line, generated)
		}
	}
}

func TestParseRangePreservesRDATEPeriodDurations(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:periods
DTSTART:20260815T090000Z
DTEND:20260815T100000Z
RDATE;VALUE=PERIOD:20260816T090000Z/20260816T110000Z,20260817T090000Z/PT3H
SUMMARY:Variable length
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].End.Sub(events[0].Start) != time.Hour || events[1].End.Sub(events[1].Start) != 2*time.Hour || events[2].End.Sub(events[2].Start) != 3*time.Hour {
		t.Fatalf("RDATE period events = %#v", events)
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

func TestClientDoesNotExposeProviderErrorBody(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusBadGateway, "private calendar content and secret-token"), nil
	})})
	connection := model.Connection{ID: "work", Calendar: &model.CalendarConfig{Kind: "feed", URL: "http://localhost/calendar.ics"}}
	_, err := client.List(context.Background(), connection, time.Now(), time.Now().Add(time.Hour), "")
	if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error exposed provider body: %v", err)
	}
}

func TestClientDoesNotExposeSecretFeedURLOnTransportError(t *testing.T) {
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, &url.Error{Op: "Get", URL: request.URL.String(), Err: fmt.Errorf("dial failed")}
	})})
	connection := model.Connection{ID: "private", Calendar: &model.CalendarConfig{Kind: "feed", URL: "https://calendar.example.test/private.ics?token=sentinel-secret"}}
	_, err := client.List(context.Background(), connection, time.Now(), time.Now().Add(time.Hour), "")
	if err == nil || strings.Contains(err.Error(), "sentinel-secret") || strings.Contains(err.Error(), "calendar.example.test") {
		t.Fatalf("transport error exposed private URL: %v", err)
	}
}

func TestClientRejectsCrossOriginFeedRedirect(t *testing.T) {
	requests := 0
	client := NewClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		response := response(http.StatusFound, "")
		response.Request = request
		response.Header.Set("Location", "https://attacker.example/collect")
		return response, nil
	})})
	connection := model.Connection{ID: "private", Calendar: &model.CalendarConfig{Kind: "feed", URL: "https://calendar.example.test/private.ics?token=sentinel-secret"}}
	_, err := client.List(context.Background(), connection, time.Now(), time.Now().Add(time.Hour), "")
	if err == nil || requests != 1 || strings.Contains(err.Error(), "sentinel-secret") {
		t.Fatalf("cross-origin redirect requests=%d error=%v", requests, err)
	}
}

func TestCalDAVHrefMustStayWithinConfiguredCollection(t *testing.T) {
	connection := model.Connection{ID: "work", Calendar: &model.CalendarConfig{
		Kind: "caldav", URL: "https://calendar.example.test/root/",
		Collections: []model.CalendarCollection{{ID: "team", Path: "/root/team/"}},
	}}
	for _, href := range []string{"https://attacker.example/item.ics", "/root/personal/item.ics", "/root/team/../personal/item.ics"} {
		if err := ValidateCalDAVHref(connection, "team", href); err == nil {
			t.Fatalf("ValidateCalDAVHref accepted %q", href)
		}
	}
	if err := ValidateCalDAVHref(connection, "team", "/root/team/item.ics"); err != nil {
		t.Fatalf("ValidateCalDAVHref rejected collection object: %v", err)
	}
}

func TestCalDAVWriteRejectsAmbiguousCollectionName(t *testing.T) {
	connection := model.Connection{ID: "work", Calendar: &model.CalendarConfig{
		Kind: "caldav", URL: "https://calendar.example.test/root/",
		Collections: []model.CalendarCollection{
			{ID: "personal-one", Name: "Personal", Path: "/root/one/"},
			{ID: "personal-two", Name: "Personal", Path: "/root/two/"},
		},
	}}
	if err := ValidateCalDAVHref(connection, "Personal", ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous collection name returned %v", err)
	}
	if err := ValidateCalDAVHref(connection, "personal-two", "/root/two/event.ics"); err != nil {
		t.Fatalf("stable collection ID returned %v", err)
	}
}

func TestBasicAuthClientRejectsCrossOriginRequest(t *testing.T) {
	origin, _ := url.Parse("https://calendar.example.test/")
	called := false
	client := &basicAuthClient{origin: origin, username: "work", password: "sentinel", client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return response(http.StatusOK, ""), nil
	})}}
	request, _ := http.NewRequest(http.MethodGet, "https://attacker.example/item.ics", nil)
	if _, err := client.Do(request); err == nil || called {
		t.Fatalf("cross-origin request returned err=%v called=%v", err, called)
	}
}

func TestCalDAVWriteTransportLossIsUncertain(t *testing.T) {
	origin, _ := url.Parse("https://calendar.example.test/")
	client := &basicAuthClient{origin: origin, client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("connection refused")
	})}}
	request, _ := http.NewRequest(http.MethodPut, origin.String()+"team/event.ics", strings.NewReader("calendar"))
	if _, err := doCalDAVMutation(client, request); err == nil {
		t.Fatal("pre-send transport failure returned no error")
	} else {
		var uncertain *UncertainError
		if errors.As(err, &uncertain) {
			t.Fatalf("pre-send failure was uncertain: %v", err)
		}
	}
	client.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, fmt.Errorf("connection closed")
	})
	_, err := doCalDAVMutation(client, request)
	var uncertain *UncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("transport loss returned %T %v", err, err)
	}
}

func TestMergeOccurrenceResolvesEmbeddedTimezone(t *testing.T) {
	existing := []byte(`BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTIMEZONE
TZID:Custom/Tallinn
BEGIN:DAYLIGHT
DTSTART:19700329T030000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
TZOFFSETFROM:+0200
TZOFFSETTO:+0300
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:19701025T040000
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
TZOFFSETFROM:+0300
TZOFFSETTO:+0200
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:series
DTSTART;TZID=Custom/Tallinn:20260323T090000
DTEND;TZID=Custom/Tallinn:20260323T100000
RRULE:FREQ=WEEKLY;COUNT=3
SUMMARY:Master
END:VEVENT
BEGIN:VEVENT
UID:series
RECURRENCE-ID;TZID=Custom/Tallinn:20260330T090000
DTSTART;TZID=Custom/Tallinn:20260330T110000
DTEND;TZID=Custom/Tallinn:20260330T120000
SUMMARY:Old override
END:VEVENT
END:VCALENDAR`)
	replacement := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:series\r\nRECURRENCE-ID:20260330T060000Z\r\nDTSTART:20260330T100000Z\r\nDTEND:20260330T110000Z\r\nSUMMARY:New override\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	merged, err := mergeOccurrence(existing, replacement, "2026-03-30T06:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(merged, "Old override") || strings.Count(merged, "RECURRENCE-ID") != 1 || !strings.Contains(merged, "New override") {
		t.Fatalf("override was not replaced:\n%s", merged)
	}
}

func TestFilename(t *testing.T) {
	if got := Filename(model.Event{Title: "Team Planning / Q3"}); got != "team-planning-q3.ics" {
		t.Fatalf("Filename returned %q", got)
	}
}

func TestParseRangeExpandsRecurrenceExceptionsAndOverrides(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:series
DTSTART;TZID=Europe/Tallinn:20260817T090000
DTEND;TZID=Europe/Tallinn:20260817T100000
RRULE:FREQ=DAILY;COUNT=4
EXDATE;TZID=Europe/Tallinn:20260818T090000
SUMMARY:Daily planning
END:VEVENT
BEGIN:VEVENT
UID:series
RECURRENCE-ID;TZID=Europe/Tallinn:20260819T090000
DTSTART;TZID=Europe/Tallinn:20260819T110000
DURATION:PT30M
SUMMARY:Moved planning
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	if events[1].Title != "Moved planning" || events[1].End.Sub(events[1].Start) != 30*time.Minute {
		t.Fatalf("override was %#v", events[1])
	}
	if events[0].ID == events[2].ID || !strings.Contains(events[0].ID, "series#") {
		t.Fatalf("occurrence IDs are not stable: %#v", events)
	}
	for _, event := range events {
		if event.RecurrenceRule != "" || len(event.RecurrenceDates) != 0 || len(event.RecurrencePeriods) != 0 || len(event.ExceptionDates) != 0 {
			t.Fatalf("expanded occurrence retained recurrence generators: %#v", event)
		}
	}
}

func TestParseRangeBoundsDenseRecurrenceExpansion(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:dense
DTSTART:20260815T000000Z
DTEND:20260815T000001Z
RRULE:FREQ=SECONDLY
SUMMARY:Dense series
END:VEVENT
END:VCALENDAR`
	_, err := ParseRange([]byte(data), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(2027, 8, 15, 0, 0, 0, 0, time.UTC))
	if err == nil || !strings.Contains(err.Error(), "exceeds 10000 occurrences") {
		t.Fatalf("dense recurrence returned %v", err)
	}
}

func TestParseRangeFastForwardsOldDenseRecurrence(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:old-dense
DTSTART:20260801T000000Z
DTEND:20260801T000030Z
RRULE:FREQ=MINUTELY
SUMMARY:Old dense series
END:VEVENT
END:VCALENDAR`
	start := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	events, err := ParseRange([]byte(data), start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 60 || !events[0].Start.Equal(start) || !events[59].Start.Equal(start.Add(59*time.Minute)) {
		t.Fatalf("fast-forwarded recurrence returned %d events from %v to %v", len(events), events[0].Start, events[len(events)-1].Start)
	}
}

func TestParseRangeFastForwardsFiniteDenseRecurrence(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:finite-dense
DTSTART:20260801T000000Z
DTEND:20260801T000030Z
RRULE:FREQ=MINUTELY;COUNT=20000;BYSECOND=0
SUMMARY:Finite dense series
END:VEVENT
END:VCALENDAR`
	seriesStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	start := seriesStart.Add(19900 * time.Minute)
	events, err := ParseRange([]byte(data), start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 60 || !events[0].Start.Equal(start) || !events[59].Start.Equal(start.Add(59*time.Minute)) {
		t.Fatalf("finite fast-forward returned %#v", events)
	}
}

func TestParseRangeAppliesThisAndFutureOverride(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:ranged
DTSTART:20260815T090000Z
DTEND:20260815T100000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Original
END:VEVENT
BEGIN:VEVENT
UID:ranged
RECURRENCE-ID;RANGE=THISANDFUTURE:20260816T090000Z
DTSTART:20260816T110000Z
DTEND:20260816T130000Z
SUMMARY:Rescheduled
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].Start.Hour() != 9 {
		t.Fatalf("THISANDFUTURE events = %#v", events)
	}
	for _, event := range events[1:] {
		if event.Start.Hour() != 11 || event.End.Sub(event.Start) != 2*time.Hour || event.Title != "Rescheduled" {
			t.Fatalf("rescheduled occurrence = %#v", event)
		}
	}
	if events[1].RecurrenceRange != "THISANDFUTURE" || events[2].RecurrenceRange != "" {
		t.Fatalf("recurrence range metadata = %#v", events)
	}
}

func TestThisAndFutureShiftWidensCandidateBoundaries(t *testing.T) {
	for _, test := range []struct {
		name          string
		overrideStart string
		rangeStart    time.Time
		rangeEnd      time.Time
		wantHour      int
	}{
		{name: "positive shift across start", overrideStart: "20260816T210000Z", rangeStart: time.Date(2026, 8, 17, 20, 30, 0, 0, time.UTC), rangeEnd: time.Date(2026, 8, 17, 22, 30, 0, 0, time.UTC), wantHour: 21},
		{name: "negative shift across end", overrideStart: "20260816T010000Z", rangeStart: time.Date(2026, 8, 17, 0, 30, 0, 0, time.UTC), rangeEnd: time.Date(2026, 8, 17, 2, 30, 0, 0, time.UTC), wantHour: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := fmt.Sprintf(`BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:boundary
DTSTART:20260815T090000Z
DTEND:20260815T100000Z
RRULE:FREQ=DAILY;COUNT=4
SUMMARY:Original
END:VEVENT
BEGIN:VEVENT
UID:boundary
RECURRENCE-ID;RANGE=THISANDFUTURE:20260816T090000Z
DTSTART:%s
DURATION:PT1H
SUMMARY:Shifted
END:VEVENT
END:VCALENDAR`, test.overrideStart)
			events, err := ParseRange([]byte(data), test.rangeStart, test.rangeEnd)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Start.Hour() != test.wantHour || events[0].Title != "Shifted" {
				t.Fatalf("boundary-shifted events = %#v", events)
			}
		})
	}
}

func TestThisAndFutureIgnoresOverrideOutsideRequestedRange(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:dense-future-override
DTSTART:20200101T000000Z
DTEND:20200101T000030Z
RRULE:FREQ=MINUTELY
SUMMARY:Original
END:VEVENT
BEGIN:VEVENT
UID:dense-future-override
RECURRENCE-ID;RANGE=THISANDFUTURE:20300101T000000Z
DTSTART:20400101T000000Z
DTEND:20400101T000030Z
SUMMARY:Far future
END:VEVENT
END:VCALENDAR`
	start := time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC)
	events, err := ParseRange([]byte(data), start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 60 || !events[0].Start.Equal(start) || !events[59].Start.Equal(start.Add(59*time.Minute)) {
		t.Fatalf("irrelevant future override returned %#v", events)
	}
}

func TestGenerateRejectsInvalidRecurrenceRange(t *testing.T) {
	event := model.Event{ID: "range", Title: "Range", Start: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC), RecurrenceID: "2026-08-15T09:00:00Z", RecurrenceRange: "THISANDFUTURE\r\nATTENDEE:mailto:attacker@example.test"}
	if _, _, err := Generate(event); err == nil || !strings.Contains(err.Error(), "recurrence range") {
		t.Fatalf("Generate accepted invalid recurrence range: %v", err)
	}
}

func TestGenerateRejectsInvalidRecurrenceRule(t *testing.T) {
	base := model.Event{ID: "rule", Title: "Rule", Start: time.Date(2026, 8, 15, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC)}
	for _, rule := range []string{"FREQ=DAILY\r\nATTENDEE:mailto:attacker@example.test", "NOT-A-RRULE"} {
		event := base
		event.RecurrenceRule = rule
		if _, _, err := Generate(event); err == nil || !strings.Contains(err.Error(), "recurrence rule") {
			t.Fatalf("Generate accepted recurrence rule %q: %v", rule, err)
		}
	}
}

func TestParseRejectsDenseTimezoneRecurrence(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTIMEZONE
TZID:Hostile/Dense
BEGIN:STANDARD
DTSTART:19700101T000000
RRULE:FREQ=SECONDLY
TZOFFSETFROM:+0000
TZOFFSETTO:+0000
END:STANDARD
END:VTIMEZONE
END:VCALENDAR`
	_, err := Parse([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "exceeds 10000 transitions") {
		t.Fatalf("dense timezone recurrence returned %v", err)
	}
}

func TestParseRangeKeepsLocalTimeAcrossDST(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:dst-series
DTSTART;TZID=Europe/Tallinn:20260323T090000
DTEND;TZID=Europe/Tallinn:20260323T100000
RRULE:FREQ=WEEKLY;COUNT=3
SUMMARY:DST-safe meeting
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Start.Hour() != 9 || event.Start.Location().String() != "Europe/Tallinn" {
			t.Fatalf("occurrence shifted across DST: %v", event.Start)
		}
	}
	if events[0].Start.UTC().Hour() == events[1].Start.UTC().Hour() {
		t.Fatalf("UTC offset did not change across DST: %v, %v", events[0].Start, events[1].Start)
	}
}

func TestRecurringAllDayEndUsesCalendarDaysAcrossDST(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	event := model.Event{
		AllDay: true,
		Start:  time.Date(2026, 3, 1, 0, 0, 0, 0, location),
		End:    time.Date(2026, 3, 2, 0, 0, 0, 0, location),
	}
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, location)
	end := recurringEnd(event, start)
	if end.Day() != 9 || end.Hour() != 0 || end.Location() != location {
		t.Fatalf("recurring all-day end = %v", end)
	}
}

func TestParseRangeSuppressesCancelledRecurringMaster(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:cancelled-series
DTSTART:20260815T090000Z
DTEND:20260815T100000Z
RRULE:FREQ=DAILY;COUNT=3
STATUS:CANCELLED
SUMMARY:Cancelled series
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC))
	if err != nil || len(events) != 0 {
		t.Fatalf("cancelled series events=%#v err=%v", events, err)
	}
}

func TestParseRangeIncludesOverrideMovedInFromOutsideRange(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VEVENT
UID:moved-series
DTSTART:20260801T090000Z
DTEND:20260801T100000Z
RRULE:FREQ=WEEKLY;COUNT=2
SUMMARY:Original
END:VEVENT
BEGIN:VEVENT
UID:moved-series
RECURRENCE-ID:20260808T090000Z
DTSTART:20260816T110000Z
DTEND:20260816T120000Z
SUMMARY:Moved into range
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Title != "Moved into range" || events[0].RecurrenceID != "2026-08-08T09:00:00Z" {
		t.Fatalf("moved override events=%#v", events)
	}
}

func TestParseRangeUsesEmbeddedTimezone(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTIMEZONE
TZID:Custom/Tallinn
BEGIN:DAYLIGHT
DTSTART:19700329T030000
RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=-1SU
TZOFFSETFROM:+0200
TZOFFSETTO:+0300
TZNAME:EEST
END:DAYLIGHT
BEGIN:STANDARD
DTSTART:19701025T040000
RRULE:FREQ=YEARLY;BYMONTH=10;BYDAY=-1SU
TZOFFSETFROM:+0300
TZOFFSETTO:+0200
TZNAME:EET
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:embedded-zone
DTSTART;TZID=Custom/Tallinn:20260323T090000
DTEND;TZID=Custom/Tallinn:20260323T100000
RRULE:FREQ=WEEKLY;COUNT=3
SUMMARY:Embedded timezone
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 3, 20, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events: %#v", len(events), events)
	}
	for _, event := range events {
		if event.Start.Hour() != 9 || event.Start.Location().String() != "Custom/Tallinn" {
			t.Fatalf("embedded timezone occurrence = %v", event.Start)
		}
	}
}

func TestParseRangePrefersEmbeddedRulesForKnownTimezoneID(t *testing.T) {
	data := `BEGIN:VCALENDAR
VERSION:2.0
BEGIN:VTIMEZONE
TZID:America/New_York
BEGIN:STANDARD
DTSTART:19700101T000000
TZOFFSETFROM:-0500
TZOFFSETTO:-0500
TZNAME:FIXED
END:STANDARD
END:VTIMEZONE
BEGIN:VEVENT
UID:embedded-known-zone
DTSTART;TZID=America/New_York:20260701T090000
DTEND;TZID=America/New_York:20260701T100000
SUMMARY:Embedded rules win
END:VEVENT
END:VCALENDAR`
	events, err := ParseRange([]byte(data), time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC)
	if len(events) != 1 || !events[0].Start.Equal(want) || events[0].Start.Location().String() != "America/New_York" {
		t.Fatalf("embedded known timezone event = %#v, want %v", events, want)
	}
}

func TestWeakETagNormalization(t *testing.T) {
	if got := quoteETag(`W/"abc"`); got != `W/"abc"` {
		t.Fatalf("quoteETag preserved weak tag as %q", got)
	}
	if got := quoteETag(`W/"abc`); got != `W/"abc"` {
		t.Fatalf("quoteETag repaired legacy weak tag as %q", got)
	}
	client := &basicAuthClient{origin: &url.URL{Scheme: "https", Host: "calendar.example.test"}, client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := response(http.StatusOK, "calendar")
		response.Header.Set("ETag", `W/"abc"`)
		return response, nil
	})}}
	_, etag, err := getCalendarObject(context.Background(), client, "https://calendar.example.test/", "/event.ics")
	if err != nil || etag != `W/"abc"` {
		t.Fatalf("getCalendarObject ETag = %q, %v", etag, err)
	}
}

func TestParseAllDayAndGenerateInvitation(t *testing.T) {
	data := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:all-day\r\nDTSTART;VALUE=DATE:20260820\r\nDURATION:P1D\r\nSUMMARY:Offsite\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	events, err := ParseRange([]byte(data), time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !events[0].AllDay || events[0].End.Sub(events[0].Start) != 24*time.Hour {
		t.Fatalf("all-day event: %#v", events)
	}
	_, invitation, err := GenerateInvitation(model.Event{ID: "invite", Title: "Planning", Start: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC), Organizer: "owner@example.test", Attendees: []string{"guest@example.test"}, Sequence: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"METHOD:REQUEST", "SEQUENCE:2", "ORGANIZER:mailto:owner@example.test", "ATTENDEE:mailto:guest@example.test"} {
		if !strings.Contains(invitation, want) {
			t.Fatalf("invitation missing %q:\n%s", want, invitation)
		}
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

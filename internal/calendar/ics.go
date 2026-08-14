package calendar

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
)

const maxFeedBytes = 16 << 20

type Client struct {
	httpClient *http.Client
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{httpClient: httpClient}
}

func (c *Client) List(ctx context.Context, connection model.Connection, start, end time.Time, query string) ([]model.Event, error) {
	if connection.Calendar != nil && connection.Calendar.Kind == "caldav" {
		return c.ListCalDAV(ctx, connection, nil, start, end, query)
	}
	feedURL, err := resolveFeedURL(connection)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create calendar feed request: %w", err)
	}
	request.Header.Set("Accept", "text/calendar")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, safeTransportError("fetch calendar feed", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("fetch calendar feed: %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read calendar feed: %w", err)
	}
	if len(data) > maxFeedBytes {
		return nil, fmt.Errorf("calendar feed exceeds 16 MiB")
	}
	events, err := ParseRange(data, start, end)
	if err != nil {
		return nil, fmt.Errorf("parse calendar feed: %w", err)
	}
	query = strings.ToLower(strings.TrimSpace(query))
	result := make([]model.Event, 0, len(events))
	for _, event := range events {
		if !overlaps(event, start, end) {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(event.Title+" "+event.Description+" "+event.Location), query) {
			continue
		}
		event.ConnectionID = connection.ID
		result = append(result, event)
	}
	slices.SortFunc(result, func(a, b model.Event) int { return a.Start.Compare(b.Start) })
	return result, nil
}

func Parse(data []byte) ([]model.Event, error) {
	calendar, err := ics.ParseCalendar(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse iCalendar: %w", err)
	}
	locations, err := embeddedTimezones(calendar)
	if err != nil {
		return nil, err
	}
	result := make([]model.Event, 0, len(calendar.Events()))
	for _, component := range calendar.Events() {
		event, err := typedEvent(component, locations)
		if err != nil {
			return nil, err
		}
		result = append(result, event)
	}
	return result, nil
}

func ParseRange(data []byte, rangeStart, rangeEnd time.Time) ([]model.Event, error) {
	parsed, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if rangeStart.IsZero() {
		rangeStart = time.Now().Add(-90 * 24 * time.Hour)
	}
	if rangeEnd.IsZero() {
		rangeEnd = time.Now().Add(365 * 24 * time.Hour)
	}
	overrides := make(map[string]model.Event)
	consumedOverrides := make(map[string]bool)
	for _, event := range parsed {
		if event.RecurrenceID != "" {
			overrides[event.ID+"\x00"+event.RecurrenceID] = event
		}
	}
	const maxOccurrences = 10000
	result := make([]model.Event, 0, len(parsed))
	for _, event := range parsed {
		if event.RecurrenceID != "" {
			continue
		}
		if event.RecurrenceRule == "" && len(event.RecurrenceDates) == 0 {
			if !strings.EqualFold(event.Status, "CANCELLED") && overlaps(event, rangeStart, rangeEnd) {
				result = append(result, event)
			}
			continue
		}
		set := &rrule.Set{}
		set.DTStart(event.Start)
		if event.RecurrenceRule != "" {
			rule, err := rrule.StrToRRule("RRULE:" + event.RecurrenceRule)
			if err != nil {
				return nil, fmt.Errorf("parse RRULE for event %s: %w", event.ID, err)
			}
			set.RRule(rule)
		}
		set.SetRDates(append([]time.Time{event.Start}, event.RecurrenceDates...))
		set.SetExDates(event.ExceptionDates)
		starts := set.Between(rangeStart.Add(-event.End.Sub(event.Start)), rangeEnd, true)
		if len(starts) > maxOccurrences-len(result) {
			return nil, fmt.Errorf("calendar recurrence expansion exceeds %d occurrences", maxOccurrences)
		}
		for _, occurrenceStart := range starts {
			recurrenceID := occurrenceStart.UTC().Format(time.RFC3339)
			overrideKey := event.ID + "\x00" + recurrenceID
			if override, ok := overrides[overrideKey]; ok {
				consumedOverrides[overrideKey] = true
				if !strings.EqualFold(override.Status, "CANCELLED") && overlaps(override, rangeStart, rangeEnd) {
					override.ID = stableOccurrenceID(event.ID, occurrenceStart)
					result = append(result, override)
				}
				continue
			}
			occurrence := event
			occurrence.RecurrenceID = recurrenceID
			occurrence.ID = stableOccurrenceID(event.ID, occurrenceStart)
			occurrence.End = occurrenceStart.Add(event.End.Sub(event.Start))
			occurrence.Start = occurrenceStart
			if overlaps(occurrence, rangeStart, rangeEnd) {
				result = append(result, occurrence)
			}
		}
	}
	// An override may move an occurrence whose original start is outside the
	// requested expansion window into the window. Such an occurrence never
	// appears in starts above, so include the unmatched detached event directly.
	for _, override := range parsed {
		if override.RecurrenceID == "" || strings.EqualFold(override.Status, "CANCELLED") {
			continue
		}
		key := override.ID + "\x00" + override.RecurrenceID
		if consumedOverrides[key] || !overlaps(override, rangeStart, rangeEnd) {
			continue
		}
		originalStart, err := time.Parse(time.RFC3339, override.RecurrenceID)
		if err != nil {
			return nil, fmt.Errorf("parse recurrence override for event %s: %w", override.ID, err)
		}
		override.ID = stableOccurrenceID(override.ID, originalStart)
		result = append(result, override)
		if len(result) > maxOccurrences {
			return nil, fmt.Errorf("calendar recurrence expansion exceeds %d occurrences", maxOccurrences)
		}
	}
	slices.SortFunc(result, func(a, b model.Event) int {
		if compared := a.Start.Compare(b.Start); compared != 0 {
			return compared
		}
		return strings.Compare(a.ID, b.ID)
	})
	return result, nil
}

func typedEvent(component *ics.VEvent, locations map[string]*time.Location) (model.Event, error) {
	event := model.Event{ID: component.Id(), SeriesID: component.Id()}
	if event.ID == "" {
		return model.Event{}, fmt.Errorf("VEVENT is missing UID")
	}
	propertyText := func(property ics.ComponentProperty) string {
		if value := component.GetProperty(property); value != nil {
			return ics.FromText(value.Value)
		}
		return ""
	}
	event.Title = propertyText(ics.ComponentPropertySummary)
	event.Description = propertyText(ics.ComponentPropertyDescription)
	event.Location = propertyText(ics.ComponentPropertyLocation)
	event.Status = propertyText(ics.ComponentPropertyStatus)
	startProperty := component.GetProperty(ics.ComponentPropertyDtStart)
	if startProperty == nil {
		return model.Event{}, fmt.Errorf("VEVENT %s is missing DTSTART", event.ID)
	}
	event.AllDay = startProperty.GetValueType() == ics.ValueDataTypeDate
	var err error
	if event.AllDay {
		event.Start, err = component.GetAllDayStartAt()
	} else {
		event.Start, err = componentTime(component, ics.ComponentPropertyDtStart, locations)
	}
	if err != nil {
		return model.Event{}, fmt.Errorf("parse DTSTART for event %s: %w", event.ID, err)
	}
	if component.GetProperty(ics.ComponentPropertyDtEnd) != nil {
		if event.AllDay {
			event.End, err = component.GetAllDayEndAt()
		} else {
			event.End, err = componentTime(component, ics.ComponentPropertyDtEnd, locations)
		}
		if err != nil {
			return model.Event{}, fmt.Errorf("parse DTEND for event %s: %w", event.ID, err)
		}
	} else if duration := component.GetProperty(ics.ComponentPropertyDuration); duration != nil {
		parsedDuration, err := parseDuration(duration.Value)
		if err != nil {
			return model.Event{}, fmt.Errorf("parse DURATION for event %s: %w", event.ID, err)
		}
		event.End = event.Start.Add(parsedDuration)
	} else if event.AllDay {
		event.End = event.Start.Add(24 * time.Hour)
	} else {
		event.End = event.Start
	}
	if organizer := component.GetProperty(ics.ComponentPropertyOrganizer); organizer != nil {
		event.Organizer = trimMailto(organizer.Value)
	}
	for _, attendee := range component.Attendees() {
		event.Attendees = append(event.Attendees, trimMailto(attendee.Value))
	}
	if recurrence := component.GetProperty(ics.ComponentPropertyRrule); recurrence != nil {
		event.RecurrenceRule = recurrence.Value
	}
	if event.RecurrenceDates, err = componentTimes(component, ics.ComponentPropertyRdate, locations); err != nil {
		return model.Event{}, fmt.Errorf("parse RDATE for event %s: %w", event.ID, err)
	}
	if event.ExceptionDates, err = componentTimes(component, ics.ComponentPropertyExdate, locations); err != nil {
		return model.Event{}, fmt.Errorf("parse EXDATE for event %s: %w", event.ID, err)
	}
	if recurrenceID := component.GetProperty(ics.ComponentPropertyRecurrenceId); recurrenceID != nil {
		parsed, err := propertyTime(recurrenceID, locations)
		if err != nil {
			return model.Event{}, fmt.Errorf("parse RECURRENCE-ID for event %s: %w", event.ID, err)
		}
		event.RecurrenceID = parsed.UTC().Format(time.RFC3339)
	}
	if sequence := component.GetProperty(ics.ComponentPropertySequence); sequence != nil {
		event.Sequence, _ = strconv.Atoi(sequence.Value)
	}
	return event, nil
}

func stableOccurrenceID(uid string, start time.Time) string {
	return uid + "#" + start.UTC().Format("20060102T150405Z")
}

func parseDuration(value string) (time.Duration, error) {
	sign := time.Duration(1)
	if strings.HasPrefix(value, "-") {
		sign, value = -1, strings.TrimPrefix(value, "-")
	} else {
		value = strings.TrimPrefix(value, "+")
	}
	if !strings.HasPrefix(value, "P") {
		return 0, fmt.Errorf("invalid RFC5545 duration %q", value)
	}
	value = strings.TrimPrefix(value, "P")
	var days, hours, minutes, seconds int
	_, err := fmt.Sscanf(strings.NewReplacer("T", "", "D", " ", "H", " ", "M", " ", "S", "").Replace(value), "%d %d %d %d", &days, &hours, &minutes, &seconds)
	if err != nil {
		// Accept the common subsets without forcing every unit to exist.
		var total time.Duration
		var number int
		inTime := false
		for _, char := range value {
			if char == 'T' {
				inTime = true
				continue
			}
			if char >= '0' && char <= '9' {
				number = number*10 + int(char-'0')
				continue
			}
			switch char {
			case 'W':
				total += time.Duration(number) * 7 * 24 * time.Hour
			case 'D':
				total += time.Duration(number) * 24 * time.Hour
			case 'H':
				total += time.Duration(number) * time.Hour
			case 'M':
				if !inTime {
					return 0, fmt.Errorf("month durations are not valid for VEVENT")
				}
				total += time.Duration(number) * time.Minute
			case 'S':
				total += time.Duration(number) * time.Second
			default:
				return 0, fmt.Errorf("invalid duration unit %q", char)
			}
			number = 0
		}
		return sign * total, nil
	}
	return sign * (time.Duration(days)*24*time.Hour + time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second), nil
}

func Generate(event model.Event) (model.Event, string, error) {
	if strings.TrimSpace(event.Title) == "" {
		return model.Event{}, "", fmt.Errorf("event title is required")
	}
	if event.Start.IsZero() || event.End.IsZero() || !event.End.After(event.Start) {
		return model.Event{}, "", fmt.Errorf("event end must be after start")
	}
	if event.ID == "" {
		event.ID = "posthouse-" + rand.Text()
	}
	uid := event.SeriesID
	if uid == "" {
		uid = event.ID
	}
	startKey, startValue := formatTime("DTSTART", event.Start, event.AllDay)
	endKey, endValue := formatTime("DTEND", event.End, event.AllDay)
	lines := []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Posthouse//EN", "CALSCALE:GREGORIAN", "BEGIN:VEVENT",
		"UID:" + escape(uid), "DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"), startKey + ":" + startValue, endKey + ":" + endValue,
		"SUMMARY:" + escape(event.Title),
	}
	if event.RecurrenceID != "" {
		if recurrenceTime, err := time.Parse(time.RFC3339, event.RecurrenceID); err == nil {
			lines = append(lines, "RECURRENCE-ID:"+recurrenceTime.UTC().Format("20060102T150405Z"))
		}
	}
	if event.RecurrenceRule != "" {
		lines = append(lines, "RRULE:"+event.RecurrenceRule)
	}
	if len(event.RecurrenceDates) > 0 {
		values := make([]string, len(event.RecurrenceDates))
		for index, value := range event.RecurrenceDates {
			values[index] = value.UTC().Format("20060102T150405Z")
		}
		lines = append(lines, "RDATE:"+strings.Join(values, ","))
	}
	if len(event.ExceptionDates) > 0 {
		values := make([]string, len(event.ExceptionDates))
		for index, value := range event.ExceptionDates {
			values[index] = value.UTC().Format("20060102T150405Z")
		}
		lines = append(lines, "EXDATE:"+strings.Join(values, ","))
	}
	if event.Sequence > 0 {
		lines = append(lines, fmt.Sprintf("SEQUENCE:%d", event.Sequence))
	}
	if event.Status != "" {
		lines = append(lines, "STATUS:"+strings.ToUpper(event.Status))
	}
	if event.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escape(event.Description))
	}
	if event.Location != "" {
		lines = append(lines, "LOCATION:"+escape(event.Location))
	}
	if event.Organizer != "" {
		lines = append(lines, "ORGANIZER:mailto:"+escape(event.Organizer))
	}
	for _, attendee := range event.Attendees {
		lines = append(lines, "ATTENDEE:mailto:"+escape(attendee))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	for index := range lines {
		lines[index] = fold(lines[index])
	}
	return event, strings.Join(lines, "\r\n") + "\r\n", nil
}

func Filename(event model.Event) string {
	base := strings.Map(func(value rune) rune {
		if unicode.IsLetter(value) || unicode.IsDigit(value) || value == '-' || value == '_' {
			return value
		}
		if unicode.IsSpace(value) {
			return '-'
		}
		return -1
	}, strings.ToLower(event.Title))
	for strings.Contains(base, "--") {
		base = strings.ReplaceAll(base, "--", "-")
	}
	base = strings.Trim(base, "-")
	if base == "" {
		base = "event"
	}
	if len([]rune(base)) > 64 {
		base = string([]rune(base)[:64])
	}
	return base + ".ics"
}

func resolveFeedURL(connection model.Connection) (string, error) {
	if connection.Calendar == nil {
		return "", fmt.Errorf("connection %s has no calendar feed", connection.ID)
	}
	feedURL := connection.Calendar.URL
	if connection.Calendar.URLSecret.Env != "" || connection.Calendar.URLSecret.Keychain != "" {
		var err error
		feedURL, err = config.ResolveSecret(connection.Calendar.URLSecret)
		if err != nil {
			return "", err
		}
	} else if connection.Calendar.URLSecretEnv != "" {
		var err error
		feedURL, err = config.Secret(connection.Calendar.URLSecretEnv)
		if err != nil {
			return "", err
		}
	}
	parsed, err := url.Parse(feedURL)
	if err != nil || parsed.Host == "" {
		return "", fmt.Errorf("connection %s has an invalid calendar feed URL", connection.ID)
	}
	if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("connection %s calendar feed URL must use HTTPS", connection.ID)
	}
	return feedURL, nil
}

func overlaps(event model.Event, start, end time.Time) bool {
	eventEnd := event.End
	if eventEnd.IsZero() {
		eventEnd = event.Start
	}
	if !start.IsZero() && !eventEnd.After(start) {
		return false
	}
	if !end.IsZero() && !event.Start.Before(end) {
		return false
	}
	return true
}

func unfold(data string) []string {
	raw := strings.Split(strings.ReplaceAll(data, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += line[1:]
			continue
		}
		lines = append(lines, strings.TrimSuffix(line, "\r"))
	}
	return lines
}

func parseTime(key, value string) (time.Time, bool, error) {
	if strings.Contains(strings.ToUpper(key), "VALUE=DATE") || len(value) == 8 {
		parsed, err := time.Parse("20060102", value)
		return parsed, true, err
	}
	if strings.HasSuffix(value, "Z") {
		parsed, err := time.Parse("20060102T150405Z", value)
		return parsed, false, err
	}
	location := time.Local
	for _, parameter := range strings.Split(key, ";")[1:] {
		name, zone, ok := strings.Cut(parameter, "=")
		if ok && strings.EqualFold(name, "TZID") {
			loaded, err := time.LoadLocation(strings.Trim(zone, `"`))
			if err != nil {
				return time.Time{}, false, fmt.Errorf("load TZID %s: %w", zone, err)
			}
			location = loaded
		}
	}
	parsed, err := time.ParseInLocation("20060102T150405", value, location)
	return parsed, false, err
}

func formatTime(name string, value time.Time, allDay bool) (string, string) {
	if allDay {
		return name + ";VALUE=DATE", value.Format("20060102")
	}
	return name, value.UTC().Format("20060102T150405Z")
}

func fold(line string) string {
	const limit = 75
	if len(line) <= limit {
		return line
	}
	var result strings.Builder
	lineBytes := 0
	for _, value := range line {
		size := len(string(value))
		if lineBytes+size > limit {
			result.WriteString("\r\n ")
			lineBytes = 1
		}
		result.WriteRune(value)
		lineBytes += size
	}
	return result.String()
}

func escape(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\r\n", "\\n", "\n", "\\n", ",", "\\,", ";", "\\;").Replace(value)
}

func unescape(value string) string {
	return strings.NewReplacer("\\n", "\n", "\\N", "\n", "\\,", ",", "\\;", ";", "\\\\", "\\").Replace(value)
}

func trimMailto(value string) string {
	return strings.TrimPrefix(strings.TrimPrefix(value, "mailto:"), "MAILTO:")
}

package calendar

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/posthousehq/posthouse/internal/config"
	"github.com/posthousehq/posthouse/internal/model"
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
		return nil, fmt.Errorf("fetch calendar feed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("fetch calendar feed: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read calendar feed: %w", err)
	}
	if len(data) > maxFeedBytes {
		return nil, fmt.Errorf("calendar feed exceeds 16 MiB")
	}
	events, err := Parse(data)
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
	lines := unfold(string(data))
	var events []model.Event
	var current *model.Event
	for _, line := range lines {
		switch strings.ToUpper(line) {
		case "BEGIN:VEVENT":
			current = &model.Event{}
			continue
		case "END:VEVENT":
			if current == nil {
				continue
			}
			if current.ID == "" || current.Start.IsZero() {
				return nil, fmt.Errorf("VEVENT is missing UID or DTSTART")
			}
			if current.End.IsZero() {
				current.End = current.Start
				if current.AllDay {
					current.End = current.Start.Add(24 * time.Hour)
				}
			}
			events = append(events, *current)
			current = nil
			continue
		}
		if current == nil {
			continue
		}
		nameAndParams, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name := strings.ToUpper(strings.Split(nameAndParams, ";")[0])
		switch name {
		case "UID":
			current.ID = unescape(value)
		case "SUMMARY":
			current.Title = unescape(value)
		case "DESCRIPTION":
			current.Description = unescape(value)
		case "LOCATION":
			current.Location = unescape(value)
		case "DTSTART":
			parsed, allDay, err := parseTime(nameAndParams, value)
			if err != nil {
				return nil, fmt.Errorf("parse DTSTART: %w", err)
			}
			current.Start, current.AllDay = parsed, allDay
		case "DTEND":
			parsed, _, err := parseTime(nameAndParams, value)
			if err != nil {
				return nil, fmt.Errorf("parse DTEND: %w", err)
			}
			current.End = parsed
		case "ATTENDEE":
			current.Attendees = append(current.Attendees, trimMailto(value))
		case "ORGANIZER":
			current.Organizer = trimMailto(value)
		}
	}
	if current != nil {
		return nil, fmt.Errorf("unterminated VEVENT")
	}
	return events, nil
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
	startKey, startValue := formatTime("DTSTART", event.Start, event.AllDay)
	endKey, endValue := formatTime("DTEND", event.End, event.AllDay)
	lines := []string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Posthouse//EN", "CALSCALE:GREGORIAN", "BEGIN:VEVENT",
		"UID:" + escape(event.ID), "DTSTAMP:" + time.Now().UTC().Format("20060102T150405Z"), startKey + ":" + startValue, endKey + ":" + endValue,
		"SUMMARY:" + escape(event.Title),
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
	if connection.Calendar.URLSecretEnv != "" {
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

package calendar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/emersion/go-webdav/caldav"

	"github.com/timborovkov/posthouse/internal/config"
	"github.com/timborovkov/posthouse/internal/model"
)

type ConflictError struct{ Message string }

func (e *ConflictError) Error() string { return e.Message }

// UncertainError reports a mutating request whose response was lost after the
// HTTP client accepted it. Callers must inspect provider state before retrying.
type UncertainError struct{ Err error }

func (e *UncertainError) Error() string { return "CalDAV write outcome is uncertain: " + e.Err.Error() }
func (e *UncertainError) Unwrap() error { return e.Err }

type PartialError struct {
	Errors                  []model.SourceError
	SuccessfulCollections   int
	SuccessfulCollectionIDs []string
}

func (e *PartialError) Error() string {
	return fmt.Sprintf("%d CalDAV collection operations failed", len(e.Errors))
}

type CalDAVDiscovery struct {
	Principal string                     `json:"principal"`
	Home      string                     `json:"home"`
	Calendars []model.CalendarCollection `json:"calendars"`
}

type basicAuthClient struct {
	client   *http.Client
	origin   *url.URL
	username string
	password string
}

func (c *basicAuthClient) Do(request *http.Request) (*http.Response, error) {
	if !sameOrigin(c.origin, request.URL) {
		return nil, fmt.Errorf("refusing to send CalDAV credentials outside the configured origin")
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.SetBasicAuth(c.username, c.password)
	return c.client.Do(clone)
}

func DiscoverCalDAV(ctx context.Context, connection model.Connection) (CalDAVDiscovery, error) {
	client, _, _, err := calDAVClient(connection)
	if err != nil {
		return CalDAVDiscovery{}, err
	}
	principal, err := client.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return CalDAVDiscovery{}, safeTransportError("discover CalDAV principal", err)
	}
	home, err := client.FindCalendarHomeSet(ctx, principal)
	if err != nil {
		return CalDAVDiscovery{}, safeTransportError("discover CalDAV home", err)
	}
	calendars, err := client.FindCalendars(ctx, home)
	if err != nil {
		return CalDAVDiscovery{}, safeTransportError("discover CalDAV calendars", err)
	}
	result := CalDAVDiscovery{Principal: principal, Home: home}
	for _, calendar := range calendars {
		if len(calendar.SupportedComponentSet) > 0 && !containsFold(calendar.SupportedComponentSet, "VEVENT") {
			continue
		}
		result.Calendars = append(result.Calendars, model.CalendarCollection{ID: collectionID(calendar.Path), Name: calendar.Name, Path: calendar.Path})
	}
	return result, nil
}

func (c *Client) ListCalDAV(ctx context.Context, connection model.Connection, collectionIDs []string, start, end time.Time, query string) ([]model.Event, error) {
	client, httpClient, endpoint, err := calDAVClient(connection)
	if err != nil {
		return nil, err
	}
	collections := connection.Calendar.Collections
	if len(collections) == 0 {
		discovery, err := DiscoverCalDAV(ctx, connection)
		if err != nil {
			return nil, err
		}
		collections = discovery.Calendars
	}
	selectedCollections := resolveSelectedCollections(collections, collectionIDs)
	var result []model.Event
	partial := &PartialError{}
	for _, collection := range collections {
		if len(collectionIDs) > 0 && !containsFold(selectedCollections, collection.ID) {
			continue
		}
		objects, err := client.QueryCalendar(ctx, collection.Path, &caldav.CalendarQuery{
			CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
			CompFilter:  caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{Name: "VEVENT", Start: start, End: end}}},
		})
		if err != nil {
			partial.Errors = append(partial.Errors, model.SourceError{ConnectionID: connection.ID, CollectionID: collection.ID, Code: "calendar_collection_unavailable", Message: safeTransportError("query CalDAV collection "+collection.ID, err).Error(), Retryable: true})
			continue
		}
		collectionSucceeded := true
		for _, object := range objects {
			data, etag, err := getCalendarObject(ctx, httpClient, endpoint, object.Path)
			if err != nil {
				collectionSucceeded = false
				partial.Errors = append(partial.Errors, model.SourceError{ConnectionID: connection.ID, CollectionID: collection.ID, Code: "calendar_object_unavailable", Message: err.Error(), Retryable: true})
				continue
			}
			events, err := ParseRange(data, start, end)
			if err != nil {
				collectionSucceeded = false
				partial.Errors = append(partial.Errors, model.SourceError{ConnectionID: connection.ID, CollectionID: collection.ID, Code: "calendar_object_invalid", Message: fmt.Sprintf("parse CalDAV object in collection %s: %v", collection.ID, err), Retryable: false})
				continue
			}
			for _, event := range events {
				if query != "" && !strings.Contains(strings.ToLower(event.Title+" "+event.Description+" "+event.Location), strings.ToLower(strings.TrimSpace(query))) {
					continue
				}
				event.ConnectionID = connection.ID
				event.CollectionID = collection.ID
				event.Href = object.Path
				event.ETag = etag
				result = append(result, event)
			}
		}
		if collectionSucceeded {
			partial.SuccessfulCollections++
			partial.SuccessfulCollectionIDs = append(partial.SuccessfulCollectionIDs, collection.ID)
		}
	}
	if len(partial.Errors) > 0 {
		return result, partial
	}
	return result, nil
}

func resolveSelectedCollections(collections []model.CalendarCollection, selected []string) []string {
	result := make([]string, 0, len(selected))
	for _, value := range selected {
		exactID := false
		for _, collection := range collections {
			if strings.EqualFold(value, collection.ID) {
				result = append(result, collection.ID)
				exactID = true
				break
			}
		}
		if exactID {
			continue
		}
		for _, collection := range collections {
			if strings.EqualFold(value, collection.Name) {
				result = append(result, collection.ID)
			}
		}
	}
	return result
}

func PutCalDAVEvent(ctx context.Context, connection model.Connection, event model.Event, create bool) (model.Event, error) {
	_, httpClient, endpoint, err := calDAVClient(connection)
	if err != nil {
		return model.Event{}, err
	}
	collection, err := exactCollection(connection, event.CollectionID)
	if err != nil {
		return model.Event{}, err
	}
	generated, data, err := Generate(event)
	if err != nil {
		return model.Event{}, err
	}
	href := event.Href
	if href == "" {
		href = path.Join(collection.Path, url.PathEscape(generated.ID)+".ics")
	}
	target, err := resolveCollectionObject(endpoint, collection.Path, href)
	if err != nil {
		return model.Event{}, err
	}
	if !create {
		existing, _, err := getCalendarObject(ctx, httpClient, endpoint, target)
		if err != nil {
			return model.Event{}, err
		}
		data, err = mergeUpdatedEvent(existing, data, event.RecurrenceID)
		if err != nil {
			return model.Event{}, err
		}
	}
	if !create {
		if err := ValidateCalDAVETag(event.ETag); err != nil {
			return model.Event{}, err
		}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, target, strings.NewReader(data))
	if err != nil {
		return model.Event{}, err
	}
	request.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	if create {
		request.Header.Set("If-None-Match", "*")
	} else {
		if event.ETag == "" {
			return model.Event{}, fmt.Errorf("event ETag is required for update")
		}
		request.Header.Set("If-Match", quoteETag(event.ETag))
	}
	response, err := doCalDAVMutation(httpClient, request)
	if err != nil {
		return model.Event{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPreconditionFailed {
		return model.Event{}, &ConflictError{Message: "CalDAV event changed; refresh and prepare the operation again"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return model.Event{}, responseError("write CalDAV event", response)
	}
	generated.ConnectionID = connection.ID
	generated.CollectionID = collection.ID
	generated.Href = href
	generated.ETag = strings.TrimSpace(response.Header.Get("ETag"))
	return generated, nil
}

func DeleteCalDAVEvent(ctx context.Context, connection model.Connection, collectionID, href, etag string) error {
	_, httpClient, endpoint, err := calDAVClient(connection)
	if err != nil {
		return err
	}
	if href == "" || etag == "" {
		return fmt.Errorf("event href and ETag are required for delete")
	}
	if err := ValidateCalDAVETag(etag); err != nil {
		return err
	}
	collection, err := exactCollection(connection, collectionID)
	if err != nil {
		return err
	}
	target, err := resolveCollectionObject(endpoint, collection.Path, href)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("If-Match", quoteETag(etag))
	response, err := doCalDAVMutation(httpClient, request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusPreconditionFailed {
		return &ConflictError{Message: "CalDAV event changed; refresh and prepare the operation again"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError("delete CalDAV event", response)
	}
	return nil
}

func doCalDAVMutation(client *basicAuthClient, request *http.Request) (*http.Response, error) {
	wroteRequest := false
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) {
		wroteRequest = info.Err == nil
	}}
	request = request.WithContext(httptrace.WithClientTrace(request.Context(), trace))
	response, err := client.Do(request)
	if err != nil {
		wrapped := safeTransportError("perform CalDAV mutation", err)
		if wroteRequest {
			return nil, &UncertainError{Err: wrapped}
		}
		return nil, wrapped
	}
	return response, nil
}

func ValidateCalDAVHref(connection model.Connection, collectionID, href string) error {
	collection, err := exactCollection(connection, collectionID)
	if err != nil {
		return err
	}
	if href == "" {
		return nil
	}
	endpoint, err := resolveCalendarURL(connection)
	if err != nil {
		return err
	}
	_, err = resolveCollectionObject(endpoint, collection.Path, href)
	return err
}

func ValidateCalDAVETag(value string) error {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "W/") {
		return fmt.Errorf("CalDAV write requires a strong ETag; refresh after the provider supplies one")
	}
	return nil
}

func GenerateInvitation(event model.Event, cancel bool) (model.Event, string, error) {
	generated, data, err := Generate(event)
	if err != nil {
		return model.Event{}, "", err
	}
	method := "REQUEST"
	if cancel {
		method = "CANCEL"
	}
	lines := unfold(data)
	output := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		output = append(output, line)
		if line == "CALSCALE:GREGORIAN" {
			output = append(output, "METHOD:"+method)
		}
		if line == "BEGIN:VEVENT" {
			if event.Sequence == 0 {
				output = append(output, "SEQUENCE:0")
			}
			if cancel {
				output = append(output, "STATUS:CANCELLED")
			}
		}
	}
	return generated, strings.Join(output, "\r\n") + "\r\n", nil
}

func mergeOccurrence(existing []byte, replacement, recurrenceID string) (string, error) {
	return mergeUpdatedEvent(existing, replacement, recurrenceID)
}

func mergeUpdatedEvent(existing []byte, replacement, recurrenceID string) (string, error) {
	parsedCalendar, err := ics.ParseCalendar(bytes.NewReader(existing))
	if err != nil {
		return "", fmt.Errorf("parse existing CalDAV object: %w", err)
	}
	replacementCalendar, err := ics.ParseCalendar(strings.NewReader(replacement))
	if err != nil {
		return "", fmt.Errorf("parse replacement CalDAV object: %w", err)
	}
	replacementEvents := replacementCalendar.Events()
	if len(replacementEvents) != 1 {
		return "", fmt.Errorf("replacement calendar must contain exactly one VEVENT")
	}
	replacementEvent := replacementEvents[0]
	locations, err := embeddedTimezones(parsedCalendar)
	if err != nil {
		return "", err
	}
	targetRecurrence := ""
	if recurrenceID != "" {
		recurrenceTime, err := time.Parse(time.RFC3339, recurrenceID)
		if err != nil {
			return "", fmt.Errorf("invalid recurrence ID: %w", err)
		}
		targetRecurrence = recurrenceTime.UTC().Format(time.RFC3339)
	}
	for _, current := range parsedCalendar.Events() {
		if current.Id() != replacementEvent.Id() {
			continue
		}
		currentRecurrence, err := eventRecurrenceKey(current, locations)
		if err != nil {
			return "", err
		}
		if currentRecurrence != targetRecurrence {
			continue
		}
		if err := preserveEventTimezone(current, replacementEvent, locations); err != nil {
			return "", err
		}
		mergeModeledEventProperties(current, replacementEvent)
		return parsedCalendar.Serialize(ics.WithNewLine("\r\n")), nil
	}
	if targetRecurrence == "" {
		return "", fmt.Errorf("existing CalDAV object does not contain the event series master")
	}
	parsedCalendar.AddVEvent(replacementEvent)
	return parsedCalendar.Serialize(ics.WithNewLine("\r\n")), nil
}

func preserveEventTimezone(current, replacement *ics.VEvent, locations map[string]*time.Location) error {
	start := current.GetProperty(ics.ComponentPropertyDtStart)
	if start == nil || len(start.ICalParameters["TZID"]) == 0 {
		return nil
	}
	timezone := strings.Trim(start.ICalParameters["TZID"][0], `"`)
	location := locations[timezone]
	if location == nil {
		var err error
		location, err = time.LoadLocation(timezone)
		if err != nil {
			return fmt.Errorf("load existing event TZID %s: %w", timezone, err)
		}
	}
	for _, property := range []ics.ComponentProperty{ics.ComponentPropertyDtStart, ics.ComponentPropertyDtEnd} {
		replacementProperty := replacement.GetProperty(property)
		if replacementProperty == nil || replacementProperty.GetValueType() == ics.ValueDataTypeDate {
			continue
		}
		instant, err := propertyTime(replacementProperty, nil)
		if err != nil {
			return fmt.Errorf("parse replacement %s: %w", property, err)
		}
		parameters := start.ICalParameters
		if currentProperty := current.GetProperty(property); currentProperty != nil && len(currentProperty.ICalParameters["TZID"]) > 0 {
			parameters = currentProperty.ICalParameters
		}
		replacementProperty.Value = instant.In(location).Format("20060102T150405")
		replacementProperty.ICalParameters = cloneParameters(parameters)
	}
	return nil
}

func cloneParameters(parameters map[string][]string) map[string][]string {
	cloned := make(map[string][]string, len(parameters))
	for name, values := range parameters {
		cloned[name] = slices.Clone(values)
	}
	return cloned
}

func eventRecurrenceKey(event *ics.VEvent, locations map[string]*time.Location) (string, error) {
	property := event.GetProperty(ics.ComponentPropertyRecurrenceId)
	if property == nil {
		return "", nil
	}
	value, err := propertyTime(property, locations)
	if err != nil {
		return "", fmt.Errorf("parse existing RECURRENCE-ID: %w", err)
	}
	return value.UTC().Format(time.RFC3339), nil
}

func mergeModeledEventProperties(current, replacement *ics.VEvent) {
	modeled := map[string]struct{}{
		"UID": {}, "DTSTAMP": {}, "DTSTART": {}, "DTEND": {}, "DURATION": {},
		"SUMMARY": {}, "RECURRENCE-ID": {}, "RRULE": {}, "RDATE": {}, "EXDATE": {},
		"SEQUENCE": {}, "STATUS": {}, "DESCRIPTION": {}, "LOCATION": {}, "ORGANIZER": {}, "ATTENDEE": {},
	}
	kept := current.Properties[:0]
	for _, property := range current.Properties {
		if _, replace := modeled[strings.ToUpper(property.IANAToken)]; !replace {
			kept = append(kept, property)
		}
	}
	current.Properties = kept
	for _, property := range replacement.Properties {
		if _, replace := modeled[strings.ToUpper(property.IANAToken)]; replace {
			current.Properties = append(current.Properties, property)
		}
	}
}

func calDAVClient(connection model.Connection) (*caldav.Client, *basicAuthClient, string, error) {
	if connection.Calendar == nil || connection.Calendar.Kind != "caldav" {
		return nil, nil, "", fmt.Errorf("connection %s has no CalDAV capability", connection.ID)
	}
	endpoint, err := resolveCalendarURL(connection)
	if err != nil {
		return nil, nil, "", err
	}
	password := connection.Calendar.ResolvedSecret
	if password == "" {
		password, err = config.ResolveSecret(connection.Calendar.Secret)
	}
	if err != nil {
		return nil, nil, "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, nil, "", fmt.Errorf("connection %s has an invalid CalDAV endpoint", connection.ID)
	}
	loopback := parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
	if parsed.Scheme != "https" && !loopback {
		return nil, nil, "", fmt.Errorf("connection %s CalDAV endpoint must use HTTPS", connection.ID)
	}
	if connection.Calendar.Insecure && !loopback {
		return nil, nil, "", fmt.Errorf("connection %s cannot disable CalDAV TLS verification outside loopback", connection.ID)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if connection.Calendar.Insecure {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} // explicitly configured development endpoint
	}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	httpClient.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		if !sameOrigin(parsed, request.URL) {
			return fmt.Errorf("refusing cross-origin CalDAV redirect")
		}
		return nil
	}
	authClient := &basicAuthClient{client: httpClient, origin: parsed, username: connection.Calendar.Username, password: password}
	client, err := caldav.NewClient(authClient, endpoint)
	if err != nil {
		return nil, nil, "", fmt.Errorf("create CalDAV client: %w", err)
	}
	return client, authClient, endpoint, nil
}

func getCalendarObject(ctx context.Context, client *basicAuthClient, endpoint, href string) ([]byte, string, error) {
	target, err := resolveEndpoint(endpoint, href)
	if err != nil {
		return nil, "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "", err
	}
	request.Header.Set("Accept", "text/calendar")
	response, err := client.Do(request)
	if err != nil {
		return nil, "", safeTransportError("fetch CalDAV object", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", responseError("fetch CalDAV object", response)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxFeedBytes {
		return nil, "", fmt.Errorf("CalDAV object exceeds 16 MiB")
	}
	return data, strings.TrimSpace(response.Header.Get("ETag")), nil
}

func resolveCalendarURL(connection model.Connection) (string, error) {
	if connection.Calendar.ResolvedURL != "" {
		return connection.Calendar.ResolvedURL, nil
	}
	if connection.Calendar.URL != "" {
		return connection.Calendar.URL, nil
	}
	return config.ResolveSecret(connection.Calendar.URLSecret)
}

func exactCollection(connection model.Connection, id string) (model.CalendarCollection, error) {
	for _, collection := range connection.Calendar.Collections {
		if strings.EqualFold(collection.ID, id) {
			if collection.ReadOnly {
				return model.CalendarCollection{}, fmt.Errorf("calendar collection %s is read-only", id)
			}
			return collection, nil
		}
	}
	var named []model.CalendarCollection
	for _, collection := range connection.Calendar.Collections {
		if strings.EqualFold(collection.Name, id) {
			named = append(named, collection)
		}
	}
	if len(named) > 1 {
		return model.CalendarCollection{}, fmt.Errorf("calendar collection name %q is ambiguous; use its stable collection ID", id)
	}
	if len(named) == 1 {
		if named[0].ReadOnly {
			return model.CalendarCollection{}, fmt.Errorf("calendar collection %s is read-only", id)
		}
		return named[0], nil
	}
	return model.CalendarCollection{}, fmt.Errorf("calendar collection %q does not exist; run connection discover", id)
}

func collectionID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:9])
}

func resolveEndpoint(endpoint, href string) (string, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("invalid configured CalDAV endpoint")
	}
	reference, err := url.Parse(href)
	if err != nil {
		return "", fmt.Errorf("invalid CalDAV object href")
	}
	target := base.ResolveReference(reference)
	if !sameOrigin(base, target) {
		return "", fmt.Errorf("CalDAV object href is outside the configured origin")
	}
	target.User = nil
	return target.String(), nil
}

func resolveCollectionObject(endpoint, collectionPath, href string) (string, error) {
	targetText, err := resolveEndpoint(endpoint, href)
	if err != nil {
		return "", err
	}
	base, _ := url.Parse(endpoint)
	collectionReference, err := url.Parse(collectionPath)
	if err != nil {
		return "", fmt.Errorf("invalid configured CalDAV collection path")
	}
	collectionURL := base.ResolveReference(collectionReference)
	target, _ := url.Parse(targetText)
	collectionRoot := strings.TrimSuffix(path.Clean(collectionURL.Path), "/") + "/"
	targetPath := path.Clean(target.Path)
	if !strings.HasPrefix(targetPath, collectionRoot) || targetPath == strings.TrimSuffix(collectionRoot, "/") {
		return "", fmt.Errorf("CalDAV object href is outside collection %s", collectionPath)
	}
	return target.String(), nil
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil || !strings.EqualFold(left.Scheme, right.Scheme) || !strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if strings.EqualFold(value.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(value.Scheme, "http") {
		return "80"
	}
	return ""
}

func safeTransportError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: provider transport failed", operation)
}

func quoteETag(value string) string {
	value = strings.TrimSpace(value)
	weak := strings.HasPrefix(value, "W/")
	if weak {
		value = strings.TrimSpace(strings.TrimPrefix(value, "W/"))
	}
	if !strings.HasPrefix(value, `"`) || !strings.HasSuffix(value, `"`) {
		value = `"` + strings.Trim(value, `"`) + `"`
	}
	if weak {
		return "W/" + value
	}
	return value
}

func responseError(operation string, response *http.Response) error {
	return fmt.Errorf("%s: %s", operation, response.Status)
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

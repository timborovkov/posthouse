package calendar

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	ics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
	"github.com/timborovkov/posthouse/internal/model"
)

func embeddedTimezones(calendar *ics.Calendar) (map[string]*time.Location, error) {
	locations := make(map[string]*time.Location)
	for _, timezone := range calendar.Timezones() {
		property := timezone.GetProperty(ics.ComponentPropertyTzid)
		if property == nil || strings.TrimSpace(property.Value) == "" {
			return nil, fmt.Errorf("VTIMEZONE is missing TZID")
		}
		id := strings.TrimSpace(property.Value)
		if location, err := time.LoadLocation(id); err == nil {
			locations[id] = location
			continue
		}
		location, err := locationFromVTimezone(id, timezone)
		if err != nil {
			return nil, fmt.Errorf("parse embedded timezone %s: %w", id, err)
		}
		locations[id] = location
	}
	return locations, nil
}

func componentTime(component *ics.VEvent, property ics.ComponentProperty, locations map[string]*time.Location) (time.Time, error) {
	value := component.GetProperty(property)
	if value == nil {
		return time.Time{}, fmt.Errorf("property %s is missing", property)
	}
	return propertyTime(value, locations)
}

func componentTimes(component *ics.VEvent, property ics.ComponentProperty, locations map[string]*time.Location) ([]time.Time, error) {
	var result []time.Time
	for _, item := range component.GetProperties(property) {
		for _, value := range strings.Split(item.Value, ",") {
			copy := *item
			copy.Value = strings.SplitN(value, "/", 2)[0]
			parsed, err := propertyTime(&copy, locations)
			if err != nil {
				return nil, err
			}
			result = append(result, parsed)
		}
	}
	return result, nil
}

func componentRecurrences(component *ics.VEvent, locations map[string]*time.Location) ([]time.Time, []model.RecurrencePeriod, error) {
	var dates []time.Time
	var periods []model.RecurrencePeriod
	for _, item := range component.GetProperties(ics.ComponentPropertyRdate) {
		for _, value := range strings.Split(item.Value, ",") {
			parts := strings.SplitN(value, "/", 2)
			startProperty := *item
			startProperty.Value = parts[0]
			start, err := propertyTime(&startProperty, locations)
			if err != nil {
				return nil, nil, err
			}
			if len(parts) == 1 {
				dates = append(dates, start)
				continue
			}
			var end time.Time
			if strings.HasPrefix(parts[1], "P") || strings.HasPrefix(parts[1], "+P") || strings.HasPrefix(parts[1], "-P") {
				duration, err := parseDuration(parts[1])
				if err != nil {
					return nil, nil, err
				}
				end = start.Add(duration)
			} else {
				endProperty := *item
				endProperty.Value = parts[1]
				end, err = propertyTime(&endProperty, locations)
				if err != nil {
					return nil, nil, err
				}
			}
			if !end.After(start) {
				return nil, nil, fmt.Errorf("RDATE period end must be after start")
			}
			periods = append(periods, model.RecurrencePeriod{Start: start, End: end})
		}
	}
	return dates, periods, nil
}

func propertyTime(property *ics.IANAProperty, locations map[string]*time.Location) (time.Time, error) {
	value := strings.TrimSpace(property.Value)
	if strings.HasSuffix(value, "Z") {
		for _, format := range []string{"20060102T150405Z", "20060102Z"} {
			if parsed, err := time.Parse(format, value); err == nil {
				return parsed, nil
			}
		}
	}
	location := time.Local
	if values := property.ICalParameters["TZID"]; len(values) == 1 {
		var ok bool
		location, ok = locations[values[0]]
		if !ok {
			var err error
			location, err = time.LoadLocation(values[0])
			if err != nil {
				return time.Time{}, fmt.Errorf("unknown TZID %q", values[0])
			}
		}
	} else if len(values) > 1 {
		return time.Time{}, fmt.Errorf("expected one TZID")
	}
	formats := []string{"20060102T150405", "20060102"}
	for _, format := range formats {
		if parsed, err := time.ParseInLocation(format, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid iCalendar time %q", value)
}

type zoneTransition struct {
	at         int64
	offsetFrom int
	offsetTo   int
	daylight   bool
	name       string
}

type zoneType struct {
	offset   int
	daylight bool
	name     string
}

func locationFromVTimezone(id string, timezone *ics.VTimezone) (*time.Location, error) {
	var transitions []zoneTransition
	for _, component := range timezone.SubComponents() {
		var base *ics.ComponentBase
		daylight := false
		switch value := component.(type) {
		case *ics.Standard:
			base = &value.ComponentBase
		case *ics.Daylight:
			base, daylight = &value.ComponentBase, true
		default:
			continue
		}
		items, err := observanceTransitions(base, daylight)
		if err != nil {
			return nil, err
		}
		transitions = append(transitions, items...)
	}
	if len(transitions) == 0 {
		return nil, fmt.Errorf("no STANDARD or DAYLIGHT transitions")
	}
	sort.Slice(transitions, func(i, j int) bool { return transitions[i].at < transitions[j].at })
	transitions = deduplicateTransitions(transitions)
	initial := zoneType{offset: transitions[0].offsetFrom, daylight: !transitions[0].daylight, name: "STD"}
	types := []zoneType{initial}
	typeIndexes := make([]byte, len(transitions))
	for index, transition := range transitions {
		kind := zoneType{offset: transition.offsetTo, daylight: transition.daylight, name: transition.name}
		if kind.name == "" {
			if kind.daylight {
				kind.name = "DST"
			} else {
				kind.name = "STD"
			}
		}
		typeIndex := -1
		for candidate := range types {
			if types[candidate] == kind {
				typeIndex = candidate
				break
			}
		}
		if typeIndex < 0 {
			types = append(types, kind)
			typeIndex = len(types) - 1
		}
		if typeIndex > 255 {
			return nil, fmt.Errorf("too many timezone types")
		}
		typeIndexes[index] = byte(typeIndex)
	}
	data, err := buildTZif(transitions, typeIndexes, types)
	if err != nil {
		return nil, err
	}
	return time.LoadLocationFromTZData(id, data)
}

func observanceTransitions(base *ics.ComponentBase, daylight bool) ([]zoneTransition, error) {
	dtstart := propertyValue(base, "DTSTART")
	fromText := propertyValue(base, "TZOFFSETFROM")
	toText := propertyValue(base, "TZOFFSETTO")
	if dtstart == "" || fromText == "" || toText == "" {
		return nil, fmt.Errorf("timezone observance requires DTSTART, TZOFFSETFROM, and TZOFFSETTO")
	}
	start, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(dtstart, "Z"), time.UTC)
	if err != nil {
		return nil, fmt.Errorf("parse timezone DTSTART: %w", err)
	}
	offsetFrom, err := parseUTCOffset(fromText)
	if err != nil {
		return nil, err
	}
	offsetTo, err := parseUTCOffset(toText)
	if err != nil {
		return nil, err
	}
	starts := []time.Time{start}
	if recurrence := propertyValue(base, "RRULE"); recurrence != "" {
		rule, err := rrule.StrToRRule("DTSTART:" + start.Format("20060102T150405") + "\nRRULE:" + recurrence)
		if err != nil {
			return nil, fmt.Errorf("parse timezone RRULE: %w", err)
		}
		starts = rule.Between(time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2101, 1, 1, 0, 0, 0, 0, time.UTC), true)
	}
	for _, property := range base.GetProperties(ics.ComponentPropertyRdate) {
		for _, value := range strings.Split(property.Value, ",") {
			parsed, err := time.ParseInLocation("20060102T150405", strings.TrimSuffix(value, "Z"), time.UTC)
			if err != nil {
				return nil, fmt.Errorf("parse timezone RDATE: %w", err)
			}
			starts = append(starts, parsed)
		}
	}
	name := propertyValue(base, "TZNAME")
	result := make([]zoneTransition, 0, len(starts))
	for _, wall := range starts {
		result = append(result, zoneTransition{at: wall.Unix() - int64(offsetFrom), offsetFrom: offsetFrom, offsetTo: offsetTo, daylight: daylight, name: name})
	}
	return result, nil
}

func propertyValue(base *ics.ComponentBase, name string) string {
	for _, property := range base.Properties {
		if strings.EqualFold(property.IANAToken, name) {
			return strings.TrimSpace(property.Value)
		}
	}
	return ""
}

func parseUTCOffset(value string) (int, error) {
	value = strings.TrimSpace(value)
	sign := 1
	if strings.HasPrefix(value, "-") {
		sign, value = -1, strings.TrimPrefix(value, "-")
	} else {
		value = strings.TrimPrefix(value, "+")
	}
	if len(value) != 4 && len(value) != 6 {
		return 0, fmt.Errorf("invalid timezone offset %q", value)
	}
	hours, err := strconv.Atoi(value[:2])
	if err != nil {
		return 0, fmt.Errorf("invalid timezone offset %q", value)
	}
	minutes, err := strconv.Atoi(value[2:4])
	if err != nil {
		return 0, fmt.Errorf("invalid timezone offset %q", value)
	}
	seconds := 0
	if len(value) == 6 {
		seconds, err = strconv.Atoi(value[4:])
		if err != nil {
			return 0, fmt.Errorf("invalid timezone offset %q", value)
		}
	}
	return sign * (hours*3600 + minutes*60 + seconds), nil
}

func deduplicateTransitions(values []zoneTransition) []zoneTransition {
	result := values[:0]
	for _, value := range values {
		if len(result) > 0 && result[len(result)-1].at == value.at {
			result[len(result)-1] = value
			continue
		}
		result = append(result, value)
	}
	return result
}

func buildTZif(transitions []zoneTransition, indexes []byte, types []zoneType) ([]byte, error) {
	abbreviations := bytes.Buffer{}
	abbreviationIndexes := make([]byte, len(types))
	for index, kind := range types {
		if abbreviations.Len() > 255 {
			return nil, fmt.Errorf("timezone abbreviations are too large")
		}
		abbreviationIndexes[index] = byte(abbreviations.Len())
		abbreviations.WriteString(kind.name)
		abbreviations.WriteByte(0)
	}
	writeHeader := func(buffer *bytes.Buffer, count int) {
		buffer.WriteString("TZif2")
		buffer.Write(make([]byte, 15))
		for _, value := range []int{0, 0, 0, count, len(types), abbreviations.Len()} {
			_ = binary.Write(buffer, binary.BigEndian, int32(value))
		}
	}
	writeTypes := func(buffer *bytes.Buffer) {
		for index, kind := range types {
			_ = binary.Write(buffer, binary.BigEndian, int32(kind.offset))
			if kind.daylight {
				buffer.WriteByte(1)
			} else {
				buffer.WriteByte(0)
			}
			buffer.WriteByte(abbreviationIndexes[index])
		}
		buffer.Write(abbreviations.Bytes())
	}
	var versionOne []zoneTransition
	var versionOneIndexes []byte
	for index, transition := range transitions {
		if transition.at >= -1<<31 && transition.at <= 1<<31-1 {
			versionOne = append(versionOne, transition)
			versionOneIndexes = append(versionOneIndexes, indexes[index])
		}
	}
	var buffer bytes.Buffer
	writeHeader(&buffer, len(versionOne))
	for _, transition := range versionOne {
		_ = binary.Write(&buffer, binary.BigEndian, int32(transition.at))
	}
	buffer.Write(versionOneIndexes)
	writeTypes(&buffer)
	writeHeader(&buffer, len(transitions))
	for _, transition := range transitions {
		_ = binary.Write(&buffer, binary.BigEndian, transition.at)
	}
	buffer.Write(indexes)
	writeTypes(&buffer)
	buffer.WriteString("\n\n")
	return buffer.Bytes(), nil
}

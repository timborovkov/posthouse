package selector

import (
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestMatchIntersectsSelectorFields(t *testing.T) {
	connections := []model.Connection{
		{ID: "acme", Name: "Acme", Category: "work", Labels: []string{"primary", "finance"}, Mail: &model.MailConfig{}},
		{ID: "home", Name: "Home", Category: "personal", Labels: []string{"primary"}, Mail: &model.MailConfig{}},
		{ID: "acme-calendar", Name: "Acme calendar", Category: "work", Labels: []string{"primary"}, Calendar: &model.CalendarConfig{}},
	}

	got, err := Match(connections, model.Selector{Category: "WORK", Labels: []string{"primary"}, Capability: "mail"})
	if err != nil {
		t.Fatalf("Match returned error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "acme" {
		t.Fatalf("Match returned %#v, want acme", got)
	}
}

func TestMatchRejectsEmptyResult(t *testing.T) {
	_, err := Match([]model.Connection{{ID: "home"}}, model.Selector{Category: "work"})
	if err == nil {
		t.Fatal("Match returned nil error for an empty result")
	}
}

func TestMatchIntersectsCalendarCollections(t *testing.T) {
	connections := []model.Connection{
		{ID: "work", Name: "Work", Calendar: &model.CalendarConfig{Kind: "caldav", Collections: []model.CalendarCollection{{ID: "team-id", Name: "Team"}}}},
		{ID: "personal", Name: "Personal", Calendar: &model.CalendarConfig{Kind: "caldav", Collections: []model.CalendarCollection{{ID: "home-id", Name: "Home"}}}},
		{ID: "holidays", Name: "Holidays", Calendar: &model.CalendarConfig{Kind: "feed"}},
	}
	matches, err := Match(connections, model.Selector{Collections: []string{"TEAM"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].ID != "work" {
		t.Fatalf("collection selector returned %#v", matches)
	}
}

func TestMatchGranularCapability(t *testing.T) {
	connections := []model.Connection{
		{ID: "read-only", Calendar: &model.CalendarConfig{}, Capabilities: []string{"calendar.read"}},
		{ID: "writable", Calendar: &model.CalendarConfig{}, Capabilities: []string{"calendar.read", "calendar.write"}},
	}
	got, err := Match(connections, model.Selector{Capability: "calendar.write"})
	if err != nil || len(got) != 1 || got[0].ID != "writable" {
		t.Fatalf("Match returned %#v, %v", got, err)
	}
}

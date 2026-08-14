package selector

import (
	"testing"

	"github.com/posthousehq/posthouse/internal/model"
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

package selector

import (
	"fmt"
	"slices"
	"strings"

	"github.com/posthousehq/posthouse/internal/model"
)

func Match(all []model.Connection, s model.Selector) ([]model.Connection, error) {
	wantedConnections := normalized(s.Connections)
	wantedLabels := normalized(s.Labels)
	category := strings.ToLower(strings.TrimSpace(s.Category))
	capability := strings.ToLower(strings.TrimSpace(s.Capability))

	var matches []model.Connection
	for _, connection := range all {
		if connection.Disabled {
			continue
		}
		if len(wantedConnections) > 0 && !slices.Contains(wantedConnections, strings.ToLower(connection.ID)) && !slices.Contains(wantedConnections, strings.ToLower(connection.Name)) {
			continue
		}
		if category != "" && strings.ToLower(connection.Category) != category {
			continue
		}
		labels := normalized(connection.Labels)
		if !containsAll(labels, wantedLabels) {
			continue
		}
		if capability == "mail" && connection.Mail == nil {
			continue
		}
		if capability == "calendar" && connection.Calendar == nil {
			continue
		}
		matches = append(matches, connection)
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("selector matched no enabled connections")
	}
	return matches, nil
}

func One(all []model.Connection, id string, capability string) (model.Connection, error) {
	matches, err := Match(all, model.Selector{Connections: []string{id}, Capability: capability})
	if err != nil {
		return model.Connection{}, err
	}
	if len(matches) != 1 {
		return model.Connection{}, fmt.Errorf("connection selector %q is ambiguous", id)
	}
	return matches[0], nil
}

func normalized(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func containsAll(values []string, wanted []string) bool {
	for _, value := range wanted {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

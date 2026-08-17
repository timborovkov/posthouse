package policy

import (
	"fmt"
	"os"
	"strings"

	"github.com/timborovkov/posthouse/internal/model"
)

// Operation classes that can be denied. Default is allow all.
const (
	ClassMailSend      = "mail.send"
	ClassMailMove      = "mail.move" // move + archive
	ClassMailMark      = "mail.mark"
	ClassMailTrash     = "mail.trash"
	ClassMailJunk      = "mail.junk"
	ClassMailDraft     = "mail.draft"
	ClassCalendarWrite = "calendar.write"
)

// KnownClasses is the stable set accepted by policy deny/allow.
var KnownClasses = []string{
	ClassMailSend,
	ClassMailMove,
	ClassMailMark,
	ClassMailTrash,
	ClassMailJunk,
	ClassMailDraft,
	ClassCalendarWrite,
}

const (
	MCPProfileFull     = "full"
	MCPProfileReadonly = "readonly"
)

// ClassForKind maps a prepared-operation kind onto a denyable class.
func ClassForKind(kind string) (string, bool) {
	switch kind {
	case "mail.send":
		return ClassMailSend, true
	case "mail.mark":
		return ClassMailMark, true
	case "mail.move", "mail.archive":
		return ClassMailMove, true
	case "mail.trash":
		return ClassMailTrash, true
	case "mail.junk":
		return ClassMailJunk, true
	case "mail.draft.create", "mail.draft.update", "mail.draft.delete":
		return ClassMailDraft, true
	case "calendar.create", "calendar.update", "calendar.delete":
		return ClassCalendarWrite, true
	default:
		return "", false
	}
}

// Normalize returns a copy with unknown classes rejected and duplicates removed.
func Normalize(cfg model.PolicyConfig) (model.PolicyConfig, error) {
	deny := make([]string, 0, len(cfg.Deny))
	seen := map[string]struct{}{}
	for _, item := range cfg.Deny {
		class := strings.TrimSpace(strings.ToLower(item))
		if class == "" {
			continue
		}
		if !IsKnownClass(class) {
			return model.PolicyConfig{}, fmt.Errorf("unknown policy class %q", item)
		}
		if _, ok := seen[class]; ok {
			continue
		}
		seen[class] = struct{}{}
		deny = append(deny, class)
	}
	profile := strings.TrimSpace(strings.ToLower(cfg.MCPProfile))
	switch profile {
	case "", MCPProfileFull, MCPProfileReadonly:
	default:
		return model.PolicyConfig{}, fmt.Errorf("mcp_profile must be %q or %q", MCPProfileFull, MCPProfileReadonly)
	}
	return model.PolicyConfig{Deny: deny, MCPProfile: profile}, nil
}

func IsKnownClass(class string) bool {
	for _, known := range KnownClasses {
		if class == known {
			return true
		}
	}
	return false
}

// EffectiveDeny merges config denials with POSTHOUSE_POLICY_DENY (comma-separated).
func EffectiveDeny(cfg model.PolicyConfig) ([]string, error) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(normalized.Deny))
	for _, class := range normalized.Deny {
		seen[class] = struct{}{}
		result = append(result, class)
	}
	for _, item := range strings.Split(os.Getenv("POSTHOUSE_POLICY_DENY"), ",") {
		class := strings.TrimSpace(strings.ToLower(item))
		if class == "" {
			continue
		}
		if !IsKnownClass(class) {
			return nil, fmt.Errorf("unknown policy class %q in POSTHOUSE_POLICY_DENY", item)
		}
		if _, ok := seen[class]; ok {
			continue
		}
		seen[class] = struct{}{}
		result = append(result, class)
	}
	return result, nil
}

// Allows reports whether the prepared-operation kind is permitted.
func Allows(cfg model.PolicyConfig, kind string) error {
	class, ok := ClassForKind(kind)
	if !ok {
		if strings.HasPrefix(kind, "mail.") || strings.HasPrefix(kind, "calendar.") {
			return fmt.Errorf("policy has no class for write kind %q", kind)
		}
		return nil
	}
	denied, err := EffectiveDeny(cfg)
	if err != nil {
		return err
	}
	for _, item := range denied {
		if item == class {
			return fmt.Errorf("policy denies %s; run posthouse policy allow %s or remove it from policy.deny / POSTHOUSE_POLICY_DENY", class, class)
		}
	}
	return nil
}

// MCPProfile resolves the effective MCP tool profile.
func MCPProfile(cfg model.PolicyConfig, override string) (string, error) {
	override = strings.TrimSpace(strings.ToLower(override))
	switch override {
	case MCPProfileFull, MCPProfileReadonly:
		return override, nil
	case "":
	default:
		return "", fmt.Errorf("mcp profile must be %q or %q", MCPProfileFull, MCPProfileReadonly)
	}
	normalized, err := Normalize(cfg)
	if err != nil {
		return "", err
	}
	switch normalized.MCPProfile {
	case MCPProfileFull, MCPProfileReadonly:
		return normalized.MCPProfile, nil
	}
	if env := strings.TrimSpace(strings.ToLower(os.Getenv("POSTHOUSE_MCP_PROFILE"))); env != "" {
		switch env {
		case MCPProfileFull, MCPProfileReadonly:
			return env, nil
		default:
			return "", fmt.Errorf("POSTHOUSE_MCP_PROFILE must be %q or %q", MCPProfileFull, MCPProfileReadonly)
		}
	}
	return MCPProfileFull, nil
}

// Status is the public policy view for CLI/MCP.
type Status struct {
	Deny       []string `json:"deny"`
	MCPProfile string   `json:"mcp_profile"`
	Classes    []string `json:"classes"`
}

func StatusFrom(cfg model.PolicyConfig) (Status, error) {
	deny, err := EffectiveDeny(cfg)
	if err != nil {
		return Status{}, err
	}
	profile, err := MCPProfile(cfg, "")
	if err != nil {
		return Status{}, err
	}
	return Status{Deny: deny, MCPProfile: profile, Classes: append([]string(nil), KnownClasses...)}, nil
}

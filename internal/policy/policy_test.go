package policy

import (
	"strings"
	"testing"

	"github.com/timborovkov/posthouse/internal/model"
)

func TestNormalizeRejectsUnknownClassAndProfile(t *testing.T) {
	if _, err := Normalize(model.PolicyConfig{Deny: []string{"mail.delete"}}); err == nil || !strings.Contains(err.Error(), "unknown policy class") {
		t.Fatalf("Normalize unknown class error = %v", err)
	}
	if _, err := Normalize(model.PolicyConfig{MCPProfile: "agent"}); err == nil || !strings.Contains(err.Error(), "mcp_profile") {
		t.Fatalf("Normalize unknown profile error = %v", err)
	}
}

func TestNormalizeDedupesAndClearsFullProfile(t *testing.T) {
	got, err := Normalize(model.PolicyConfig{Deny: []string{"mail.send", "MAIL.SEND", " mail.move "}, MCPProfile: "full"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Deny) != 2 || got.Deny[0] != ClassMailSend || got.Deny[1] != ClassMailMove {
		t.Fatalf("Deny = %#v", got.Deny)
	}
	if got.MCPProfile != "" {
		t.Fatalf("MCPProfile = %q, want empty", got.MCPProfile)
	}
}

func TestEffectiveDenyMergesEnv(t *testing.T) {
	t.Setenv("POSTHOUSE_POLICY_DENY", "mail.trash, mail.send")
	deny, err := EffectiveDeny(model.PolicyConfig{Deny: []string{ClassMailSend, ClassMailMove}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ClassMailSend, ClassMailMove, ClassMailTrash}
	if len(deny) != len(want) {
		t.Fatalf("EffectiveDeny = %#v", deny)
	}
	for i := range want {
		if deny[i] != want[i] {
			t.Fatalf("EffectiveDeny = %#v, want %#v", deny, want)
		}
	}
}

func TestAllowsDeniesMappedKinds(t *testing.T) {
	cfg := model.PolicyConfig{Deny: []string{ClassMailMove, ClassMailSend}}
	if err := Allows(cfg, "mail.archive"); err == nil || !strings.Contains(err.Error(), ClassMailMove) {
		t.Fatalf("Allows archive error = %v", err)
	}
	if err := Allows(cfg, "mail.send"); err == nil {
		t.Fatal("Allows send unexpectedly succeeded")
	}
	if err := Allows(cfg, "mail.mark"); err != nil {
		t.Fatalf("Allows mark = %v", err)
	}
	if err := Allows(cfg, "unknown.kind"); err != nil {
		t.Fatalf("Allows unknown = %v", err)
	}
}

func TestMCPProfileResolutionOrder(t *testing.T) {
	t.Setenv("POSTHOUSE_MCP_PROFILE", "readonly")
	profile, err := MCPProfile(model.PolicyConfig{}, "")
	if err != nil || profile != MCPProfileReadonly {
		t.Fatalf("env profile = %q, %v", profile, err)
	}
	profile, err = MCPProfile(model.PolicyConfig{MCPProfile: MCPProfileReadonly}, MCPProfileFull)
	if err != nil || profile != MCPProfileFull {
		t.Fatalf("override profile = %q, %v", profile, err)
	}
	t.Setenv("POSTHOUSE_MCP_PROFILE", "")
	profile, err = MCPProfile(model.PolicyConfig{}, "")
	if err != nil || profile != MCPProfileFull {
		t.Fatalf("default profile = %q, %v", profile, err)
	}
}

func TestClassForKind(t *testing.T) {
	cases := map[string]string{
		"mail.send":         ClassMailSend,
		"mail.move":         ClassMailMove,
		"mail.archive":      ClassMailMove,
		"mail.mark":         ClassMailMark,
		"mail.trash":        ClassMailTrash,
		"mail.junk":         ClassMailJunk,
		"mail.draft.create": ClassMailDraft,
		"calendar.create":   ClassCalendarWrite,
	}
	for kind, want := range cases {
		got, ok := ClassForKind(kind)
		if !ok || got != want {
			t.Fatalf("ClassForKind(%q) = %q, %v", kind, got, ok)
		}
	}
}

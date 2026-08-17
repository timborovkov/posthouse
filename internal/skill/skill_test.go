package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/timborovkov/posthouse/skills"
)

func TestListAndInstallSelectedSkills(t *testing.T) {
	listed, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(skills.IDs) {
		t.Fatalf("skills = %#v", listed)
	}
	for _, info := range listed {
		if info.Description == "" || !strings.HasPrefix(info.Name, "posthouse-") {
			t.Fatalf("skill %#v", info)
		}
		if utf8.RuneCountInString(info.Description) > 300 {
			t.Fatalf("description too long for %s (%d runes)", info.ID, utf8.RuneCountInString(info.Description))
		}
	}

	dir := t.TempDir()
	result, err := Install(dir, []string{"mail", "posthouse-rest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 2 {
		t.Fatalf("installed = %#v", result)
	}
	body, err := os.ReadFile(filepath.Join(dir, "posthouse-mail", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "prepared operation") {
		t.Fatalf("mail skill body = %s", body)
	}
	if !strings.Contains(string(body), `"text":`) {
		t.Fatalf("mail skill missing draft.json example: %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "posthouse-mcp", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("mcp skill should not have been installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "posthouse-calendar", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("calendar skill should not have been installed: %v", err)
	}

	if _, err := Install(dir, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("unknown skill error = %v", err)
	}
}

func TestInstallPrunesRetiredSkills(t *testing.T) {
	dir := t.TempDir()
	for _, id := range retiredIDs {
		legacy := filepath.Join(dir, "posthouse-"+id)
		if err := os.MkdirAll(legacy, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(legacy, "SKILL.md"), []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := Install(dir, []string{"connections"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != len(retiredIDs) {
		t.Fatalf("removed = %#v", result.Removed)
	}
	for _, id := range retiredIDs {
		if _, err := os.Stat(filepath.Join(dir, "posthouse-"+id)); !os.IsNotExist(err) {
			t.Fatalf("retired skill %s still present: %v", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "posthouse-connections", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
}

func TestInstallAllAndAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	result, err := Install(dir, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != len(skills.IDs) {
		t.Fatalf("all installed = %#v", result)
	}
	for _, id := range skills.IDs {
		if _, err := os.Stat(filepath.Join(dir, "posthouse-"+id, "SKILL.md")); err != nil {
			t.Fatalf("missing installed skill %s: %v", id, err)
		}
	}
	path, err := AgentDirectory("claude")
	if err != nil || !strings.Contains(path, string(filepath.Separator)+".claude"+string(filepath.Separator)+"skills") {
		t.Fatalf("claude dir = %q, %v", path, err)
	}
	codexDirs, err := AgentDirectories("codex")
	if err != nil || len(codexDirs) != 2 {
		t.Fatalf("codex dirs = %#v, %v", codexDirs, err)
	}
	if !strings.Contains(codexDirs[0], string(filepath.Separator)+".agents"+string(filepath.Separator)+"skills") {
		t.Fatalf("codex primary = %q", codexDirs[0])
	}
	if !strings.Contains(codexDirs[1], string(filepath.Separator)+".codex"+string(filepath.Separator)+"skills") {
		t.Fatalf("codex legacy = %q", codexDirs[1])
	}
	codex, err := AgentDirectory("codex")
	if err != nil || codex != codexDirs[0] {
		t.Fatalf("codex dir = %q, %v", codex, err)
	}
	if _, err := AgentDirectory("unknown"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

func TestInstallAllWritesEveryDestination(t *testing.T) {
	primary := t.TempDir()
	legacy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(legacy, "posthouse-cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "posthouse-cli", "SKILL.md"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := InstallAll([]string{primary, legacy}, []string{"mail"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Installed) != 2 {
		t.Fatalf("installed = %#v", result.Installed)
	}
	if len(result.Removed) != 1 || !strings.Contains(result.Removed[0], "posthouse-cli") {
		t.Fatalf("removed = %#v", result.Removed)
	}
	for _, dir := range []string{primary, legacy} {
		if _, err := os.Stat(filepath.Join(dir, "posthouse-mail", "SKILL.md")); err != nil {
			t.Fatalf("missing mail in %s: %v", dir, err)
		}
	}
}

func TestSkillFrontmatterNamesMatchInstallDirs(t *testing.T) {
	for _, id := range skills.IDs {
		body, err := skills.FS.ReadFile(id + "/SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		want := "posthouse-" + id
		var name string
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "name:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "name:"))
				break
			}
		}
		if name != want {
			t.Fatalf("skill %s name = %q, want %q", id, name, want)
		}
	}
	calendar, err := skills.FS.ReadFile("calendar/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calendar), "collection_id") {
		t.Fatal("calendar skill must document collection_id")
	}
	if !strings.Contains(string(calendar), `"href"`) || !strings.Contains(string(calendar), `"etag"`) {
		t.Fatal("calendar skill must document update identity fields")
	}
	if strings.Contains(string(calendar), `"collection"`) {
		t.Fatal("calendar skill must not use ambiguous collection field")
	}
}

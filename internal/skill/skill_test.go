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
	installed, err := Install(dir, []string{"mail", "posthouse-rest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 {
		t.Fatalf("installed = %#v", installed)
	}
	body, err := os.ReadFile(filepath.Join(dir, "posthouse-mail", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "prepared operation") {
		t.Fatalf("mail skill body = %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "posthouse-mcp", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("mcp skill should not have been installed: %v", err)
	}
	for _, legacy := range []string{"cli", "email-inboxes", "email-send"} {
		if _, err := os.Stat(filepath.Join(dir, "posthouse-"+legacy, "SKILL.md")); !os.IsNotExist(err) {
			t.Fatalf("legacy skill %s should not exist: %v", legacy, err)
		}
	}

	if _, err := Install(dir, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown skill") {
		t.Fatalf("unknown skill error = %v", err)
	}
}

func TestInstallAllAndAgentDirectory(t *testing.T) {
	dir := t.TempDir()
	installed, err := Install(dir, []string{"all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != len(skills.IDs) {
		t.Fatalf("all installed = %#v", installed)
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
	if _, err := AgentDirectory("unknown"); err == nil {
		t.Fatal("expected unknown agent error")
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
}

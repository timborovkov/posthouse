package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListAndInstallSelectedSkills(t *testing.T) {
	listed, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 3 {
		t.Fatalf("skills = %#v", listed)
	}
	for _, info := range listed {
		if info.Description == "" || !strings.HasPrefix(info.Name, "posthouse-") {
			t.Fatalf("skill %#v", info)
		}
	}

	dir := t.TempDir()
	installed, err := Install(dir, []string{"cli", "posthouse-rest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 {
		t.Fatalf("installed = %#v", installed)
	}
	body, err := os.ReadFile(filepath.Join(dir, "posthouse-cli", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Prepared operation") {
		t.Fatalf("cli skill body = %s", body)
	}
	if _, err := os.Stat(filepath.Join(dir, "posthouse-mcp", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("mcp skill should not have been installed: %v", err)
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
	if len(installed) != 3 {
		t.Fatalf("all installed = %#v", installed)
	}
	path, err := AgentDirectory("claude")
	if err != nil || !strings.Contains(path, string(filepath.Separator)+".claude"+string(filepath.Separator)+"skills") {
		t.Fatalf("claude dir = %q, %v", path, err)
	}
	if _, err := AgentDirectory("unknown"); err == nil {
		t.Fatal("expected unknown agent error")
	}
}

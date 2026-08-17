package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/timborovkov/posthouse/skills"
)

// retiredIDs are previous skill folder names removed from the catalog. Install
// deletes them from the destination so agents do not keep stale instructions.
var retiredIDs = []string{"cli", "email-inboxes", "email-send"}

type Info struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type InstallResult struct {
	Installed []string `json:"installed"`
	Removed   []string `json:"removed,omitempty"`
}

func List() ([]Info, error) {
	result := make([]Info, 0, len(skills.IDs))
	for _, id := range skills.IDs {
		body, err := skills.FS.ReadFile(id + "/SKILL.md")
		if err != nil {
			return nil, err
		}
		result = append(result, Info{ID: id, Name: "posthouse-" + id, Description: skillDescription(string(body))})
	}
	return result, nil
}

func Install(destination string, ids []string) (InstallResult, error) {
	var result InstallResult
	if destination == "" {
		return result, fmt.Errorf("skill install requires a destination directory")
	}
	if len(ids) == 0 {
		return result, fmt.Errorf("select at least one skill: %s, or --all", strings.Join(skills.IDs, ", "))
	}
	selected, err := normalizeIDs(ids)
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return result, fmt.Errorf("create skill directory: %w", err)
	}
	result.Installed = make([]string, 0, len(selected))
	for _, id := range selected {
		body, err := skills.FS.ReadFile(id + "/SKILL.md")
		if err != nil {
			return result, err
		}
		dir := filepath.Join(destination, "posthouse-"+id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return result, err
		}
		path := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return result, fmt.Errorf("write %s: %w", path, err)
		}
		result.Installed = append(result.Installed, path)
	}
	removed, err := pruneRetired(destination)
	if err != nil {
		return result, err
	}
	result.Removed = removed
	return result, nil
}

func pruneRetired(destination string) ([]string, error) {
	var removed []string
	for _, id := range retiredIDs {
		dir := filepath.Join(destination, "posthouse-"+id)
		info, err := os.Stat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, fmt.Errorf("stat retired skill %s: %w", dir, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return removed, fmt.Errorf("remove retired skill %s: %w", dir, err)
		}
		removed = append(removed, dir)
	}
	return removed, nil
}

func AgentDirectory(agent string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "claude":
		return filepath.Join(home, ".claude", "skills"), nil
	case "cursor":
		return filepath.Join(home, ".cursor", "skills"), nil
	case "codex":
		// Current Codex discovery path (USER scope). Older builds also scanned
		// ~/.codex/skills; pass --dir there if needed.
		return filepath.Join(home, ".agents", "skills"), nil
	case "hermes":
		return filepath.Join(home, ".hermes", "skills"), nil
	case "":
		return "", fmt.Errorf("choose --agent claude|cursor|codex|hermes or pass --dir")
	default:
		return "", fmt.Errorf("unknown agent %q; use claude, cursor, codex, hermes, or --dir", agent)
	}
}

func normalizeIDs(ids []string) ([]string, error) {
	seen := map[string]bool{}
	var selected []string
	for _, id := range ids {
		id = strings.ToLower(strings.TrimSpace(id))
		id = strings.TrimPrefix(id, "posthouse-")
		if id == "all" {
			return append([]string{}, skills.IDs...), nil
		}
		ok := false
		for _, known := range skills.IDs {
			if id == known {
				ok = true
				break
			}
		}
		if !ok {
			return nil, fmt.Errorf("unknown skill %q; available: %s", id, strings.Join(skills.IDs, ", "))
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		selected = append(selected, id)
	}
	return selected, nil
}

func skillDescription(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "description:"))
		}
	}
	return ""
}

func SkillFS() fs.FS {
	return skills.FS
}

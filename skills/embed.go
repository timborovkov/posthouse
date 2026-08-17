package skills

import "embed"

// FS holds the shipped agent skill files. Install copies selected skills onto
// a local agent skill directory; Posthouse does not phone home.
//
//go:embed connections/SKILL.md email-inboxes/SKILL.md email-send/SKILL.md calendar/SKILL.md rest/SKILL.md mcp/SKILL.md
var FS embed.FS

// IDs are the selectable skill names for `posthouse skill install`.
var IDs = []string{
	"connections",
	"email-inboxes",
	"email-send",
	"calendar",
	"rest",
	"mcp",
}

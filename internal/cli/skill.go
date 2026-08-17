package cli

import (
	"flag"
	"fmt"

	"github.com/timborovkov/posthouse/internal/skill"
)

func (c *CLI) skill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: posthouse skill <list|install>")
	}
	switch args[0] {
	case "list":
		listed, err := skill.List()
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{"skills": listed})
	case "install":
		flags := flag.NewFlagSet("skill install", flag.ContinueOnError)
		flags.SetOutput(c.stderr)
		dir := flags.String("dir", "", "destination skill directory")
		agent := flags.String("agent", "", "install into a known agent skill directory: claude, cursor, codex, or hermes")
		all := flags.Bool("all", false, "install every shipped skill, including mcp and rest")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		destination := *dir
		if destination == "" && *agent != "" {
			resolved, err := skill.AgentDirectory(*agent)
			if err != nil {
				return err
			}
			destination = resolved
		}
		selected := flags.Args()
		if *all {
			selected = append(selected, "all")
		}
		result, err := skill.Install(destination, selected)
		if err != nil {
			return err
		}
		return writeJSON(c.stdout, map[string]any{
			"ok":        true,
			"directory": destination,
			"installed": result.Installed,
			"removed":   result.Removed,
		})
	default:
		return fmt.Errorf("unknown skill command %q", args[0])
	}
}

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// discoverSkills finds the operator's skills (ADR-0010/0011): Claude
// Code's format from gem-agent's own global directory plus the shared
// project one. ~/.claude is never read — that is Claude Code's live
// environment, and inheriting it implicitly would couple the fallback's
// behaviour to the primary's (the operator shares individual skills, or
// everything, with a symlink instead).
func discoverSkills(projectDir string) ([]skills.Skill, []string) {
	global := ""
	if home, err := os.UserHomeDir(); err == nil {
		global = filepath.Join(home, ".config", "gem-agent", "skills")
	}
	return skills.Discover(global, projectDir, skills.DefaultLimits())
}

// registerSkillTool adds load_skill to the registry. Read-only and
// ungated: it can only read inside discovered skill directories, which
// is also what bounds the agent's unwrap exemption for its results.
func registerSkillTool(registry *tools.Registry, list []skills.Skill) error {
	if len(list) == 0 {
		return nil
	}
	return registry.Register(&tools.Tool{
		Name: skills.ToolName,
		Description: "Load a skill installed by the user. With only `name`, returns the skill's " +
			"instructions (SKILL.md); with `file`, returns a supporting file from that skill's own " +
			"directory (e.g. references/guide.md, scripts/run.py — a path to a directory lists it). " +
			"The available skills and when to use them are listed in the system prompt. " +
			"Returned skill content is the user's own instructions for the task.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{"type": "string", "description": "skill name, exactly as listed"},
				"file": map[string]any{"type": "string", "description": "optional path relative to the skill's directory"},
			},
			"required": []string{"name"},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			s, ok := skills.Find(list, name)
			if !ok {
				return "", fmt.Errorf("unknown skill %q — only the skills listed in the system prompt exist", name)
			}
			if file, _ := args["file"].(string); file != "" {
				return s.File(file, skills.DefaultLimits())
			}
			body, err := s.Body(skills.DefaultLimits())
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Skill %q (%s scope) — the user's instructions for this kind of task:\n\n%s",
				s.Name, s.Scope, body), nil
		},
	})
}

// expandSkillInput turns "/skill <name> [args]" into the text of a turn,
// with the body injected directly — the operator already decided, so no
// model round is spent asking for it (ADR-0010 §2). handled reports
// whether the input was a /skill invocation at all; errMsg is
// operator-facing and means "handled, but nothing to run".
func expandSkillInput(input string, list []skills.Skill) (turn string, handled bool, errMsg string) {
	if input != "/skill" && !strings.HasPrefix(input, "/skill ") {
		return "", false, ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(input, "/skill"))
	if rest == "" {
		return "", true, "usage: /skill <name> [arguments] — /skills lists what is installed"
	}
	name, args, _ := strings.Cut(rest, " ")
	s, ok := skills.Find(list, name)
	if !ok {
		return "", true, fmt.Sprintf("unknown skill %q — /skills lists what is installed", name)
	}
	body, err := s.Body(skills.DefaultLimits())
	if err != nil {
		return "", true, "could not load the skill: " + err.Error()
	}
	args = strings.TrimSpace(args)
	argLine := "(none)"
	if args != "" {
		argLine = args
	}
	return fmt.Sprintf(`I am invoking the skill %q with arguments: %s

Follow the skill's instructions below for this task. Supporting files under the skill's directory are available via the %s tool (%s(%q, "<relative path>")).

--- %s / SKILL.md ---

%s`,
		s.Name, argLine, skills.ToolName, skills.ToolName, s.Name, s.Name, body), true, ""
}

// skillsListing renders /skills output.
func skillsListing(list []skills.Skill) string {
	if len(list) == 0 {
		return "no skills installed — gem-agent reads Claude Code's skill format from:\n" +
			"  ~/.config/gem-agent/skills/<name>/SKILL.md  (global: gem-agent's own, every project)\n" +
			"  <project>/.claude/skills/<name>/SKILL.md    (project: shared with Claude Code)\n" +
			"skills-series zips unpack straight into the global directory. To share\n" +
			"skills already installed for Claude Code, link them in — per skill or all:\n" +
			"  ln -s ~/.claude/skills/<name> ~/.config/gem-agent/skills/<name>\n" +
			"  ln -s ~/.claude/skills ~/.config/gem-agent/skills\n"
	}
	var b strings.Builder
	for _, s := range list {
		fmt.Fprintf(&b, "  %-24s [%s] %s\n", s.Name, s.Scope, clipRunes(s.Description, 90))
		if s.ArgumentHint != "" {
			fmt.Fprintf(&b, "  %-24s   usage: /skill %s %s\n", "", s.Name, s.ArgumentHint)
		}
	}
	b.WriteString("invoke with /skill <name> [arguments]; the model can also load them itself when the task matches\n")
	return b.String()
}

// skillBannerLine summarises discovery for the startup banner.
func skillBannerLine(list []skills.Skill) string {
	if len(list) == 0 {
		return ""
	}
	names := make([]string, 0, len(list))
	for _, s := range list {
		n := s.Name
		if s.Scope == "project" {
			n += " [project]"
		}
		names = append(names, n)
	}
	if len(names) > 8 {
		names = append(names[:8], fmt.Sprintf("… +%d more", len(list)-8))
	}
	return "skills: " + strings.Join(names, ", ")
}

func clipRunes(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

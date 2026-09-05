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
func discoverSkills(projectDir string, grant projectGrant) ([]skills.Skill, []string) {
	global := ""
	if home, err := os.UserHomeDir(); err == nil {
		global = filepath.Join(home, ".config", "gem-agent", "skills")
	}
	// Skill bodies load as the operator's own instructions (ADR-0010),
	// so an untrusted project contributes none (ADR-0023 §2).
	if !grant.trusted {
		projectDir = ""
	}
	list, notes := skills.Discover(global, projectDir, skills.DefaultLimits())
	// A project skill whose content changed since it was trusted stays
	// out until re-trusted (ADR-0074); its root is released. The
	// operator's own global skill of the same name, which the project
	// one had overridden, comes back in its place: project content that
	// changed must not switch off a skill the operator wrote
	// (verification G).
	kept := list[:0]
	shadowed := map[string]bool{}
	for _, s := range list {
		if s.Scope == "project" && !grant.skill(s.Entry) {
			shadowed[s.Name] = true
			s.Close()
			continue
		}
		kept = append(kept, s)
	}
	if len(shadowed) > 0 && global != "" {
		globals, _ := skills.Discover(global, "", skills.DefaultLimits())
		for _, g := range globals {
			if shadowed[g.Name] {
				kept = append(kept, g)
				notes = append(notes, fmt.Sprintf("skill %q: the project version is not loaded (changed since trusted); your global one is", g.Name))
				continue
			}
			g.Close()
		}
	}
	return kept, notes
}

// registerSkillTool adds load_skill to the registry. Read-only and
// ungated: it can only read inside discovered skill directories, which
// is also what bounds the agent's unwrap exemption for its results.
// The skill list is read through the getter on every call (ADR-0039):
// /skills reload swaps the list, and the tool is registered even when
// the session starts with zero skills, so a reload can populate it.
func registerSkillTool(registry *tools.Registry, get func() []skills.Skill) error {
	return registry.Register(&tools.Tool{
		Name: skills.ToolName,
		Description: "Load a skill installed by the user. With only `name`, returns the skill's " +
			"instructions (SKILL.md); with `file`, returns a supporting file from that skill's own " +
			"directory (e.g. references/guide.md, scripts/run.py — a path to a directory lists it). " +
			"The available skills and when to use them are listed in the system prompt. " +
			"Returned skill content is the user's own instructions for the task. The result opens with " +
			"`Base directory for this skill: <dir>` — run the skill's scripts through shell_exec from there.",
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
			s, ok := skills.Find(get(), name)
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
			// The base-directory line is Claude Code's, verbatim
			// (ADR-0070 §1): skills are written against that sentence
			// (`SKILL_DIR/scripts/…`), and without it a global skill's
			// scripts are reachable by no path the model knows.
			return fmt.Sprintf("Skill %q (%s scope) — the user's instructions for this kind of task.\n%s\n\n%s",
				s.Name, s.Scope, skills.BaseDirLine(s), body), nil
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
%s

--- %s / SKILL.md ---

%s`,
		s.Name, argLine, skills.ToolName, skills.ToolName, s.Name, skills.BaseDirLine(s), s.Name, body), true, ""
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

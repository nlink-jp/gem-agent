// Package skills discovers and loads Claude Code skills (ADR-0010):
// directories carrying a SKILL.md with YAML frontmatter, installed under
// ~/.claude/skills/ or <project>/.claude/skills/. The format and the
// locations are Claude Code's, read as-is — the same drop-in rule as
// AGENTS.md and .mcp.json, and the reason the operator's existing skill
// library works here unmodified.
//
// Skills are progressive disclosure: one description line per skill sits
// in the system prompt, and the body loads only when used. Loaded
// content is instruction-grade — authored/installed by the operator,
// like the instruction files — which is why the agent exempts this
// package's tool from the nonce wrapping (bounded by Body/File refusing
// to read outside a discovered skill's directory).
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ToolName is the read-only tool the model uses to load a skill's body
// or one of its supporting files. The agent treats this one tool's
// results as instructions rather than wrapped data (ADR-0010 §3), so the
// name is a shared constant, not a string repeated in two packages.
const ToolName = "load_skill"

// Limits bound what discovery and loading will read.
type Limits struct {
	MaxSkills      int // listed skills; extras are dropped with a note
	MaxDescription int // runes shown in the system prompt line
	MaxBody        int // bytes of SKILL.md body
	MaxFile        int // bytes of a supporting file
}

// DefaultLimits returns the production limits.
func DefaultLimits() Limits {
	return Limits{MaxSkills: 100, MaxDescription: 400, MaxBody: 64 * 1024, MaxFile: 96 * 1024}
}

// Skill is one discovered skill.
type Skill struct {
	Name         string
	Description  string
	ArgumentHint string
	// Dir is the skill's own directory, symlink-resolved — the boundary
	// Body and File confine reads to.
	Dir string
	// Scope is "personal" (~/.claude/skills) or "project".
	Scope string
}

// namePattern bounds what a skill may be called. Names appear in slash
// commands and tool arguments; a name that needs quoting is a name that
// will be mistyped, and path separators in a name would be a traversal.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Discover scans the personal and project skill directories. The project
// wins a name collision (announced in notes), matching how .mcp.json
// scopes merge. Either directory may be absent — skills are optional
// everywhere.
func Discover(personalDir, projectDir string, lim Limits) ([]Skill, []string) {
	var notes []string
	byName := map[string]Skill{}
	order := []string{}

	scan := func(root, scope string) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return // absent is the normal case, not an error
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := filepath.Join(root, e.Name())
			s, err := readSkill(dir, scope, lim)
			if err != nil {
				notes = append(notes, fmt.Sprintf("skill %s skipped: %v", e.Name(), err))
				continue
			}
			if s == nil {
				continue // no SKILL.md: just a directory
			}
			if prev, exists := byName[s.Name]; exists {
				notes = append(notes, fmt.Sprintf("skill %q: %s overrides %s", s.Name, s.Scope, prev.Scope))
			} else {
				order = append(order, s.Name)
			}
			byName[s.Name] = *s
		}
	}
	// Personal first so a project skill of the same name overrides it.
	scan(personalDir, "personal")
	if projectDir != "" {
		scan(filepath.Join(projectDir, ".claude", "skills"), "project")
	}

	sort.Strings(order)
	if len(order) > lim.MaxSkills {
		notes = append(notes, fmt.Sprintf("%d skills found; listing the first %d", len(order), lim.MaxSkills))
		order = order[:lim.MaxSkills]
	}
	out := make([]Skill, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, notes
}

// readSkill parses one skill directory. nil with no error means "not a
// skill" (no SKILL.md); an error means "looks like a skill, unusable".
func readSkill(dir, scope string, lim Limits) (*Skill, error) {
	path := filepath.Join(dir, "SKILL.md")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fm, _ := splitFrontmatter(string(data))
	meta := parseFrontmatter(fm)

	name := meta["name"]
	if name == "" {
		name = filepath.Base(dir)
	}
	if !namePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	desc := strings.TrimSpace(meta["description"])
	if desc == "" {
		// The description is the load-bearing half of progressive
		// disclosure: without it, nothing can decide when to load the
		// skill, so listing it would just spend a prompt line on a name.
		return nil, fmt.Errorf("SKILL.md has no description in its frontmatter")
	}
	if r := []rune(desc); len(r) > lim.MaxDescription {
		desc = string(r[:lim.MaxDescription]) + "…"
	}
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	return &Skill{
		Name:         name,
		Description:  desc,
		ArgumentHint: strings.TrimSpace(meta["argument-hint"]),
		Dir:          realDir,
		Scope:        scope,
	}, nil
}

// splitFrontmatter separates a leading "---\n…\n---" block from the body.
func splitFrontmatter(content string) (frontmatter, body string) {
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return "", content
	}
	rest := content[strings.Index(content, "\n")+1:]
	for _, marker := range []string{"\n---\n", "\n---\r\n"} {
		if i := strings.Index(rest, marker); i >= 0 {
			return rest[:i], rest[i+len(marker):]
		}
	}
	if strings.HasSuffix(rest, "\n---") {
		return strings.TrimSuffix(rest, "\n---"), ""
	}
	return "", content
}

// parseFrontmatter reads the minimal subset this package needs: one
// "key: value" per line, quotes stripped. Unknown keys are kept (and
// ignored by callers) — the file is another tool's schema, and refusing
// its extensions would break the point of reading it (ADR-0010 §1).
// Multi-line YAML values are out of scope; a skill using them for its
// description will be reported as missing one, which is honest enough.
func parseFrontmatter(fm string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(fm, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		out[strings.TrimSpace(key)] = value
	}
	return out
}

// Find returns the named skill.
func Find(list []Skill, name string) (Skill, bool) {
	for _, s := range list {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// Body returns the skill's instructions: SKILL.md without its
// frontmatter, clipped at the limit with an explicit truncation note (a
// silently amputated procedure looks complete).
func (s Skill) Body(lim Limits) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(data))
	body = strings.TrimSpace(body)
	if len(body) > lim.MaxBody {
		body = body[:lim.MaxBody] +
			fmt.Sprintf("\n\n[skill truncated: %d of %d bytes shown]", lim.MaxBody, len(body))
	}
	return body, nil
}

// File returns a supporting file from the skill's own directory
// (references/, scripts/, …). Reads are confined to that directory,
// symlinks resolved and re-checked — this confinement is what bounds the
// agent's unwrap exemption for this tool: without it, load_skill would
// be "read any file on disk, unwrapped" (ADR-0010 §4).
func (s Skill) File(rel string, lim Limits) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("file must be a path relative to the skill directory")
	}
	abs := filepath.Clean(filepath.Join(s.Dir, rel))
	if !within(s.Dir, abs) {
		return "", fmt.Errorf("path escapes the skill directory: %s", rel)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !within(s.Dir, real) {
		return "", fmt.Errorf("path escapes the skill directory via symlink: %s", rel)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(real)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return strings.Join(names, "\n"), nil
	}
	data, err := os.ReadFile(real)
	if err != nil {
		return "", err
	}
	if len(data) > lim.MaxFile {
		return string(data[:lim.MaxFile]) +
			fmt.Sprintf("\n\n[file truncated: %d of %d bytes shown]", lim.MaxFile, len(data)), nil
	}
	return string(data), nil
}

func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// PromptSection renders the system-prompt listing: one line per skill.
// Empty input renders nothing — a session without skills should not
// carry a heading promising them.
func PromptSection(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nSkills — procedure packages the user has installed. Each is a set of the user's own instructions for one kind of task. When the task at hand matches a description below, call the " + ToolName +
		" tool with the skill's name and follow the instructions it returns; use " + ToolName +
		"(name, file) for supporting files the skill references. Do not guess a skill's content from its name.\n")
	for _, s := range list {
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, s.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Package skills discovers and loads Claude Code skills (ADR-0010/0011):
// directories carrying a SKILL.md with YAML frontmatter. The format is
// Claude Code's, read as-is; the global location is gem-agent's own
// (~/.config/gem-agent/skills/), because format compatibility is
// drop-in while location sharing is coupling — reading another tool's
// live environment means the fallback's behaviour changes whenever the
// primary's does. Sharing with Claude Code is an operator-made symlink,
// which discovery follows. The project scope
// (<project>/.claude/skills/) is shared on purpose: a repository is the
// project's environment, not either tool's.
//
// Skills are progressive disclosure: one description line per skill sits
// in the system prompt, and the body loads only when used. Loaded
// content is instruction-grade — authored/installed by the operator,
// like the instruction files — which is why the agent exempts this
// package's tool from the nonce wrapping (bounded by Body/File refusing
// to read outside a discovered skill's directory).
package skills

import (
	"github.com/nlink-jp/gem-agent/internal/bounded"

	"fmt"
	"io"
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

// BaseDirPrefix opens the line that tells the model where a loaded
// skill lives. It is Claude Code's wording, verbatim (ADR-0070 §1):
// SKILL.md files are written against that sentence ("SKILL_DIR is the
// directory containing this SKILL.md"), so the same sentence is the
// whole of the compatibility.
const BaseDirPrefix = "Base directory for this skill: "

// BaseDirLine renders that line for one skill: the symlink-resolved
// directory Body and File confine reads to — a place the model could
// already read under, now named so the skill's scripts can be run.
func BaseDirLine(s Skill) string { return BaseDirPrefix + s.Dir }

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
	// Entry is the directory entry name under the skills root — the
	// key the content pins use (ADR-0074); Name may differ (frontmatter).
	Entry string
	// Dir is the skill's own directory, symlink-resolved — the boundary
	// Body and File confine reads to.
	Dir string
	// Scope is "global" (~/.config/gem-agent/skills) or "project" —
	// MCP's vocabulary (ADR-0011).
	Scope string
	// root is Dir held open as an os.Root: every read of the skill goes
	// through it, so a file or directory swapped for a link that leads
	// out between a check and a read is refused at the open (review
	// after v0.68.2 — the reads used the lexical path, and their result
	// reaches the model unwrapped). nil on a Skill built by hand; the
	// readers then open Dir for the call.
	root *os.Root
}

// skillDirEntryCap bounds a skill directory listing; skillScanCap
// bounds the scan of a skills root at startup (review after v0.68.2,
// R12 — os.ReadDir held every entry).
const (
	skillDirEntryCap = 2000
	skillScanCap     = 1000
)

// readDirBounded lists at most n entries of dir, reporting whether
// there were more.
func readDirBounded(dir string, n int) ([]os.DirEntry, bool, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = d.Close() }()
	entries, more, err := bounded.ReadDir(d, n)
	if err != nil {
		return nil, false, err
	}
	if more {
		return entries, true, nil
	}
	return entries, false, nil
}

// skillReadCap bounds one read of a skill file at discovery and in
// Body: the frontmatter and body of any real SKILL.md fit in it; a
// sparse or generated giant does not reach memory.
const skillReadCap = 4 << 20

// openRoot returns the skill's root and the release the caller owes.
func (s Skill) openRoot() (*os.Root, func(), error) {
	if s.root != nil {
		return s.root, func() {}, nil
	}
	root, err := os.OpenRoot(s.Dir)
	if err != nil {
		return nil, nil, err
	}
	return root, func() { _ = root.Close() }, nil
}

// Close releases the skill's root. CloseAll does it for a discovered
// list that a reload replaces.
func (s Skill) Close() {
	if s.root != nil {
		_ = s.root.Close()
	}
}

// CloseAll closes every skill's root — for the list a reload replaces.
func CloseAll(list []Skill) {
	for _, s := range list {
		s.Close()
	}
}

// readCapped reads rel through root, at most cap bytes, reporting
// whether more followed.
func readCapped(root *os.Root, rel string, cap int) ([]byte, bool, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	return bounded.ReadAll(f, cap)
}

// namePattern bounds what a skill may be called. Names appear in slash
// commands and tool arguments; a name that needs quoting is a name that
// will be mistyped, and path separators in a name would be a traversal.
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Discover scans the global and project skill directories. The project
// wins a name collision (announced in notes), matching how .mcp.json
// scopes merge. Either directory may be absent — skills are optional
// everywhere.
//
// Symlinked entries are followed: a skill shared from Claude Code's
// directory by `ln -s` is discovered like a real one, and the read
// confinement downstream applies to the resolved directory (ADR-0011).
func Discover(globalDir, projectDir string, lim Limits) ([]Skill, []string) {
	var notes []string
	byName := map[string]Skill{}
	order := []string{}

	scan := func(root, scope string) {
		entries, more, err := readDirBounded(root, skillScanCap)
		if err != nil {
			return // absent is the normal case, not an error
		}
		if more {
			notes = append(notes, fmt.Sprintf("%s holds more than %d entries; only the first %d were scanned for skills", root, skillScanCap, skillScanCap))
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			// os.Stat, not e.IsDir(): a symlinked skill directory reports
			// IsDir()=false on the DirEntry, and symlinks are exactly how
			// sharing with Claude Code works (ADR-0011 §3).
			info, err := os.Stat(dir)
			if err != nil || !info.IsDir() {
				continue
			}
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
				prev.Close() // the overridden skill's root (review after v0.68.2)
			} else {
				order = append(order, s.Name)
			}
			byName[s.Name] = *s
		}
	}
	// Global first so a project skill of the same name overrides it.
	scan(globalDir, "global")
	if projectDir != "" {
		scan(filepath.Join(projectDir, ".claude", "skills"), "project")
	}

	sort.Strings(order)
	if len(order) > lim.MaxSkills {
		notes = append(notes, fmt.Sprintf("%d skills found; listing the first %d", len(order), lim.MaxSkills))
		for _, name := range order[lim.MaxSkills:] {
			byName[name].Close() // dropped from the list: its root goes too
		}
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
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, err
	}
	// The root is opened first and SKILL.md read through it: the
	// description enters the system prompt, and must come from inside
	// the skill directory however the tree changes underneath.
	root, err := os.OpenRoot(realDir)
	if err != nil {
		return nil, err
	}
	if _, err := root.Stat("SKILL.md"); err != nil {
		_ = root.Close()
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	data, _, err := readCapped(root, "SKILL.md", skillReadCap)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	fm, _ := splitFrontmatter(string(data))
	meta := parseFrontmatter(fm)

	name := meta["name"]
	if name == "" {
		name = filepath.Base(dir)
	}
	if !namePattern.MatchString(name) {
		_ = root.Close()
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	desc := strings.TrimSpace(meta["description"])
	if desc == "" {
		// The description is the load-bearing half of progressive
		// disclosure: without it, nothing can decide when to load the
		// skill, so listing it would just spend a prompt line on a name.
		_ = root.Close()
		return nil, fmt.Errorf("SKILL.md has no description in its frontmatter")
	}
	if r := []rune(desc); len(r) > lim.MaxDescription {
		desc = string(r[:lim.MaxDescription]) + "…"
	}
	return &Skill{
		Name:         name,
		Entry:        filepath.Base(dir),
		Description:  desc,
		ArgumentHint: strings.TrimSpace(meta["argument-hint"]),
		Dir:          realDir,
		Scope:        scope,
		root:         root,
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
	root, release, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer release()
	data, more, err := readCapped(root, "SKILL.md", skillReadCap)
	if err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(data))
	body = strings.TrimSpace(body)
	if len(body) > lim.MaxBody || more {
		cut := cutRunes(body, lim.MaxBody)
		total := fmt.Sprintf("%d", len(body))
		if more {
			total = fmt.Sprintf("more than %d", skillReadCap) // the file outgrew the read cap (review A-08)
		}
		body = cut + fmt.Sprintf("\n\n[skill truncated: %d of %s bytes shown]", len(cut), total)
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
	inside, err := filepath.Rel(s.Dir, abs)
	if err != nil {
		return "", err
	}
	// Opened through the root: a link that leads out of the skill
	// directory is refused at the open, whenever it appeared.
	root, release, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer release()
	info, err := root.Stat(inside)
	if err != nil {
		if strings.Contains(err.Error(), "escapes") {
			return "", fmt.Errorf("path escapes the skill directory via symlink: %s", rel)
		}
		return "", err
	}
	if info.IsDir() {
		d, err := root.Open(inside)
		if err != nil {
			return "", err
		}
		entries, err := d.ReadDir(skillDirEntryCap + 1)
		_ = d.Close()
		if err != nil && err != io.EOF {
			return "", err
		}
		names := make([]string, 0, len(entries))
		for i, e := range entries {
			if i == skillDirEntryCap {
				names = append(names, fmt.Sprintf("[more than %d entries — listing stopped]", skillDirEntryCap))
				break
			}
			names = append(names, e.Name())
		}
		return strings.Join(names, "\n"), nil
	}
	if info.Size() > int64(lim.MaxFile) || !info.Mode().IsRegular() {
		// Refused by size before the read; the note names the cap.
		return "", fmt.Errorf("%s is %d bytes; the skill file limit is %d", rel, info.Size(), lim.MaxFile)
	}
	data, more, err := readCapped(root, inside, lim.MaxFile)
	if err != nil {
		return "", err
	}
	if more {
		// Exactly MaxFile bytes: trim a rune the cut broke (review A-08).
		cut := string(bounded.TrimIncompleteRune(data))
		return cut + fmt.Sprintf("\n\n[file truncated: %d bytes shown of a file past the %d-byte limit]", len(cut), lim.MaxFile), nil
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

// cutRunes truncates s to at most n bytes without splitting a UTF-8
// sequence (internal/bounded is the one implementation).
func cutRunes(s string, n int) string {
	return string(bounded.CutRunes([]byte(s), n))
}

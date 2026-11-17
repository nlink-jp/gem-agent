// Package skills discovers and loads Claude Code skills (ADR-0010/0011):
// directories carrying a SKILL.md with YAML frontmatter. The format is
// Claude Code's, read as-is; the global location is gem-agent's own
// (~/.config/gem-agent/skills/), because format compatibility is
// drop-in while location sharing is coupling — reading another tool's
// live environment means the fallback's behaviour changes whenever the
// primary's does. A skill installed for Claude Code is copied in, not
// linked (ADR-0076: ~/.claude is denied to the shell lanes and the
// kernel resolves a link); discovery follows symlinks to readable
// locations. The project scope
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
	"github.com/nlink-jp/gem-agent/internal/pathid"

	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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
	MaxDescription int // runes shown in the system prompt line (ADR-0106: the platform's 1,024)
	MaxBody        int // bytes of SKILL.md body
	MaxFile        int // bytes of a supporting file
}

// DefaultLimits returns the production limits.
func DefaultLimits() Limits {
	return Limits{MaxSkills: 100, MaxDescription: 1024, MaxBody: 64 * 1024, MaxFile: 96 * 1024}
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
	// descRunes is the description's length before the cut, so
	// discovery can say a skill's triggers were hidden (ADR-0106).
	descRunes int
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

// ReadCap bounds one read of SKILL.md at discovery and in Body, and a
// SKILL.md larger than it is not a skill. It is the size up to which a
// content pin covers a file's bytes (trustpin.FileCap; a test holds the
// two equal): past it a pin sees only the size, so a larger SKILL.md
// could keep its frontmatter in the covered megabyte and its body
// outside it, and change the body without changing its pin (the release
// review of ADR-0119 demonstrated it). Nothing the pin does not cover is
// served. The frontmatter and body of any real SKILL.md are a small
// fraction of this.
const ReadCap = 1 << 20

const skillReadCap = ReadCap

// ErrTooLarge: SKILL.md is past ReadCap.
var ErrTooLarge = errors.New("SKILL.md is larger than 1 MiB (1,048,576 bytes)")

// Verify holds a read to what the caller is prepared to serve
// (ADR-0119 §1). A nil *Verify verifies nothing.
type Verify struct {
	// Content is given each file's bytes, under its path relative to
	// the skill's directory (slash-separated), before anything made from
	// them is returned. An error from it is the read's error.
	Content func(rel string, data []byte) error
	// Listed reports whether an entry may appear in a directory listing.
	// A listing returns names, and a name is text too: one the caller
	// has not compared — an empty directory called whatever its author
	// liked — is left out, and the listing says how many were.
	Listed func(rel string) bool
}

func (v *Verify) content(rel string, data []byte) error {
	if v == nil || v.Content == nil {
		return nil
	}
	return v.Content(rel, data)
}

func (v *Verify) listed(rel string) bool {
	return v == nil || v.Listed == nil || v.Listed(rel)
}

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

// WithRoot runs fn on the root every read of this skill goes through.
// The comparison made when a skill is used (ADR-0119 §1) digests through
// it, so what is compared is the tree Body and File read — not whatever
// the skill's path resolves to at that moment.
func (s Skill) WithRoot(fn func(root *os.Root) error) error {
	root, release, err := s.openRoot()
	if err != nil {
		return err
	}
	defer release()
	return fn(root)
}

// unsafeEntry reports a name or path that could write into a dialog or
// a note that shows it: a control character (a line break above all), a
// format character such as a bidirectional override, or bytes that are
// not text at all. The name a skill declares is validated by
// namePattern; its directory is not, and the trust question shows the
// directory (ADR-0119 §11). Both spellings are checked: the entry under
// the skills root, and the path it resolves to — an entry with a plain
// name may be a link to a directory that has neither.
func unsafeEntry(name string) bool {
	if !utf8.ValidString(name) {
		return true
	}
	for _, r := range name {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
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
	// Not a plain open: a SKILL.md that is a FIFO would hold discovery,
	// and so startup, until somebody wrote to it.
	f, err := bounded.OpenRegular(root, rel)
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
// Symlinked entries are followed and resolved, and the read confinement
// downstream applies to the resolved directory (ADR-0011). A link into
// ~/.claude is discovered too, but the shell lanes cannot read it —
// the operator copies instead (ADR-0076).
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
			if unsafeEntry(e.Name()) {
				// Before anything is read from it, and quoted: the note
				// must not carry the characters it is about.
				notes = append(notes, fmt.Sprintf("skill directory %q skipped: its name holds a control or format character, or is not valid text", e.Name()))
				continue
			}
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
			if s.descRunes > lim.MaxDescription {
				// A cut hides triggers: the operator is told which
				// skill, rather than finding out that it never loads
				// (ADR-0106).
				notes = append(notes, fmt.Sprintf("skill %q: description is %d characters; only the first %d are listed", s.Name, s.descRunes, lim.MaxDescription))
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
	if unsafeEntry(realDir) {
		// Dir is what the base-directory line and the trust question
		// show. Quoted here too: the error becomes a note.
		return nil, fmt.Errorf("it resolves to %q, which holds a control or format character", realDir)
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
	data, more, err := readCapped(root, "SKILL.md", skillReadCap)
	if err == nil && more {
		err = ErrTooLarge
	}
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
	// The prompt gives each skill one line (ADR-0106): a `|` block keeps
	// its line breaks, and a `>` block turns a blank line into one, so
	// they are joined with spaces here — a break left in would start a
	// new line of the system prompt in the skill author's words (lagent's
	// independent review of the port).
	desc = oneLine(desc)
	descRunes := utf8.RuneCountInString(desc)
	if r := []rune(desc); len(r) > lim.MaxDescription {
		desc = string(r[:lim.MaxDescription]) + "…"
	}
	return &Skill{
		descRunes:    descRunes,
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
// A value continued on more-indented lines — a block scalar or a folded
// plain scalar — is read whole (ADR-0106): the old reading turned
// `description: >` into the description ">", which is not "missing"
// but wrong. Quoted values spanning lines, anchors and flow collections
// are still not read; this is not a YAML parser.
func parseFrontmatter(fm string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(strings.ReplaceAll(fm, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		// A value may continue on the more-indented lines that follow
		// (ADR-0106): a block scalar (`>` folds, `|` keeps line breaks)
		// or a plain scalar folded across lines. Read as one value
		// rather than dropped — `description: >` was a one-character
		// description.
		var cont []string
		for i+1 < len(lines) && (strings.TrimSpace(lines[i+1]) == "" || lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
			i++
			cont = append(cont, lines[i])
		}
		switch {
		case isBlockIndicator(value):
			value = blockScalar(value[0] == '|', cont)
		case len(cont) > 0:
			value = foldLines(append([]string{value}, dedent(cont)...))
		}
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

// isBlockIndicator reports a YAML block scalar header: `>` or `|`, with
// an optional chomping indicator (`-`, `+`) and indentation digit in
// either order. A trailing comment is allowed.
func isBlockIndicator(v string) bool {
	if v == "" || (v[0] != '>' && v[0] != '|') {
		return false
	}
	rest := v[1:]
	if j := strings.Index(rest, "#"); j >= 0 {
		rest = rest[:j]
	}
	rest = strings.TrimSpace(rest)
	if len(rest) > 2 {
		return false
	}
	for _, c := range rest {
		if c != '-' && c != '+' && (c < '1' || c > '9') {
			return false
		}
	}
	return true
}

// blockScalar renders a block scalar's lines: literal keeps the line
// breaks, folded joins lines with a space and turns a blank line into a
// break. Chomping does not matter here: every value is trimmed.
func blockScalar(literal bool, lines []string) string {
	lines = dedent(lines)
	if literal {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return foldLines(lines)
}

// foldLines joins lines the way YAML folds them: a space between lines,
// a line break for each blank line.
func foldLines(lines []string) string {
	var b strings.Builder
	pendingBreak := false
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			b.WriteString("\n")
			pendingBreak = true
			continue
		}
		if b.Len() > 0 && !pendingBreak {
			b.WriteString(" ")
		}
		b.WriteString(l)
		pendingBreak = false
	}
	return strings.TrimSpace(b.String())
}

// dedent removes the indentation of the first non-blank line from every
// line that carries it.
func dedent(lines []string) []string {
	indent := ""
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			indent = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			break
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimPrefix(l, indent)
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
func (s Skill) Body(lim Limits) (string, error) { return s.BodyVerified(lim, nil) }

// BodyVerified is Body with the file's bytes handed to verify first.
// The whole file is read — a SKILL.md past ReadCap is an error, not a
// truncated body — so what verify sees is everything the body is made
// from.
func (s Skill) BodyVerified(lim Limits, verify *Verify) (string, error) {
	root, release, err := s.openRoot()
	if err != nil {
		return "", err
	}
	defer release()
	data, more, err := readCapped(root, "SKILL.md", skillReadCap)
	if err != nil {
		return "", err
	}
	if more {
		return "", ErrTooLarge // it outgrew the cap since discovery
	}
	if err := verify.content("SKILL.md", data); err != nil {
		return "", err
	}
	_, body := splitFrontmatter(string(data))
	body = strings.TrimSpace(body)
	if len(body) > lim.MaxBody {
		cut := cutRunes(body, lim.MaxBody)
		body = cut + fmt.Sprintf("\n\n[skill truncated: %d of %d bytes shown]", len(cut), len(body))
	}
	return body, nil
}

// File returns a supporting file from the skill's own directory
// (references/, scripts/, …). Reads are confined to that directory,
// symlinks resolved and re-checked — this confinement is what bounds the
// agent's unwrap exemption for this tool: without it, load_skill would
// be "read any file on disk, unwrapped" (ADR-0010 §4).
func (s Skill) File(rel string, lim Limits) (string, error) { return s.FileVerified(rel, lim, nil) }

// FileVerified is File with the file's bytes handed to verify first. A
// file is read whole or not at all: one past the limit is refused by
// size, and one that grew past it between the size check and the read
// is refused too, so verify always sees everything that is returned. A
// directory listing shows only the entries verify allows.
func (s Skill) FileVerified(rel string, lim Limits, verify *Verify) (string, error) {
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
		hidden := 0
		for i, e := range entries {
			if i == skillDirEntryCap {
				names = append(names, fmt.Sprintf("[more than %d entries — listing stopped]", skillDirEntryCap))
				break
			}
			if !verify.listed(path.Join(filepath.ToSlash(inside), e.Name())) {
				hidden++
				continue
			}
			names = append(names, e.Name())
		}
		if hidden > 0 {
			names = append(names, fmt.Sprintf("[not listed: %d that are not part of the trusted skill]", hidden))
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
		// It grew past the limit after the size check: refused like a
		// file that was already past it, rather than served in part.
		return "", fmt.Errorf("%s is larger than the skill file limit of %d bytes", rel, lim.MaxFile)
	}
	if err := verify.content(filepath.ToSlash(inside), data); err != nil {
		return "", err
	}
	return string(data), nil
}

// within is pathid.Within (ADR-0101): one comparison for every
// containment check, in the volume's sense of a name.
func within(base, p string) bool {
	return pathid.Within(base, p)
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

// oneLine joins a multi-line value into one line: each line trimmed, the
// empty ones dropped, the rest joined by a space.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	var parts []string
	for _, l := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' }) {
		if l = strings.TrimSpace(l); l != "" {
			parts = append(parts, l)
		}
	}
	return strings.Join(parts, " ")
}

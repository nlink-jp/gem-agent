package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, dir, frontmatter, body string) string {
	t.Helper()
	d := filepath.Join(root, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\n" + frontmatter + "\n---\n" + body
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDiscoverReadsClaudeCodeFormat(t *testing.T) {
	personal := t.TempDir()
	// The real shape from skills-series: quoted argument-hint, an
	// allowed-tools line we must ignore rather than choke on.
	writeSkill(t, personal, "meeting-notes",
		`name: meeting-notes
description: Structure a meeting transcript into minutes. 議事録の作成・構造化.
argument-hint: "<transcript-file> [--lang ja|en]"
allowed-tools: Read Write Bash(python3 *)`,
		"# meeting-notes\n\nDo the thing.")

	list, notes := Discover(personal, "", DefaultLimits())
	if len(notes) != 0 {
		t.Errorf("notes = %v", notes)
	}
	if len(list) != 1 {
		t.Fatalf("found %d skills", len(list))
	}
	s := list[0]
	if s.Name != "meeting-notes" || s.Scope != "global" {
		t.Errorf("skill = %+v", s)
	}
	if !strings.Contains(s.Description, "議事録") {
		t.Errorf("description lost: %q", s.Description)
	}
	if s.ArgumentHint != "<transcript-file> [--lang ja|en]" {
		t.Errorf("argument-hint = %q (quotes should be stripped)", s.ArgumentHint)
	}

	body, err := s.Body(DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, "allowed-tools") || !strings.Contains(body, "Do the thing.") {
		t.Errorf("body = %q — frontmatter must be stripped", body)
	}
}

func TestDiscoverProjectWinsCollisions(t *testing.T) {
	personal, project := t.TempDir(), t.TempDir()
	writeSkill(t, personal, "deploy", "name: deploy\ndescription: global version", "P")
	writeSkill(t, filepath.Join(project, ".claude", "skills"), "deploy",
		"name: deploy\ndescription: project version", "Q")

	list, notes := Discover(personal, project, DefaultLimits())
	if len(list) != 1 || list[0].Scope != "project" || list[0].Description != "project version" {
		t.Fatalf("list = %+v", list)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "overrides") {
		t.Errorf("the collision was silent: %v", notes)
	}
}

func TestDiscoverSkipsUnusableSkillsWithNotes(t *testing.T) {
	personal := t.TempDir()
	// No description: the load-bearing half of progressive disclosure.
	writeSkill(t, personal, "no-desc", "name: no-desc", "body")
	// A name that would be a traversal or an unmatchable slash command.
	writeSkill(t, personal, "bad-name", "name: ../escape\ndescription: d", "body")
	// A plain directory is not a skill and not worth a note.
	if err := os.MkdirAll(filepath.Join(personal, "not-a-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, personal, "good", "name: good\ndescription: works", "body")

	list, notes := Discover(personal, "", DefaultLimits())
	if len(list) != 1 || list[0].Name != "good" {
		t.Fatalf("list = %+v", list)
	}
	if len(notes) != 2 {
		t.Errorf("skips must be reported, not silent: %v", notes)
	}
}

func TestDiscoverMissingDirsAreEmpty(t *testing.T) {
	list, notes := Discover(filepath.Join(t.TempDir(), "nope"), "", DefaultLimits())
	if len(list) != 0 || len(notes) != 0 {
		t.Errorf("list=%v notes=%v", list, notes)
	}
}

// File reads are the boundary that bounds the agent's unwrap exemption:
// escaping the skill directory would turn load_skill into "read any file
// on disk, unwrapped".
func TestFileIsConfinedToTheSkillDirectory(t *testing.T) {
	personal := t.TempDir()
	dir := writeSkill(t, personal, "s", "name: s\ndescription: d", "body")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "guide.md"), []byte("REF"), 0o644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(dir, "link.md")); err != nil {
		t.Fatal(err)
	}

	list, _ := Discover(personal, "", DefaultLimits())
	s := list[0]

	got, err := s.File("references/guide.md", DefaultLimits())
	if err != nil || got != "REF" {
		t.Errorf("legitimate read failed: %q %v", got, err)
	}
	for _, bad := range []string{"../../secret.txt", "../s/../../x", "/etc/hosts", "link.md"} {
		if out, err := s.File(bad, DefaultLimits()); err == nil {
			t.Errorf("File(%q) succeeded: %.40q", bad, out)
		}
	}
	// A directory listing helps the model navigate references/.
	if got, err := s.File("references", DefaultLimits()); err != nil || !strings.Contains(got, "guide.md") {
		t.Errorf("directory listing: %q %v", got, err)
	}
}

func TestBodyClipsWithAnExplicitNote(t *testing.T) {
	personal := t.TempDir()
	lim := DefaultLimits()
	lim.MaxBody = 100
	writeSkill(t, personal, "big", "name: big\ndescription: d", strings.Repeat("x", 500))
	list, _ := Discover(personal, "", lim)
	body, err := list[0].Body(lim)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "[skill truncated") {
		t.Error("a silently amputated procedure looks complete")
	}
}

func TestPromptSectionListsAndEmptyIsEmpty(t *testing.T) {
	if PromptSection(nil) != "" {
		t.Error("no skills must render no heading")
	}
	out := PromptSection([]Skill{{Name: "a", Description: "does a"}, {Name: "b", Description: "does b"}})
	for _, want := range []string{ToolName, "- a: does a", "- b: does b"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt section missing %q:\n%s", want, out)
		}
	}
}

func TestFrontmatterOddShapes(t *testing.T) {
	// No frontmatter at all: body is everything, no metadata.
	fm, body := splitFrontmatter("just a body\n")
	if fm != "" || body != "just a body\n" {
		t.Errorf("fm=%q body=%q", fm, body)
	}
	// Unterminated frontmatter: treat the whole thing as body rather
	// than swallowing the file into metadata.
	fm, _ = splitFrontmatter("---\nname: x\nno terminator")
	if fm != "" {
		t.Errorf("unterminated frontmatter parsed as %q", fm)
	}
	// Colons in values survive (descriptions contain URLs and 日本語:).
	meta := parseFrontmatter("description: use for: everything, https://example.com")
	if meta["description"] != "use for: everything, https://example.com" {
		t.Errorf("description = %q", meta["description"])
	}
}

// ADR-0011: sharing with Claude Code is an operator-made symlink, so a
// linked skill directory must be discovered like a real one — and its
// confinement boundary is the resolved directory.
func TestDiscoverFollowsSymlinkedSkills(t *testing.T) {
	claudeSide := t.TempDir() // stands in for ~/.claude/skills
	dir := writeSkill(t, claudeSide, "shared", "name: shared\ndescription: linked in", "SHARED BODY")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "r.md"), []byte("REF"), 0o644); err != nil {
		t.Fatal(err)
	}

	gemSide := t.TempDir() // stands in for ~/.config/gem-agent/skills
	if err := os.Symlink(dir, filepath.Join(gemSide, "shared")); err != nil {
		t.Fatal(err)
	}

	list, notes := Discover(gemSide, "", DefaultLimits())
	if len(list) != 1 || list[0].Name != "shared" {
		t.Fatalf("symlinked skill not discovered: %+v %v", list, notes)
	}
	body, err := list[0].Body(DefaultLimits())
	if err != nil || !strings.Contains(body, "SHARED BODY") {
		t.Errorf("body through the link: %q %v", body, err)
	}
	if got, err := list[0].File("references/r.md", DefaultLimits()); err != nil || got != "REF" {
		t.Errorf("supporting file through the link: %q %v", got, err)
	}
	// The boundary is the resolved directory — escapes still refused.
	if _, err := list[0].File("../../etc/hosts", DefaultLimits()); err == nil {
		t.Error("a linked skill escaped its resolved directory")
	}
}

// Linking the whole directory is the "share everything" recipe.
func TestDiscoverFollowsAFullySymlinkedRoot(t *testing.T) {
	claudeSide := t.TempDir()
	writeSkill(t, claudeSide, "one", "name: one\ndescription: d1", "b")
	writeSkill(t, claudeSide, "two", "name: two\ndescription: d2", "b")

	parent := t.TempDir()
	root := filepath.Join(parent, "skills")
	if err := os.Symlink(claudeSide, root); err != nil {
		t.Fatal(err)
	}
	list, _ := Discover(root, "", DefaultLimits())
	if len(list) != 2 {
		t.Fatalf("linked root found %d skills, want 2", len(list))
	}
}

// Review after v0.68.2: truncation lands on a rune boundary.
func TestCutRunesKeepsRunesWhole(t *testing.T) {
	if got := cutRunes("あいう", 4); got != "あ" {
		t.Errorf("cutRunes = %q", got)
	}
}

// Review after v0.68.2: skill reads go through the skill's os.Root —
// a link that leads out is refused at the open, a huge file is capped
// before it is held, and the note names what was actually shown.
func TestSkillReadsThroughItsRoot(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	global := t.TempDir()
	dir := filepath.Join(global, "s")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: s\ndescription: d\n---\n"+strings.Repeat("本文", 3000)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(dir, "references", "link.md")); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "references", "huge.txt")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	lim := DefaultLimits()
	lim.MaxBody = 100
	lim.MaxFile = 1024
	list, _ := Discover(global, "", lim)
	if len(list) != 1 {
		t.Fatalf("discovered %d skills", len(list))
	}
	s := list[0]
	defer s.Close()
	if s.root == nil {
		t.Fatal("the discovered skill holds no root")
	}
	if _, err := s.File("references/link.md", lim); err == nil {
		t.Fatal("a link out of the skill directory was read")
	}
	if _, err := s.File("references/huge.txt", lim); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("a 64 MiB file was not refused by size: %v", err)
	}
	body, err := s.Body(lim)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "[skill truncated: 99 of 18000 bytes shown]") {
		t.Errorf("the note does not name the bytes actually shown:\n%s", body[len(body)-80:])
	}
	if names, err := s.File("references", lim); err != nil || !strings.Contains(names, "huge.txt") {
		t.Errorf("directory listing through the root failed: %q %v", names, err)
	}
}

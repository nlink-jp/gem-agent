package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func testSkillDir(t *testing.T) []skills.Skill {
	t.Helper()
	personal := t.TempDir()
	dir := filepath.Join(personal, "meeting-notes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := `---
name: meeting-notes
description: Structure a transcript into minutes.
argument-hint: "<file> [--lang ja|en]"
---
# meeting-notes

Read the transcript, then produce minutes.`
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	list, notes := skills.Discover(personal, "", skills.DefaultLimits())
	if len(notes) != 0 || len(list) != 1 {
		t.Fatalf("discovery: %v %v", list, notes)
	}
	return list
}

// The operator already decided: /skill injects the body directly, no
// model round spent asking for it (ADR-0010 §2).
func TestExpandSkillInputInjectsTheBody(t *testing.T) {
	list := testSkillDir(t)
	turn, handled, errMsg := expandSkillInput("/skill meeting-notes rec.vtt --lang ja", list)
	if !handled || errMsg != "" {
		t.Fatalf("handled=%v err=%q", handled, errMsg)
	}
	for _, want := range []string{"rec.vtt --lang ja", "produce minutes", "load_skill", skills.BaseDirPrefix + list[0].Dir} {
		if !strings.Contains(turn, want) {
			t.Errorf("turn missing %q:\n%.300s", want, turn)
		}
	}
	if strings.Contains(turn, "argument-hint") {
		t.Error("frontmatter leaked into the turn")
	}
}

// A SKILL.md written to Claude Code's contract says "SKILL_DIR is the
// directory containing this SKILL.md" and runs `python3
// SKILL_DIR/scripts/…`. Without the directory in the load result a
// global skill's scripts are reachable by no path the model knows —
// session 20260904-225330 went looking with `find /` (ADR-0070 §1).
func TestLoadSkillNamesItsBaseDirectoryInClaudeCodesWords(t *testing.T) {
	list := testSkillDir(t)
	registry, err := tools.New(t.TempDir(), nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerSkillTool(registry, func() []skills.Skill { return list }); err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get(skills.ToolName)
	if !ok {
		t.Fatal("load_skill not registered")
	}
	out, err := tool.Run(context.Background(), map[string]any{"name": "meeting-notes"})
	if err != nil {
		t.Fatal(err)
	}
	line := "Base directory for this skill: " + list[0].Dir
	at := strings.Index(out, line)
	if at < 0 {
		t.Fatalf("load result lacks %q:\n%s", line, out)
	}
	if body := strings.Index(out, "produce minutes"); body < at {
		t.Errorf("the directory line must precede the body:\n%s", out)
	}
	// The directory named is the symlink-resolved one reads are
	// confined to — an absolute, existing directory holding SKILL.md.
	if !filepath.IsAbs(list[0].Dir) {
		t.Errorf("Dir %q is not absolute", list[0].Dir)
	}
	if _, err := os.Stat(filepath.Join(list[0].Dir, "SKILL.md")); err != nil {
		t.Errorf("Dir does not hold SKILL.md: %v", err)
	}
	if !strings.Contains(tool.Description, skills.BaseDirPrefix) {
		t.Error("the tool description does not tell the model to expect the directory line")
	}
	// A supporting-file read returns the file alone; the body is where
	// the location is disclosed, once.
	file, err := tool.Run(context.Background(), map[string]any{"name": "meeting-notes", "file": "SKILL.md"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(file, skills.BaseDirPrefix) {
		t.Error("a supporting-file read must not prepend the directory line")
	}
}

func TestExpandSkillInputErrorsAreHandledNotTurns(t *testing.T) {
	list := testSkillDir(t)
	for input, wantErr := range map[string]string{
		"/skill":        "usage:",
		"/skill nope":   "unknown skill",
		"/skill nope x": "unknown skill",
	} {
		turn, handled, errMsg := expandSkillInput(input, list)
		if !handled || turn != "" || !strings.Contains(errMsg, wantErr) {
			t.Errorf("expand(%q) = %q, %v, %q", input, turn, handled, errMsg)
		}
	}
	// Everything else passes through untouched — including /skills,
	// which is the listing command, not an invocation.
	for _, input := range []string{"/skills", "/help", "hello", "!ls", "/skillet"} {
		if _, handled, _ := expandSkillInput(input, list); handled {
			t.Errorf("expand(%q) claimed the input", input)
		}
	}
}

func TestSkillsListingShowsUsageAndInstallPathsWhenEmpty(t *testing.T) {
	list := testSkillDir(t)
	out := skillsListing(list)
	for _, want := range []string{"meeting-notes", "[global]", "/skill meeting-notes <file> [--lang ja|en]"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
	empty := skillsListing(nil)
	// The empty state must teach both the install location and the
	// sharing recipe — the knowledge is needed exactly when it is missing.
	for _, want := range []string{"~/.config/gem-agent/skills", "ln -s ~/.claude/skills"} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty listing missing %q:\n%s", want, empty)
		}
	}
}

func TestSkillBannerLine(t *testing.T) {
	if skillBannerLine(nil) != "" {
		t.Error("no skills must add no banner line")
	}
	list := []skills.Skill{{Name: "a", Scope: "global"}, {Name: "b", Scope: "project"}}
	line := skillBannerLine(list)
	if !strings.Contains(line, "a") || !strings.Contains(line, "b [project]") {
		t.Errorf("banner = %q", line)
	}
}

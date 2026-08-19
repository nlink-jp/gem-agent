package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/skills"
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
	for _, want := range []string{"rec.vtt --lang ja", "produce minutes", "load_skill"} {
		if !strings.Contains(turn, want) {
			t.Errorf("turn missing %q:\n%.300s", want, turn)
		}
	}
	if strings.Contains(turn, "argument-hint") {
		t.Error("frontmatter leaked into the turn")
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
	for _, want := range []string{"meeting-notes", "[personal]", "/skill meeting-notes <file> [--lang ja|en]"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing missing %q:\n%s", want, out)
		}
	}
	empty := skillsListing(nil)
	if !strings.Contains(empty, "~/.claude/skills") {
		t.Errorf("empty listing must say where skills install:\n%s", empty)
	}
}

func TestSkillBannerLine(t *testing.T) {
	if skillBannerLine(nil) != "" {
		t.Error("no skills must add no banner line")
	}
	list := []skills.Skill{{Name: "a", Scope: "personal"}, {Name: "b", Scope: "project"}}
	line := skillBannerLine(list)
	if !strings.Contains(line, "a") || !strings.Contains(line, "b [project]") {
		t.Errorf("banner = %q", line)
	}
}

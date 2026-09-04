package instructions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func realTemp(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func labelsOf(files []File) string {
	return strings.Join(Labels(files), "|")
}

// TestVendorFileNames: every convention a repository might already
// carry is read, including GEMINI.md — the native one for this agent.
func TestVendorFileNames(t *testing.T) {
	home := realTemp(t)
	proj := filepath.Join(home, "proj")
	for _, name := range []string{"AGENTS.md", "AGENT.md", "CLAUDE.md", "GEMINI.md"} {
		write(t, filepath.Join(proj, name), "rules from "+name)
	}
	files, notes := Load(proj, home, "", DefaultLimits())
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if got := labelsOf(files); got != "AGENTS.md|AGENT.md|CLAUDE.md|GEMINI.md" {
		t.Errorf("labels = %q", got)
	}
}

// TestAncestorWalk is the drop-in requirement: workspace-wide rules in a
// parent directory apply to projects beneath it, and the nearest file is
// injected last so it reads as the most specific.
func TestAncestorWalk(t *testing.T) {
	home := realTemp(t)
	workspace := filepath.Join(home, "works", "org")
	proj := filepath.Join(workspace, "series", "tool")
	write(t, filepath.Join(home, "CLAUDE.md"), "home rules")
	write(t, filepath.Join(workspace, "CLAUDE.md"), "org rules")
	write(t, filepath.Join(proj, "AGENTS.md"), "project rules")

	files, _ := Load(proj, home, "", DefaultLimits())
	if len(files) != 3 {
		t.Fatalf("files = %v", labelsOf(files))
	}
	if files[0].Content != "home rules" || files[2].Content != "project rules" {
		t.Errorf("order is not outermost-first: %v", labelsOf(files))
	}
	// Ancestor labels show how far up the rules came from.
	if !strings.HasPrefix(files[1].Label, "..") {
		t.Errorf("ancestor label = %q, want a relative path", files[1].Label)
	}
	rendered := Render(files)
	if strings.Index(rendered, "org rules") > strings.Index(rendered, "project rules") {
		t.Error("rendered order should end with the nearest rules")
	}
}

// TestWalkStopsAtHome: an instruction file is trusted as instructions,
// so gem-agent must not pick one up from a location the operator does
// not own (a shared /tmp being the classic case).
func TestWalkStopsAtHome(t *testing.T) {
	root := realTemp(t)
	home := filepath.Join(root, "home")
	outside := filepath.Join(root, "shared")
	proj := filepath.Join(outside, "checkout")
	write(t, filepath.Join(root, "AGENTS.md"), "attacker rules")
	write(t, filepath.Join(outside, "AGENTS.md"), "shared rules")
	write(t, filepath.Join(proj, "AGENTS.md"), "project rules")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}

	files, _ := Load(proj, home, "", DefaultLimits())
	if len(files) != 1 || files[0].Content != "project rules" {
		t.Errorf("outside home, only the project's own file may load: %v", labelsOf(files))
	}
}

func TestGlobalInstructionsComeFirst(t *testing.T) {
	home := realTemp(t)
	global := filepath.Join(home, ".config", "gem-agent")
	proj := filepath.Join(home, "proj")
	write(t, filepath.Join(global, "AGENTS.md"), "global rules")
	write(t, filepath.Join(proj, "AGENTS.md"), "project rules")

	files, _ := Load(proj, home, global, DefaultLimits())
	if len(files) != 2 || files[0].Content != "global rules" {
		t.Fatalf("global file should be injected first: %v", labelsOf(files))
	}
	if !strings.Contains(files[0].Label, ".config/gem-agent") {
		t.Errorf("global label = %q", files[0].Label)
	}
}

func TestDuplicateContentInjectedOnce(t *testing.T) {
	home := realTemp(t)
	proj := filepath.Join(home, "proj")
	write(t, filepath.Join(proj, "AGENTS.md"), "same rules")
	write(t, filepath.Join(proj, "CLAUDE.md"), "same rules")

	files, _ := Load(proj, home, "", DefaultLimits())
	if len(files) != 1 {
		t.Errorf("identical content should be injected once: %v", labelsOf(files))
	}
}

func TestEmptyAndMissingFilesSkipped(t *testing.T) {
	home := realTemp(t)
	proj := filepath.Join(home, "proj")
	write(t, filepath.Join(proj, "AGENTS.md"), "   \n\n")
	files, notes := Load(proj, home, "", DefaultLimits())
	if len(files) != 0 || len(notes) != 0 {
		t.Errorf("blank file should be skipped silently: %v %v", labelsOf(files), notes)
	}
}

func TestLimits(t *testing.T) {
	home := realTemp(t)
	proj := filepath.Join(home, "proj")
	big := strings.Repeat("x", 4000)
	write(t, filepath.Join(proj, "AGENTS.md"), big)
	write(t, filepath.Join(proj, "CLAUDE.md"), strings.Repeat("y", 4000))

	files, notes := Load(proj, home, "", Limits{PerFileBytes: 1000, TotalBytes: 1200})
	total := 0
	for _, f := range files {
		total += len(f.Content)
	}
	if total > 1200+200 { // +200 allows for the truncation markers
		t.Errorf("total injected %d exceeds the budget", total)
	}
	if len(notes) == 0 {
		t.Error("truncation or skipping should be reported")
	}
}

func TestRenderEmpty(t *testing.T) {
	if Render(nil) != "" {
		t.Error("no files should render nothing")
	}
}

// Review after v0.68.2 (found in the same sweep as load_skill): an
// instruction file enters the system prompt unwrapped, so a link
// planted where AGENTS.md should be must not read content from
// outside its directory; a link to a sibling still resolves.
func TestInstructionLinksStayInTheirDirectory(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "evil.md"), []byte("OUTSIDE RULES"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "evil.md"), filepath.Join(project, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "GEMINI.md"), []byte("inside rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("GEMINI.md", filepath.Join(project, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}
	files, notes := Load(project, "", "", DefaultLimits())
	for _, f := range files {
		if strings.Contains(f.Content, "OUTSIDE") {
			t.Fatalf("an instruction file was read through a link out of the project: %+v", f)
		}
	}
	found := false
	for _, f := range files {
		if f.Content == "inside rules" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the sibling link / regular file was not loaded: %+v", files)
	}
	if !strings.Contains(strings.Join(notes, "\n"), "not read") {
		t.Errorf("the refused link was not noted: %v", notes)
	}
}

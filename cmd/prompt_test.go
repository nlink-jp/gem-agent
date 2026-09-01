package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemPromptShape(t *testing.T) {
	sys := buildSystemPrompt("/tmp/proj", "", "")

	// The defensive framing must stay at the very top, ahead of
	// anything a project could put in front of it.
	if !strings.HasPrefix(sys, "SECURITY, read first:") {
		t.Error("defensive instructions must lead the prompt")
	}
	// The prompt is a template: the agent expands {{DATA_TAG}} with a
	// fresh nonce every LLM call.
	if !strings.Contains(sys, "{{DATA_TAG}}") {
		t.Error("prompt must carry the data-tag placeholder")
	}
	if !strings.Contains(sys, "/tmp/proj") {
		t.Error("prompt must name the project directory")
	}
}

func TestSystemPromptAppendsProjectContext(t *testing.T) {
	sys := buildSystemPrompt("/tmp/proj", "", "\n\nProject instructions:\n\n### AGENTS.md\n\nbuild with make")
	if !strings.Contains(sys, "build with make") {
		t.Error("project context not appended")
	}
	if strings.Index(sys, "SECURITY, read first:") > strings.Index(sys, "build with make") {
		t.Error("project context must come after the defensive framing")
	}
}

// TestLoadInstructionsReadsVendorFiles wires the loader to a real
// directory: the names and the ancestor walk are covered in
// internal/instructions, this checks the cmd-level seam.
func TestLoadInstructionsReadsVendorFiles(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	// Build the project under the real home so the ancestor walk (which
	// stops at home) reaches it.
	base, err := os.MkdirTemp(home, "gem-agent-test-")
	if err != nil {
		t.Skip("cannot create a temp dir under home")
	}
	defer func() { _ = os.RemoveAll(base) }()
	proj := filepath.Join(base, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "GEMINI.md"), []byte("gemini rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "CLAUDE.md"), []byte("workspace rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	section, labels, notes := loadInstructions(proj, true)
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	if !strings.Contains(section, "gemini rules") || !strings.Contains(section, "workspace rules") {
		t.Errorf("section missing content: %q", section)
	}
	if len(labels) < 2 {
		t.Errorf("labels = %v, want the ancestor file and the project file", labels)
	}
	// Nearest last: the project's own file is the final section.
	if strings.Index(section, "workspace rules") > strings.Index(section, "gemini rules") {
		t.Error("project rules should come after ancestor rules")
	}
}

// A capability nothing points at never gets used: without the section
// the model has no way to learn the directory exists, and keeps putting
// intermediates in the project (ADR-0058).
func TestSystemPromptNamesTheWorkDirectory(t *testing.T) {
	work := "/state/gem-agent/proj/work/sess-1"
	got := buildSystemPrompt("/proj", work, "")
	if !strings.Contains(got, work) {
		t.Error("the work directory is not named in the prompt")
	}
	// The literal path, not the variable: an MCP tool argument is JSON
	// the model writes, and nothing expands a variable on the way.
	if !strings.Contains(got, "$GEMAGENT_WORK_DIR") {
		t.Error("shell commands are not told the variable exists")
	}
	if i, j := strings.Index(got, work), strings.Index(got, "$GEMAGENT_WORK_DIR"); i < 0 || j < 0 || i > j {
		t.Error("the literal path should be given before the variable is mentioned")
	}
}

func TestSystemPromptOmitsTheSectionWithoutAWorkDirectory(t *testing.T) {
	got := buildSystemPrompt("/proj", "", "")
	if strings.Contains(got, "Session work directory") {
		t.Error("a session with no work directory should not be told it has one")
	}
}

// A capability the workflow never points at never fires: 75 sessions /
// 788 tool calls produced zero spontaneous agentic_file_search
// delegations while this prompt prescribed the manual list/search/read
// loop by name and never mentioned the tool (ADR-0062). The delegation
// guidance must exist, keep its concrete trigger, and come BEFORE the
// self-navigation guidance.
func TestSystemPromptDelegatesExplorationFirst(t *testing.T) {
	sys := buildSystemPrompt("/proj", "", "")
	di := strings.Index(sys, "agentic_file_search")
	if di < 0 {
		t.Fatal("the system prompt does not name agentic_file_search")
	}
	ni := strings.Index(sys, "Navigate yourself")
	if ni < 0 {
		t.Fatal("the manual-navigation guidance is missing")
	}
	if di > ni {
		t.Error("delegation guidance must precede the manual-navigation guidance")
	}
	// The trigger stays concrete — a vague recommendation next to
	// specific manual guidance reads as "rarely" (the v0.39.0 lesson).
	if !strings.Contains(sys, "without waiting to be asked") {
		t.Error("the delegation trigger lost its unprompted-use clause")
	}
}

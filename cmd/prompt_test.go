package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectContextInjection(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("# proj\nBuild with make."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("Always answer in Japanese."), 0o644); err != nil {
		t.Fatal(err)
	}

	sys := buildSystemPrompt(dir)
	if !strings.Contains(sys, "### AGENTS.md") || !strings.Contains(sys, "Build with make.") {
		t.Error("AGENTS.md not injected")
	}
	if !strings.Contains(sys, "### CLAUDE.md") || !strings.Contains(sys, "Always answer in Japanese.") {
		t.Error("CLAUDE.md not injected")
	}
	if strings.Index(sys, "### AGENTS.md") > strings.Index(sys, "### CLAUDE.md") {
		t.Error("AGENTS.md should come first")
	}
	// The defensive framing must stay at the very top, before any
	// project content.
	if !strings.HasPrefix(sys, "SECURITY, read first:") {
		t.Error("defensive instructions must lead the prompt")
	}
}

func TestProjectContextAbsent(t *testing.T) {
	sys := buildSystemPrompt(t.TempDir())
	if strings.Contains(sys, "Project instructions") {
		t.Error("no context files -> no context section")
	}
}

func TestProjectContextEmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("   \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := loadProjectContext(dir); got != "" {
		t.Errorf("blank file should be skipped: %q", got)
	}
}

func TestProjectContextTruncation(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", contextFileCap+1000)
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	got := loadProjectContext(dir)
	if !strings.Contains(got, "[truncated") {
		t.Error("oversized file should carry a truncation marker")
	}
	if len(got) > contextFileCap+500 {
		t.Errorf("truncated section still too large: %d", len(got))
	}
}

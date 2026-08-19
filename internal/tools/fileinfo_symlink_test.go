package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0021: file_info's escaping-symlink report only fires when the
// path's PARENT genuinely resolves inside the project. A lexically
// in-project path under an escaping link must not leak out-of-project
// link targets.
func TestFileInfoNoLeakThroughIntermediateSymlink(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()
	// outside/secret-link is a symlink whose target name would leak.
	if err := os.Symlink("/nonexistent/target-name", filepath.Join(outside, "secret-link")); err != nil {
		t.Fatal(err)
	}
	// project/escape -> outside (the model can create this via shell).
	if err := os.Symlink(outside, filepath.Join(project, "escape")); err != nil {
		t.Fatal(err)
	}
	reg, err := New(project, directExec, 5e9)
	if err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("file_info")
	out, err := tool.Run(context.Background(), map[string]any{"path": "escape/secret-link"})
	report := out
	if err != nil {
		report = err.Error()
	}
	if strings.Contains(report, "/nonexistent") || strings.Contains(report, "target-name") {
		t.Errorf("out-of-project link target leaked: %q", report)
	}
	if strings.Contains(report, "symlink →") {
		t.Errorf("intermediate-symlink path produced a link report: %q", report)
	}

	// The legitimate case still works: a direct in-project link that
	// escapes is reported (target string only), not a dead end.
	if err := os.Symlink(outside, filepath.Join(project, "direct-escape")); err != nil {
		t.Fatal(err)
	}
	out, err = tool.Run(context.Background(), map[string]any{"path": "direct-escape"})
	if err != nil || out == "" {
		t.Errorf("direct escaping symlink should be a reportable fact: out=%q err=%v", out, err)
	}
}

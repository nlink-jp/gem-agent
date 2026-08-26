package agent

// ADR-0051: the floors against summarizing overwrites that live at the
// agent layer — the approval detail carries write_file's replacement
// annotation, and the compaction stand-in warns that file contents are
// no longer verbatim.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

func TestDescribeCarriesTheWriteAnnotation(t *testing.T) {
	a, reg := newAgent(t, &mockBackend{}, &approveAll{}, 5)
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "sop.md"),
		[]byte(strings.Repeat("x", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}

	detail, _ := a.Describe(llm.ToolCall{Name: "write_file",
		Args: map[string]any{"path": "sop.md", "content": "short"}})
	if !strings.Contains(detail, "replaces existing file: 4KB → 5B") {
		t.Errorf("detail lacks the replacement annotation: %q", detail)
	}

	// A new file annotates nothing — the detail stays single-line.
	detail, _ = a.Describe(llm.ToolCall{Name: "write_file",
		Args: map[string]any{"path": "fresh.md", "content": "short"}})
	if strings.Contains(detail, "replaces existing file") {
		t.Errorf("new-file detail carries a replacement annotation: %q", detail)
	}
}

func TestSummaryMessageWarnsFileContentsAreStale(t *testing.T) {
	m := SummaryMessage("the summary body")
	if !strings.Contains(m.Content, "no longer verbatim") {
		t.Errorf("framing lacks the staleness warning: %q", m.Content)
	}
	// The warning is gem-agent's trusted framing, never part of the
	// untrusted attached summary.
	if strings.Contains(m.Attachments[0].Content, "no longer verbatim") {
		t.Error("staleness warning leaked into the attachment")
	}
}

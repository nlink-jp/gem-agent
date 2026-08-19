package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ADR-0021: a file starting with blank lines must not shift the
// near-miss diagnosis — the quoted snippet and line number must name
// the real near-match region.
func TestNearMissAlignedWithLeadingBlankLines(t *testing.T) {
	content := "\n\nline three\nline four\n\tfunc target() {\nline six\n"
	line, snippet, ok := nearMiss(content, "func target() {")
	if !ok {
		t.Fatal("near miss not found")
	}
	if line != 5 {
		t.Errorf("near miss reported line %d, want 5", line)
	}
	if !strings.Contains(snippet, "func target() {") {
		t.Errorf("snippet quotes the wrong region: %q", snippet)
	}
}

// read_file's window note must report the file's real line count, and
// a window on the phantom line past the end must be refused.
func TestSliceLinesCountsRealLines(t *testing.T) {
	content := "a\nb\n" // two lines, newline-terminated
	got, note, err := sliceLines(content, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "lines 1–2 of 2") {
		t.Errorf("note = %q, want 'of 2' (not the phantom third line)", note)
	}
	if got != "a\nb" {
		t.Errorf("window = %q", got)
	}
	if _, _, err := sliceLines(content, 3, 3); err == nil {
		t.Error("start_line 3 of a 2-line file was accepted")
	}
}

// edit_file's success header must not extend the reported span by one
// line when the replacement ends with a newline.
func TestEditReportSpanExcludesTrailingNewline(t *testing.T) {
	dir := t.TempDir()
	reg, err := New(dir, directExec, 5e9)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("edit_file")
	out, err := tool.Run(context.Background(), map[string]any{
		"path":       "f.txt",
		"old_string": "two\n",
		"new_string": "TWO\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "lines 2–2") {
		t.Errorf("report = %q, want the span to stay on line 2", out)
	}
}

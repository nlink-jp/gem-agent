package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func editProject(t *testing.T, content string) (*Registry, string) {
	t.Helper()
	r := newRegistry(t)
	path := filepath.Join(r.ProjectDir(), "f.go")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return r, path
}

const editSrc = `package main

func start() {
	retries := 3
	launch(retries)
}

func stop() {
	retries := 3
	halt(retries)
}
`

func TestEditFileSingleFormUnchanged(t *testing.T) {
	r, path := editProject(t, editSrc)
	out, err := run(t, r, "edit_file", map[string]any{
		"path": "f.go", "old_string": "launch(retries)", "new_string": "launch(retries + 1)",
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "launch(retries + 1)") {
		t.Errorf("edit not applied:\n%s", data)
	}
	// Success carries evidence — the changed region with its span — so
	// verification does not need a read-back round.
	if !strings.Contains(out, "lines 5–5 now read:") || !strings.Contains(out, "launch(retries + 1)") {
		t.Errorf("report lacks evidence: %q", out)
	}
}

// The batch is sequential (each edit sees the previous output) and
// atomic (any failure writes nothing).
func TestEditFileBatchSequentialAndAtomic(t *testing.T) {
	r, path := editProject(t, editSrc)
	out, err := run(t, r, "edit_file", map[string]any{
		"path": "f.go",
		"edits": []any{
			map[string]any{"old_string": "launch(retries)", "new_string": "launch(retries * 2)"},
			// Sees edit 1's output:
			map[string]any{"old_string": "launch(retries * 2)\n}", "new_string": "launch(retries * 2)\n\tlog()\n}"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "launch(retries * 2)\n\tlog()") {
		t.Errorf("sequential batch failed:\n%s", data)
	}
	if !strings.Contains(out, "2 edit(s)") {
		t.Errorf("report = %q", out)
	}

	// Atomicity: first edit would apply, second cannot — nothing written.
	before, _ := os.ReadFile(path)
	_, err = run(t, r, "edit_file", map[string]any{
		"path": "f.go",
		"edits": []any{
			map[string]any{"old_string": "func stop()", "new_string": "func stopAll()"},
			map[string]any{"old_string": "NO SUCH TEXT", "new_string": "x"},
		},
	})
	if err == nil {
		t.Fatal("a failing batch reported success")
	}
	if !strings.Contains(err.Error(), "edit 2") || !strings.Contains(err.Error(), "file unchanged") {
		t.Errorf("error must name the failing edit and the atomicity: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a failed batch left a half-applied file")
	}
}

func TestEditFileReplaceAllAndUniquenessDiagnosis(t *testing.T) {
	r, path := editProject(t, editSrc)
	// "retries := 3" appears twice: the uniqueness error lists the lines.
	_, err := run(t, r, "edit_file", map[string]any{
		"path": "f.go", "old_string": "retries := 3", "new_string": "retries := 5",
	})
	if err == nil || !strings.Contains(err.Error(), "lines 4, 9") {
		t.Fatalf("uniqueness error must list occurrence lines: %v", err)
	}
	// replace_all is the explicit opt-out, and reports the count.
	out, err := run(t, r, "edit_file", map[string]any{
		"path": "f.go", "old_string": "retries := 3", "new_string": "retries := 5", "replace_all": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "replaced 2 occurrence(s)") {
		t.Errorf("report = %q", out)
	}
	data, _ := os.ReadFile(path)
	if strings.Count(string(data), "retries := 5") != 2 {
		t.Errorf("replace_all did not:\n%s", data)
	}
}

// The way models actually miss: indentation drift. The diagnosis quotes
// the file's REAL text so the fix is a copy-paste, not a re-read.
func TestEditFileMissDiagnosisQuotesTheRealText(t *testing.T) {
	r, _ := editProject(t, editSrc)
	_, err := run(t, r, "edit_file", map[string]any{
		"path":       "f.go",
		"old_string": "func start() {\n    retries := 3\n    launch(retries)", // spaces, file has tabs
		"new_string": "x",
	})
	if err == nil {
		t.Fatal("a whitespace miss applied?!")
	}
	msg := err.Error()
	if !strings.Contains(msg, "whitespace differs") || !strings.Contains(msg, "line 3") {
		t.Errorf("no near-miss diagnosis: %v", msg)
	}
	if !strings.Contains(msg, "\tretries := 3") {
		t.Errorf("diagnosis must quote the real (tab-indented) text: %v", msg)
	}

	// A genuine miss says so and names the recovery.
	_, err = run(t, r, "edit_file", map[string]any{
		"path": "f.go", "old_string": "completely absent text", "new_string": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "start_line") {
		t.Errorf("genuine miss should point at windowed reads: %v", err)
	}
}

func TestEditFileArgValidation(t *testing.T) {
	r, _ := editProject(t, editSrc)
	cases := []map[string]any{
		{"path": "f.go", "old_string": "x", "new_string": "x"}, // no-op
		{"path": "f.go", "old_string": "", "new_string": "x"},  // empty anchor
		{"path": "f.go"}, // nothing
		{"path": "f.go", "old_string": "a", "new_string": "b", "edits": []any{}}, // both forms
		{"path": "f.go", "edits": []any{}},                                       // empty batch
		{"path": "f.go", "edits": []any{map[string]any{"old_string": "same", "new_string": "same"}}},
	}
	for i, args := range cases {
		if _, err := run(t, r, "edit_file", args); err == nil {
			t.Errorf("case %d accepted: %v", i, args)
		}
	}
	// Confinement unchanged.
	if _, err := run(t, r, "edit_file", map[string]any{"path": "../x", "old_string": "a", "new_string": "b"}); err == nil {
		t.Error("edit_file escaped the project")
	}
}

package tools

// Regression: the batch form and the single form must reject the same
// malformed replacement. The batch form used to read new_string with a
// discarded type assertion, so a missing or non-string value became ""
// and the edit deleted old_string's text (review 2026-09-08, F-02).

import (
	"os"
	"strings"
	"testing"
)

// badNewString enumerates the ways new_string can fail to be a string.
var badNewString = []struct {
	name string
	edit map[string]any // the per-edit object, shared by both forms
}{
	{"missing", map[string]any{"old_string": "launch(retries)"}},
	{"null", map[string]any{"old_string": "launch(retries)", "new_string": nil}},
	{"number", map[string]any{"old_string": "launch(retries)", "new_string": 42.0}},
	{"object", map[string]any{"old_string": "launch(retries)", "new_string": map[string]any{}}},
}

func TestEditFileBatchRejectsMalformedNewString(t *testing.T) {
	for _, tc := range badNewString {
		t.Run(tc.name, func(t *testing.T) {
			r, path := editProject(t, editSrc)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			out, err := run(t, r, "edit_file", map[string]any{
				"path":  "f.go",
				"edits": []any{tc.edit},
			})
			if err == nil {
				t.Fatalf("accepted %v: %q", tc.edit, out)
			}
			if !strings.Contains(err.Error(), "new_string") {
				t.Errorf("error does not name the argument: %v", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != string(before) {
				t.Errorf("file changed by a rejected edit:\n%s", after)
			}
		})
	}
}

// The two forms are one parse path: whatever one rejects, the other
// rejects too.
func TestEditFileFormsAgreeOnMalformedNewString(t *testing.T) {
	for _, tc := range badNewString {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := editProject(t, editSrc)
			single := map[string]any{"path": "f.go"}
			for k, v := range tc.edit {
				single[k] = v
			}
			if _, err := run(t, r, "edit_file", single); err == nil {
				t.Fatalf("single form accepted %v", tc.edit)
			}
		})
	}
}

// A rejected edit anywhere in the batch leaves the file untouched — the
// valid edits before it are not applied either.
func TestEditFileBatchMalformedEditIsAtomic(t *testing.T) {
	r, path := editProject(t, editSrc)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "edit_file", map[string]any{
		"path": "f.go",
		"edits": []any{
			map[string]any{"old_string": "launch(retries)", "new_string": "launch(0)"},
			map[string]any{"old_string": "halt(retries)"}, // no new_string
		},
	}); err == nil {
		t.Fatal("accepted a batch with a malformed edit")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the valid edit was applied anyway:\n%s", after)
	}
}

// Deletion stays available — it is new_string passed as "", which is
// what the rejection above distinguishes a missing argument from.
func TestEditFileEmptyNewStringStillDeletes(t *testing.T) {
	r, path := editProject(t, editSrc)
	if _, err := run(t, r, "edit_file", map[string]any{
		"path":  "f.go",
		"edits": []any{map[string]any{"old_string": "\tlaunch(retries)\n", "new_string": ""}},
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "launch(retries)") {
		t.Errorf("deletion did not apply:\n%s", data)
	}
}

// replace_all is a flag, so a wrong type has to fail rather than fall
// back to false: silently requiring uniqueness would report "appears N
// times" for a call that asked for every occurrence.
func TestEditFileRejectsNonBooleanReplaceAll(t *testing.T) {
	r, _ := editProject(t, editSrc)
	for _, form := range []map[string]any{
		{"path": "f.go", "old_string": "retries", "new_string": "tries", "replace_all": "yes"},
		{"path": "f.go", "edits": []any{
			map[string]any{"old_string": "retries", "new_string": "tries", "replace_all": "yes"},
		}},
	} {
		_, err := run(t, r, "edit_file", form)
		if err == nil {
			t.Errorf("accepted a non-boolean replace_all: %v", form)
			continue
		}
		// Not "appears N times": that is the uniqueness check firing on
		// a silent fallback to false, which is the bug, not the fix.
		if !strings.Contains(err.Error(), "replace_all") {
			t.Errorf("error does not name replace_all: %v", err)
		}
	}
}

// edits must be an array; a scalar or object under that key is a
// malformed call, not an empty batch.
func TestEditFileRejectsNonArrayEdits(t *testing.T) {
	r, _ := editProject(t, editSrc)
	for _, v := range []any{"launch", 3.0, map[string]any{"old_string": "a", "new_string": "b"}} {
		_, err := run(t, r, "edit_file", map[string]any{"path": "f.go", "edits": v})
		if err == nil {
			t.Errorf("accepted edits=%v", v)
			continue
		}
		// Not "old_string is required": that is the single form taking
		// over because the assertion to []any failed silently.
		if !strings.Contains(err.Error(), "edits must be an array") {
			t.Errorf("edits=%v reported as a single-form error: %v", v, err)
		}
	}
}

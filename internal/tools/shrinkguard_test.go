package tools

// The write_file shrink guard and annotation (ADR-0051): overwriting a
// sizeable existing file with much smaller content is refused unless
// the shrink is declared, and the approval annotation names what an
// overwrite replaces.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFile writes a file of n bytes under the registry's project dir.
func seedFile(t *testing.T, r *Registry, name string, n int) string {
	t.Helper()
	abs := filepath.Join(r.ProjectDir(), name)
	if err := os.WriteFile(abs, []byte(strings.Repeat("x", n)), 0o644); err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestShrinkGuardRefusesAndLeavesFileUntouched(t *testing.T) {
	r := newRegistry(t)
	abs := seedFile(t, r, "sop.md", 4096)

	_, err := run(t, r, "write_file", map[string]any{"path": "sop.md", "content": strings.Repeat("y", 1000)})
	if err == nil {
		t.Fatal("shrinking overwrite succeeded, want refusal")
	}
	for _, want := range []string{"4KB", "1000B", "edit_file", "allow_shrink", "file unchanged"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err.Error(), want)
		}
	}
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) != 4096 || data[0] != 'x' {
		t.Fatalf("refused write still changed the file (%d bytes)", len(data))
	}
}

func TestShrinkGuardHonoursDeclaredIntent(t *testing.T) {
	r := newRegistry(t)
	abs := seedFile(t, r, "sop.md", 4096)

	out, err := run(t, r, "write_file", map[string]any{
		"path": "sop.md", "content": "replaced", "allow_shrink": true,
	})
	if err != nil {
		t.Fatalf("allow_shrink overwrite failed: %v", err)
	}
	if !strings.Contains(out, "wrote 8 bytes") {
		t.Errorf("unexpected result: %q", out)
	}
	data, _ := os.ReadFile(abs)
	if string(data) != "replaced" {
		t.Errorf("file content = %q, want %q", data, "replaced")
	}
}

func TestShrinkGuardScope(t *testing.T) {
	cases := []struct {
		name    string
		oldSize int
		content string
		ok      bool
	}{
		{"small files shrink freely", 1000, "tiny", true},
		{"growth is never guarded", 4096, strings.Repeat("y", 8000), true},
		{"exactly 70 percent passes", 4000, strings.Repeat("y", 2800), true},
		{"just under 70 percent is refused", 4000, strings.Repeat("y", 2799), false},
		{"at the size floor the guard is armed", shrinkGuardMinBytes, "s", false},
		{"below the size floor it is not", shrinkGuardMinBytes - 1, "s", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRegistry(t)
			seedFile(t, r, "f.md", tc.oldSize)
			_, err := run(t, r, "write_file", map[string]any{"path": "f.md", "content": tc.content})
			if tc.ok && err != nil {
				t.Fatalf("want success, got %v", err)
			}
			if !tc.ok && (err == nil || !strings.Contains(err.Error(), "allow_shrink")) {
				t.Fatalf("want shrink refusal, got %v", err)
			}
		})
	}
}

func TestShrinkGuardIgnoresNewFiles(t *testing.T) {
	r := newRegistry(t)
	if _, err := run(t, r, "write_file", map[string]any{"path": "fresh.md", "content": "a"}); err != nil {
		t.Fatalf("new-file write failed: %v", err)
	}
}

func TestWriteFileAnnotateNamesTheReplacement(t *testing.T) {
	r := newRegistry(t)
	seedFile(t, r, "sop.md", 4096)
	tool, _ := r.Get("write_file")
	if tool.Annotate == nil {
		t.Fatal("write_file has no Annotate hook")
	}

	note := tool.Annotate(map[string]any{"path": "sop.md", "content": strings.Repeat("y", 1024)})
	if note != "replaces existing file: 4KB → 1KB" {
		t.Errorf("annotation = %q", note)
	}
	// New files, directories, and escaping paths annotate nothing.
	for name, args := range map[string]map[string]any{
		"new file":  {"path": "fresh.md", "content": "a"},
		"directory": {"path": ".", "content": "a"},
		"escape":    {"path": "../outside", "content": "a"},
		"no path":   {"content": "a"},
	} {
		if got := tool.Annotate(args); got != "" {
			t.Errorf("%s: annotation = %q, want empty", name, got)
		}
	}
}

func TestSizeLabel(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0B", 512: "512B", 2048: "2KB", 43_000: "41KB", 2 << 20: "2.0MB",
	} {
		if got := sizeLabel(n); got != want {
			t.Errorf("sizeLabel(%d) = %q, want %q", n, got, want)
		}
	}
}

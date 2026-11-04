package tools

// write_file's `from` (ADR-0105 §2): a whole-file copy, byte for byte,
// judged like any write, refused rather than truncated.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sha(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// A saved MCP result in the work directory is copied into the project
// exactly, and the result line reports the hash of what landed.
func TestWriteFileFromCopiesASavedResultByteForByte(t *testing.T) {
	r, work := newRegistryWithWork(t)
	body := []byte("{\"rows\":[{\"bytes_out\":452631160}]}\n\x00\xff tail")
	src := filepath.Join(work, "bq-query-x.json")
	if err := os.WriteFile(src, body, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "write_file", map[string]any{"path": "evidence/rows.json", "from": src})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(r.ProjectDir(), "evidence", "rows.json"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("copy = %q, %v", got, err)
	}
	for _, want := range []string{"copied", sha(body), src, "evidence/rows.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("result %q does not name %q", out, want)
		}
	}
}

// A source larger than read_file's window is copied whole: the copy does
// not borrow any reader that cuts.
func TestWriteFileFromCopiesPastTheReadWindow(t *testing.T) {
	r := newRegistry(t)
	body := []byte(strings.Repeat("0123456789abcdef", (readCap/16)*2+7))
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "big.txt"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "write_file", map[string]any{"path": "copy.txt", "from": "big.txt"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(r.ProjectDir(), "copy.txt"))
	if sha(got) != sha(body) {
		t.Errorf("copied %d bytes, want all %d", len(got), len(body))
	}
}

func TestWriteFileFromRefusals(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(dir, "huge.bin")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(CopySourceCap + 1); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	_ = os.WriteFile(outside, []byte("o"), 0o644)

	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"both":      {map[string]any{"path": "x.txt", "content": "c", "from": "a.txt"}, "not both"},
		"neither":   {map[string]any{"path": "x.txt"}, "content is required"},
		"directory": {map[string]any{"path": "x.txt", "from": "sub"}, "not a regular file"},
		"too large": {map[string]any{"path": "x.txt", "from": "huge.bin"}, "copy limit"},
		"outside":   {map[string]any{"path": "x.txt", "from": outside}, "escapes"},
		"missing":   {map[string]any{"path": "x.txt", "from": "nope.txt"}, "no such file"},
	} {
		_, err := run(t, r, "write_file", tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %q", name, err, tc.want)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "x.txt")); statErr == nil {
			t.Fatalf("%s: a refused copy wrote the destination", name)
		}
	}
}

// Credential material is never copied, and the refusal is not the
// covered reads' ErrCredentialRead: that one opens an operator question
// whose yes runs the call in process (ADR-0086 §2), and a copy must
// never be one approval away.
func TestWriteFileFromRefusesCredentialMaterial(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), ".env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".env", filepath.Join(r.ProjectDir(), "notes.txt")); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{".env", "notes.txt"} {
		_, err := run(t, r, "write_file", map[string]any{"path": "out.txt", "from": from})
		if err == nil || !strings.Contains(err.Error(), "credential") || errors.Is(err, ErrCredentialRead) {
			t.Errorf("from %s: err = %v", from, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.ProjectDir(), "out.txt")); err == nil {
		t.Error("credential material was copied")
	}
	// Through a child that reports the kernel's refusal, the same.
	r.SetFileChild(func(context.Context, string, map[string]any) (string, error) { return "", ErrCredentialRead })
	_, err := run(t, r, "write_file", map[string]any{"path": "out.txt", "from": "plain.txt"})
	if err == nil || errors.Is(err, ErrCredentialRead) {
		t.Errorf("child refusal: err = %v, want a plain refusal", err)
	}
}

// With a child installed the source is read through it, by the copy
// operation, and its bytes are what lands.
func TestWriteFileFromReadsThroughTheChild(t *testing.T) {
	r := newRegistry(t)
	var gotTool string
	r.SetFileChild(func(_ context.Context, tool string, args map[string]any) (string, error) {
		gotTool = tool
		return "from the child", nil
	})
	if _, err := run(t, r, "write_file", map[string]any{"path": "c.txt", "from": "anything.txt"}); err != nil {
		t.Fatal(err)
	}
	if gotTool != CopySourceOp {
		t.Errorf("child ran %q, want %q", gotTool, CopySourceOp)
	}
	if got, _ := os.ReadFile(filepath.Join(r.ProjectDir(), "c.txt")); string(got) != "from the child" {
		t.Errorf("copy = %q", got)
	}
}

// The shrink guard compares the source's size: copying a small file
// over a large one is the same destruction as writing small content.
func TestWriteFileFromKeepsTheShrinkGuard(t *testing.T) {
	r := newRegistry(t)
	seedFile(t, r, "report.md", 4096)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "small.txt"), []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := run(t, r, "write_file", map[string]any{"path": "report.md", "from": "small.txt"})
	if err == nil || !strings.Contains(err.Error(), "allow_shrink") {
		t.Errorf("err = %v, want the shrink guard", err)
	}
}

// The detail of a copy names the source and its size — never its bytes:
// Annotate runs for every call and its text reaches the activity trace
// and the audit telemetry (ADR-0105 §2, independent review).
func TestWriteFileFromAnnotateNamesTheSource(t *testing.T) {
	r := newRegistry(t)
	seedFile(t, r, "AGENTS.md", 4096)
	body := []byte("first line of the rules\nsecond")
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "src.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	tool, _ := r.Get("write_file")
	note := tool.Annotate(map[string]any{"path": "AGENTS.md", "from": "src.md"})
	for _, want := range []string{"copies src.md (30B)", "replaces existing file: 4KB → 30B"} {
		if !strings.Contains(note, want) {
			t.Errorf("annotation %q does not show %q", note, want)
		}
	}
	if strings.Contains(note, "first line") {
		t.Errorf("annotation carries the source's bytes: %q", note)
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), ".env"), []byte("TOKEN=x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := tool.Annotate(map[string]any{"path": "x", "from": ".env"}); got != "" {
		t.Errorf("credential source annotated: %q", got)
	}
}

// An empty content beside from is a model filling every field: the copy
// proceeds.
func TestWriteFileFromWithEmptyContentCopies(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "write_file", map[string]any{"path": "b.txt", "content": "", "from": "a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(r.ProjectDir(), "b.txt")); string(got) != "a" {
		t.Errorf("copy = %q", got)
	}
}

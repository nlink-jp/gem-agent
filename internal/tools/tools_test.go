package tools

import (
	"fmt"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func directExec(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "/bin/bash", "-c", command)
}

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := New(t.TempDir(), directExec, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func run(t *testing.T, r *Registry, name string, args map[string]any) (string, error) {
	t.Helper()
	tool, ok := r.Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tool.Run(context.Background(), args)
}

func TestRegistryShape(t *testing.T) {
	r := newRegistry(t)
	want := map[string]bool{ // name → Mutating
		"list_files": false, "list_tree": false, "search_files": false,
		"read_file": false, "file_info": false, "view_image": false,
		"write_file": true, "edit_file": true, "shell_exec": true,
	}
	if len(r.List()) != len(want) {
		t.Fatalf("registered %d tools, want %d", len(r.List()), len(want))
	}
	for name, mutating := range want {
		tool, ok := r.Get(name)
		if !ok {
			t.Fatalf("missing tool %s", name)
		}
		if tool.Mutating != mutating {
			t.Errorf("%s Mutating = %v, want %v", name, tool.Mutating, mutating)
		}
	}
}

func TestRegisterExternalTool(t *testing.T) {
	r := newRegistry(t)
	ext := &Tool{Name: "mcp__x__lookup", Description: "d", Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) { return "ok", nil }}
	if err := r.Register(ext); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get("mcp__x__lookup"); got != ext {
		t.Error("registered tool not retrievable")
	}
	if r.List()[len(r.List())-1] != ext {
		t.Error("registered tool should append to order")
	}
	if err := r.Register(ext); err == nil {
		t.Error("duplicate registration must fail")
	}
	if err := r.Register(&Tool{Name: "read_file"}); err == nil {
		t.Error("collision with built-in must fail")
	}
}

func TestPathConfinement(t *testing.T) {
	r := newRegistry(t)
	outside := t.TempDir()

	for _, p := range []string{
		filepath.Join(outside, "x.txt"), // absolute, outside
		"../escape.txt",                 // traversal
		"/etc/passwd",                   // absolute system path
	} {
		if _, err := run(t, r, "read_file", map[string]any{"path": p}); err == nil {
			t.Errorf("read_file(%q) should be rejected", p)
		}
		if _, err := run(t, r, "write_file", map[string]any{"path": p, "content": "x"}); err == nil {
			t.Errorf("write_file(%q) should be rejected", p)
		}
	}
}

func TestSymlinkEscapeRejected(t *testing.T) {
	r := newRegistry(t)
	outside := t.TempDir()
	link := filepath.Join(r.ProjectDir(), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	// Writing to a new file under a symlinked dir that points outside.
	if _, err := run(t, r, "write_file", map[string]any{"path": "link/evil.txt", "content": "x"}); err == nil {
		t.Error("write through an outside-pointing symlink should be rejected")
	}
	// Reading an existing outside file through the symlink.
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": "link/secret.txt"}); err == nil {
		t.Error("read through an outside-pointing symlink should be rejected")
	}
}

func TestListFiles(t *testing.T) {
	r := newRegistry(t)
	if err := os.MkdirAll(filepath.Join(r.ProjectDir(), "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "list_files", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Errorf("list_files output = %q", out)
	}
}

func TestReadWriteEdit(t *testing.T) {
	r := newRegistry(t)

	if _, err := run(t, r, "write_file", map[string]any{"path": "dir/new.txt", "content": "hello world"}); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": "dir/new.txt"})
	if err != nil || out != "hello world" {
		t.Fatalf("read back = %q, err %v", out, err)
	}

	if _, err := run(t, r, "edit_file", map[string]any{
		"path": "dir/new.txt", "old_string": "world", "new_string": "gem-agent"}); err != nil {
		t.Fatal(err)
	}
	out, _ = run(t, r, "read_file", map[string]any{"path": "dir/new.txt"})
	if out != "hello gem-agent" {
		t.Errorf("after edit = %q", out)
	}

	// old_string not found
	if _, err := run(t, r, "edit_file", map[string]any{
		"path": "dir/new.txt", "old_string": "absent", "new_string": "x"}); err == nil {
		t.Error("edit with absent old_string should fail")
	}
	// ambiguous old_string
	if _, err := run(t, r, "write_file", map[string]any{"path": "dup.txt", "content": "aa aa"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "edit_file", map[string]any{
		"path": "dup.txt", "old_string": "aa", "new_string": "b"}); err == nil || !strings.Contains(err.Error(), "2 times") {
		t.Errorf("ambiguous edit should fail naming the count, got %v", err)
	}
}

func TestReadFileTruncation(t *testing.T) {
	r := newRegistry(t)
	big := strings.Repeat("x", readCap+100)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": "big.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[output truncated") {
		t.Error("large read should carry a truncation marker")
	}
	if len(out) > readCap+200 {
		t.Errorf("truncated output still too large: %d", len(out))
	}
}

func TestShellExec(t *testing.T) {
	r := newRegistry(t)

	out, err := run(t, r, "shell_exec", map[string]any{"command": "echo hello && pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, r.ProjectDir()) {
		t.Errorf("shell_exec output = %q (cwd should be project dir)", out)
	}

	// Non-zero exit status must be surfaced, not swallowed.
	out, err = run(t, r, "shell_exec", map[string]any{"command": "echo failing; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "failing") || !strings.Contains(out, "[exit status 3]") {
		t.Errorf("exit status not surfaced: %q", out)
	}

	// Output truncation.
	out, err = run(t, r, "shell_exec", map[string]any{"command": "yes x | head -c 30000"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[output truncated") {
		t.Error("large shell output should carry a truncation marker")
	}
}

func TestShellExecTimeout(t *testing.T) {
	r, err := New(t.TempDir(), directExec, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "shell_exec", map[string]any{"command": "sleep 5; echo done"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "timed out") {
		t.Errorf("timeout should be reported in the result: %q", out)
	}
}

// --- images (ADR-0012) ---

var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
	0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
	0x00, 0x00, 0x03, 0x00, 0x01, 0xCE, 0xFC, 0x53, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func TestReadImageConfinedAndSniffed(t *testing.T) {
	r := newRegistry(t)
	dir := r.ProjectDir()
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fake.png"), []byte("not a picture"), 0o644); err != nil {
		t.Fatal(err)
	}

	data, mime, err := r.ReadImage("shot.png")
	if err != nil || mime != "image/png" || len(data) != len(tinyPNG) {
		t.Fatalf("ReadImage: %v %q %d", err, mime, len(data))
	}
	// Outside the project: same refusal as every file tool — the
	// model-triggered route gets no out-of-tree exception (ADR-0012).
	if _, _, err := r.ReadImage("../outside.png"); err == nil {
		t.Fatal("ReadImage escaped the project")
	}
	if _, _, err := r.ReadImage("/etc/hosts"); err == nil {
		t.Fatal("ReadImage read an absolute path")
	}
	// A renamed non-image is refused by the sniff.
	if _, _, err := r.ReadImage("fake.png"); err == nil {
		t.Fatal("ReadImage accepted a renamed text file")
	}
}

func TestViewImageToolReturnsMetadataNotBytes(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "s.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	tool, ok := r.Get(ViewImageName)
	if !ok {
		t.Fatal("view_image not registered")
	}
	if tool.Mutating {
		t.Error("view_image must be read-only (usable in one-shot mode)")
	}
	out, err := tool.Run(context.Background(), map[string]any{"path": "s.png"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image attached: s.png") || strings.Contains(out, "\x89PNG") {
		t.Errorf("tool result should be metadata, got %.60q", out)
	}
}

// Feeding a PNG through read_file yields mojibake that helps nobody —
// refuse and point at the tool that works.
func TestReadFileRefusesImages(t *testing.T) {
	r := newRegistry(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "s.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	tool, _ := r.Get("read_file")
	_, err := tool.Run(context.Background(), map[string]any{"path": "s.png"})
	if err == nil || !strings.Contains(err.Error(), "view_image") {
		t.Fatalf("read_file on an image: %v", err)
	}
}

// --- partial reads (ADR-0014) ---

func TestReadFileLineWindows(t *testing.T) {
	r := newRegistry(t)
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "f.txt"),
		[]byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}

	// A window, annotated so it can never masquerade as the whole file.
	out, err := run(t, r, "read_file", map[string]any{"path": "f.txt", "start_line": float64(10), "end_line": float64(12)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "line 10\nline 11\nline 12") {
		t.Errorf("window content = %q", out)
	}
	if !strings.Contains(out, "[showing lines 10–12 of 40]") {
		t.Errorf("window note missing: %q", out)
	}
	// No line-number prefixes: numbered output would poison edit_file's
	// exact-match contract the moment the model copies what it read.
	if strings.Contains(out, "10:") || strings.Contains(out, "10\t") {
		t.Errorf("line numbers leaked into content: %q", out)
	}

	// Open-ended tail; clamped end.
	out, err = run(t, r, "read_file", map[string]any{"path": "f.txt", "start_line": float64(39)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "line 39\nline 40") || !strings.Contains(out, "39–40 of 40") {
		t.Errorf("tail window = %q", out)
	}
	out, err = run(t, r, "read_file", map[string]any{"path": "f.txt", "end_line": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "line 1\nline 2") || !strings.Contains(out, "1–2 of 40") {
		t.Errorf("head window = %q", out)
	}

	// Beyond EOF: an error naming the real length, not empty output.
	_, err = run(t, r, "read_file", map[string]any{"path": "f.txt", "start_line": float64(99)})
	if err == nil || !strings.Contains(err.Error(), "40 lines") {
		t.Fatalf("beyond-EOF error = %v", err)
	}
	// Inverted range.
	if _, err := run(t, r, "read_file", map[string]any{"path": "f.txt", "start_line": float64(5), "end_line": float64(3)}); err == nil {
		t.Fatal("inverted range accepted")
	}

	// No params: exactly the old behaviour, no note.
	out, err = run(t, r, "read_file", map[string]any{"path": "f.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[showing lines") || !strings.HasSuffix(out, "line 40") {
		t.Errorf("full read changed: %q", out)
	}
}

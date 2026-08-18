package tools

import (
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
		"list_files": false, "read_file": false,
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

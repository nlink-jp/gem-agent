package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRegistryWithWork returns a registry whose file tools may reach the
// project and a separate session work directory.
func newRegistryWithWork(t *testing.T) (*Registry, string) {
	t.Helper()
	r, err := New(t.TempDir(), directExec, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := r.UseWorkDir(work); err != nil {
		t.Fatal(err)
	}
	// Compare against the resolved path: on macOS t.TempDir() is under
	// a symlinked /var, and containment compares real paths.
	return r, r.WorkDir()
}

// The work directory is where everything the session produces outside
// the project lands — a spilled MCP result, a screenshot a server
// returned. If the model can see those paths and not open them, it
// routes around the file tools with shell redirection instead.
func TestFileToolsReachTheWorkDirectory(t *testing.T) {
	r, work := newRegistryWithWork(t)
	spilled := filepath.Join(work, "mcp-result.jsonl")
	if err := os.WriteFile(spilled, []byte(`{"host":"example.test"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, r, "read_file", map[string]any{"path": spilled})
	if err != nil {
		t.Fatalf("read_file on a work-dir path: %v", err)
	}
	if !strings.Contains(out, "example.test") {
		t.Errorf("read_file returned %q", out)
	}

	report := filepath.Join(work, "report.md")
	if _, err := run(t, r, "write_file", map[string]any{"path": report, "content": "# findings\n"}); err != nil {
		t.Fatalf("write_file into the work dir: %v", err)
	}
	if b, err := os.ReadFile(report); err != nil || string(b) != "# findings\n" {
		t.Errorf("write_file did not land: %v %q", err, b)
	}
}

// Adding a second root must not widen anything else: a path in neither
// root is still refused.
func TestPathsOutsideBothRootsAreStillRefused(t *testing.T) {
	r, _ := newRegistryWithWork(t)
	outside := filepath.Join(t.TempDir(), "elsewhere.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": outside}); err == nil {
		t.Error("a path in neither root was accepted")
	}
	if _, err := run(t, r, "write_file", map[string]any{"path": outside, "content": "x"}); err == nil {
		t.Error("a write in neither root was accepted")
	}
}

// With no work directory set the registry behaves exactly as before.
func TestWithoutAWorkDirectoryOnlyTheProjectIsReachable(t *testing.T) {
	r := newRegistry(t)
	if r.WorkDir() != "" {
		t.Fatalf("a fresh registry should have no work dir, got %q", r.WorkDir())
	}
	outside := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": outside}); err == nil {
		t.Error("a path outside the only root was accepted")
	}
}

// A relative path keeps meaning the project file it has always meant:
// the work directory is reached by the absolute path the prompt names.
func TestRelativePathsStillResolveAgainstTheProject(t *testing.T) {
	r, work := newRegistryWithWork(t)
	if err := os.WriteFile(filepath.Join(r.ProjectDir(), "same.txt"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "same.txt"), []byte("work"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, r, "read_file", map[string]any{"path": "same.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "project") {
		t.Errorf("a relative path resolved somewhere other than the project: %q", out)
	}
}

// Subset shares the parent's confinement, work directory included —
// otherwise a skill's restricted registry could not read a spilled
// result the same session produced.
func TestSubsetKeepsTheWorkDirectory(t *testing.T) {
	r, work := newRegistryWithWork(t)
	sub, err := r.Subset("read_file")
	if err != nil {
		t.Fatal(err)
	}
	if sub.WorkDir() != work {
		t.Fatalf("subset work dir = %q, want %q", sub.WorkDir(), work)
	}
	f := filepath.Join(work, "spill.txt")
	if err := os.WriteFile(f, []byte("carried"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := run(t, sub, "read_file", map[string]any{"path": f}); err != nil || !strings.Contains(out, "carried") {
		t.Errorf("subset cannot read the work dir: %v %q", err, out)
	}
}

// A symlink planted in the work directory must not become a way out,
// exactly as one planted in the project does not.
func TestSymlinkOutOfTheWorkDirectoryIsRefused(t *testing.T) {
	r, work := newRegistryWithWork(t)
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(work, "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": link}); err == nil {
		t.Error("a symlink out of the work directory was followed")
	}
}

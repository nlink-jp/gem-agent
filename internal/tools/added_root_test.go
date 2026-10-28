package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// newRegistryWithAdded returns a registry with one added write root and
// one added read root (ADR-0099), as real paths.
func newRegistryWithAdded(t *testing.T) (r *Registry, wroot, rroot string) {
	t.Helper()
	r, err := New(t.TempDir(), directExec, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wroot, _ = filepath.EvalSymlinks(t.TempDir())
	rroot, _ = filepath.EvalSymlinks(t.TempDir())
	if err := r.AddDirs([]sandbox.AddedRoot{{Dir: wroot, Write: true}, {Dir: rroot, Write: false}}); err != nil {
		t.Fatal(err)
	}
	return r, wroot, rroot
}

// A write root is read and written like the project; a read root is
// read like the project and written by no file tool, whatever the
// verdict said (ADR-0099 §1).
func TestAddedRootsReachAndLevel(t *testing.T) {
	r, wroot, rroot := newRegistryWithAdded(t)
	for _, root := range []string{wroot, rroot} {
		f := filepath.Join(root, "note.md")
		if err := os.WriteFile(f, []byte("hello from "+root), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := run(t, r, "read_file", map[string]any{"path": f})
		if err != nil || !strings.Contains(out, "hello from") {
			t.Errorf("read_file in %s: %v %q", root, err, out)
		}
	}
	w := filepath.Join(wroot, "sub", "new.md")
	if _, err := run(t, r, "write_file", map[string]any{"path": w, "content": "x\n"}); err != nil {
		t.Errorf("write_file into a write root: %v", err)
	}
	if b, _ := os.ReadFile(w); string(b) != "x\n" {
		t.Errorf("the write did not land: %q", b)
	}
	ro := filepath.Join(rroot, "new.md")
	_, err := run(t, r, "write_file", map[string]any{"path": ro, "content": "x\n"})
	if err == nil || !strings.Contains(err.Error(), "added read-only") {
		t.Errorf("write_file into a read root: %v", err)
	}
	if _, err := os.Stat(ro); err == nil {
		t.Error("a file landed in a read root")
	}
	_, err = run(t, r, "edit_file", map[string]any{"path": filepath.Join(rroot, "note.md"), "old_string": "hello", "new_string": "bye"})
	if err == nil {
		t.Error("edit_file in a read root succeeded")
	}
	if b, _ := os.ReadFile(filepath.Join(rroot, "note.md")); !strings.HasPrefix(string(b), "hello") {
		t.Errorf("a read root's file changed: %q", b)
	}
	got := r.AddedDirs()
	if len(got) != 2 || got[0].Dir != wroot || !got[0].Write || got[1].Dir != rroot || got[1].Write {
		t.Errorf("AddedDirs = %+v", got)
	}
}

// The innermost root decides the level: a write root added inside a
// read root is written; the read root around it is not.
func TestInnermostAddedRootDecidesTheLevel(t *testing.T) {
	r, err := New(t.TempDir(), directExec, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	outer, _ := filepath.EvalSymlinks(t.TempDir())
	inner := filepath.Join(outer, "drafts")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.AddDirs([]sandbox.AddedRoot{{Dir: outer, Write: false}, {Dir: inner, Write: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "write_file", map[string]any{"path": filepath.Join(inner, "a.md"), "content": "x"}); err != nil {
		t.Errorf("write into the inner write root: %v", err)
	}
	if _, err := run(t, r, "write_file", map[string]any{"path": filepath.Join(outer, "a.md"), "content": "x"}); err == nil {
		t.Error("write into the outer read root succeeded")
	}
}

// A link inside an added root that leads out of every root is refused
// like one in the project; a delegated child sees the added roots.
func TestAddedRootSymlinkOutAndSubset(t *testing.T) {
	r, wroot, _ := newRegistryWithAdded(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(wroot, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "read_file", map[string]any{"path": link}); err == nil {
		t.Error("a link out of an added root was followed")
	}
	sub, err := r.Subset("read_file")
	if err != nil {
		t.Fatal(err)
	}
	if len(sub.AddedDirs()) != 2 {
		t.Errorf("a subset sees %d added roots, want 2", len(sub.AddedDirs()))
	}
}

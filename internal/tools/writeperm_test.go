package tools

// Regression: overwriting through replaceFile installs a new inode, so
// the mode has to be carried across. write_file passed a literal 0644
// and reset every file it overwrote (review 2026-09-08, F-03).

import (
	"os"
	"path/filepath"
	"testing"
)

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// Both writing tools preserve the mode, because the preservation lives
// in replaceFile rather than at either call site.
func TestOverwritePreservesFileMode(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o600, 0o444} {
		for _, tool := range []string{"write_file", "edit_file"} {
			t.Run(tool+"-"+mode.String(), func(t *testing.T) {
				r := newRegistry(t)
				path := filepath.Join(r.ProjectDir(), "s.sh")
				if err := os.WriteFile(path, []byte("#!/bin/sh\necho old\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
				args := map[string]any{"path": "s.sh"}
				if tool == "write_file" {
					args["content"] = "#!/bin/sh\necho new\n"
					args["allow_shrink"] = true
				} else {
					args["old_string"] = "echo old"
					args["new_string"] = "echo new"
				}
				if _, err := run(t, r, tool, args); err != nil {
					t.Fatal(err)
				}
				if got := modeOf(t, path); got != mode {
					t.Errorf("%s turned %v into %v", tool, mode, got)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "#!/bin/sh\necho new\n" {
					t.Errorf("content = %q", data)
				}
			})
		}
	}
}

// A file that did not exist gets newFileMode through the umask — never
// wider than it, whatever the operator's umask is. Forcing the literal
// past umask would widen files on a restrictive one, which is the same
// defect pointed the other way.
func TestCreateDoesNotWidenPastNewFileMode(t *testing.T) {
	r := newRegistry(t)
	if _, err := run(t, r, "write_file", map[string]any{
		"path": "fresh.txt", "content": "hello\n",
	}); err != nil {
		t.Fatal(err)
	}
	got := modeOf(t, filepath.Join(r.ProjectDir(), "fresh.txt"))
	if got&^os.FileMode(newFileMode) != 0 {
		t.Errorf("new file is %v, wider than newFileMode %v", got, os.FileMode(newFileMode))
	}
}

// The mode of the name being replaced is what is inherited. A symlink
// is not a regular file, so the fresh file replacing that name takes
// the creation default — it does not borrow the target's mode.
func TestOverwriteThroughSymlinkDoesNotInheritTargetMode(t *testing.T) {
	r := newRegistry(t)
	target := filepath.Join(r.ProjectDir(), "target.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.ProjectDir(), "link.sh")
	if err := os.Symlink("target.sh", link); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, r, "write_file", map[string]any{
		"path": "link.sh", "content": "plain text\n", "allow_shrink": true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, link); got&0o111 != 0 {
		t.Errorf("replacement inherited the target's execute bit: %v", got)
	}
	// The link's target keeps its bytes and its mode (ADR-0073 R2).
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("write went through the link: %q", data)
	}
	if got := modeOf(t, target); got != 0o755 {
		t.Errorf("target mode = %v", got)
	}
}

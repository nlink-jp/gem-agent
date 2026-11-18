package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// The file the clipboard is captured into is made in the runtime's own
// state directory, in a directory nobody else may enter: osascript runs
// outside the sandbox and writes into the path it is given, and in the
// temporary directory — which every write lane writes — a link put
// under that name had the clipboard written through it.
func TestTheClipboardCaptureFileIsMadeWhereNoLaneWrites(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv(statedir.EnvRoot, state)
	// A temporary directory of its own, so that "not in it" says something.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	path, done, err := clipboardTemp()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if dir != filepath.Join(state, "clipboard") {
		t.Errorf("made in %s, want the state directory's clipboard directory", dir)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the directory is %v (%v), want it closed to everybody else", st, err)
	}
	if st, err := os.Lstat(path); err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o600 {
		t.Errorf("the file is %v (%v)", st, err)
	}
	for _, scratch := range append(sandbox.ScratchDirs(), tmp) {
		if strings.HasPrefix(path, scratch+string(filepath.Separator)) {
			t.Errorf("%s is under %s, which the lanes write", path, scratch)
		}
	}
	done()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("the file was left behind: %v", err)
	}
}

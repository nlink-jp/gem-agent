package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// A link planted under the old fixed name is not the probe's directory.
func TestFileProbeDirIgnoresAPlantedLink(t *testing.T) {
	work, victim := t.TempDir(), t.TempDir()
	if err := os.Symlink(victim, filepath.Join(work, "probe")); err != nil {
		t.Fatal(err)
	}
	dir, done := fileProbeDir(work)
	defer done()
	if filepath.Dir(dir) != work {
		t.Fatalf("probe directory %q is not in the work directory", dir)
	}
	fi, err := os.Lstat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("probe directory is not a real directory: %v, %v", fi, err)
	}
	if dir == filepath.Join(work, "probe") {
		t.Fatal("the planted name was used")
	}
}

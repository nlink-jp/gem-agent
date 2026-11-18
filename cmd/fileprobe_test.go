package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
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

// A link left at <work>/scratch must not become the read lane's writable
// directory: the lane is turned off, and its target stays out of reach.
func TestPlantedScratchLinkDisablesTheReadLane(t *testing.T) {
	if err := sandbox.Available(); err != nil {
		t.Skip(err)
	}
	project, _ := sandbox.ResolveWriteDir(t.TempDir())
	work, _ := sandbox.ResolveWriteDir(t.TempDir())
	victim, _ := sandbox.ResolveWriteDir(t.TempDir())
	if err := os.Symlink(victim, filepath.Join(work, "scratch")); err != nil {
		t.Fatal(err)
	}
	if dir, err := readScratchDir(work); err == nil {
		t.Fatalf("the planted link was accepted: %q", dir)
	}
	execFn, enf, notes, err := buildExecFn(true, project, work, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if enf.ReadLane {
		t.Errorf("the read lane stayed on: %v", notes)
	}
	if !strings.Contains(strings.Join(notes, " "), "read lane disabled") {
		t.Errorf("the operator is not told: %v", notes)
	}
	planted := filepath.Join(victim, "planted")
	_ = execFn(context.Background(), "echo x > "+shellQuoted(planted), sandbox.LaneRead).Run()
	if _, err := os.Stat(planted); err == nil {
		t.Error("the read lane wrote through the planted link")
	}
}

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/workdir"
)

// lanesFixture is a project and a directory beside it that no lane may
// write, under home, with the state tree beside them.
func lanesFixture(t *testing.T) (proj, target string) {
	t.Helper()
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	base, err := os.MkdirTemp(home, ".gem-agent-kept-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = sandbox.ResolveWriteDir(base)
	proj, target = filepath.Join(base, "proj"), filepath.Join(base, "target")
	for _, d := range []string{proj, target} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, "main.go"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(statedir.EnvRoot, filepath.Join(base, "state"))
	return proj, target
}

func laneRun(t *testing.T, fn tools.LaneExecFunc, lane sandbox.Lane, command string) error {
	t.Helper()
	out, err := fn(context.Background(), command, lane).CombinedOutput()
	if err != nil {
		t.Logf("%s lane, %q: %v %s", lane, command, err, strings.TrimSpace(string(out)))
	}
	return err
}

func untouched(t *testing.T, target string) {
	t.Helper()
	entries, _ := os.ReadDir(target)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the directory beside the project now holds %v", names)
	}
	if got, _ := os.ReadFile(filepath.Join(target, "keep.txt")); string(got) != "keep\n" {
		t.Errorf("a file beside the project now holds %q", got)
	}
}

// A command in the write lane writes its work directory, and what it
// writes it can replace: remove it and put a link under its name. The
// lanes are built more than once in a session, and each build resolved
// the names again: after the replacement the next build anchored the
// write lane on wherever the link led (measured on v0.106.0: a file
// beside the project was written). The lanes are anchored on the first
// answer a name gave (ADR-0122 §2): built again after the replacement,
// they do not write where the link leads, and the runtime makes nothing
// there.
func TestALaneThatReplacesItsWorkDirectoryGainsNothingAtTheRebuild(t *testing.T) {
	proj, target := lanesFixture(t)
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, enf, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf.Confined {
		t.Fatalf("the first build: %+v, %v", enf, err)
	}
	if laneRun(t, fn, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "direct")) == nil {
		t.Fatal("the write lane writes beside the project: the fixture shows nothing")
	}
	if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && mv "+work+" "+filepath.Join(proj, "old-work")+" && ln -s "+target+" "+work); err != nil {
		t.Fatalf("the write lane could not replace its work directory: the replacement was not exercised (%v)", err)
	}
	if st, err := os.Lstat(work); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the work directory was not replaced by a link: %v, %v", st, err)
	}
	fn2, enf2, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf2.Confined {
		t.Fatalf("the rebuild after the replacement: %+v, %v", enf2, err)
	}
	for _, f := range []tools.LaneExecFunc{fn, fn2} {
		for _, lane := range []sandbox.Lane{sandbox.LaneWrite, sandbox.LaneRead} {
			if laneRun(t, f, lane, "echo x > "+filepath.Join(target, "after")) == nil {
				t.Errorf("the %s lane writes where the link leads", lane)
			}
			if laneRun(t, f, lane, "echo x > "+filepath.Join(work, "through")) == nil {
				t.Errorf("the %s lane writes through the link", lane)
			}
		}
	}
	untouched(t, target)
}

// The temporary directory is one the lanes write and the profiles are
// anchored on. Where its parent lets it be moved — a TMPDIR the operator
// set — a command in the write lane can move it aside and leave a link.
// It keeps its first answer: the lanes built again are anchored where
// the directory was, which gives the link nothing.
func TestATemporaryDirectoryReplacedByALinkIsNotWhereTheLanesWrite(t *testing.T) {
	proj, target := lanesFixture(t)
	tmp := filepath.Join(filepath.Dir(proj), "tmp")
	if err := os.Mkdir(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, enf, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf.Confined {
		t.Fatalf("the first build: %+v, %v", enf, err)
	}
	if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && mv "+tmp+" "+filepath.Join(proj, "old-tmp")+" && ln -s "+target+" "+tmp); err != nil {
		t.Fatalf("the write lane could not replace the temporary directory: the replacement was not exercised (%v)", err)
	}
	fn2, enf2, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf2.Confined {
		t.Fatalf("the rebuild: %+v, %v", enf2, err)
	}
	for _, f := range []tools.LaneExecFunc{fn, fn2} {
		if laneRun(t, f, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Error("the write lane writes where the link leads")
		}
	}
	untouched(t, target)
}

// A work directory a command removed, with nothing put under its name,
// is built on again: the lanes stay anchored where it was, and a
// command can make it again there.
func TestAWorkDirectoryThatIsGoneIsBuiltOnAgain(t *testing.T) {
	proj, target := lanesFixture(t)
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, _, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && rm -rf "+work); err != nil {
		t.Fatalf("the write lane could not remove its work directory (%v)", err)
	}
	fn2, enf2, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf2.Confined {
		t.Fatalf("the rebuild after the removal: %+v, %v", enf2, err)
	}
	if err := laneRun(t, fn2, sandbox.LaneWrite, "mkdir -p "+work+" && echo x > "+filepath.Join(work, "f")); err != nil {
		t.Errorf("the write lane cannot write its work directory after the rebuild: %v", err)
	}
	untouched(t, target)
}

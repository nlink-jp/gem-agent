package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/place"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/workdir"
)

// lanesFixture is a project and a directory beside it that no lane may
// write, under home, with the state tree beside them.
func lanesFixture(t *testing.T, projectFiles ...string) (proj, target string) {
	t.Helper()
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	base, err := os.MkdirTemp(home, ".gem-agent-place-test-")
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
	for _, f := range append(projectFiles, "main.go") {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(proj, f)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(proj, f), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
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

// A lane writes its work directory, and what it writes it can replace:
// remove it and put a link under its name. The lanes are built more
// than once in a session, and they were built from names: after such a
// replacement the next build anchored the write lane on wherever the
// link led, and the startup checks passed (measured on both runtimes
// before ADR-0122: a file beside the project was written, and the read
// lane wrote a scratch directory there). The lanes are built from the
// places the session took. A rebuild after the replacement fails and
// says the place was replaced, the lanes that were built before it do
// not write where the link leads, and nothing is made there.
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
	if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && rm -rf "+work+" && ln -s "+target+" "+work); err != nil {
		t.Fatalf("the write lane could not replace its work directory: the replacement was not exercised (%v)", err)
	}
	if st, err := os.Lstat(work); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the work directory was not replaced by a link: %v, %v", st, err)
	}
	fn2, _, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if !errors.Is(err, place.ErrReplaced) || fn2 != nil {
		t.Errorf("the rebuild after the replacement: %v, want it refused as a replaced place", err)
	}
	for _, lane := range []sandbox.Lane{sandbox.LaneWrite, sandbox.LaneRead} {
		if laneRun(t, fn, lane, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Errorf("the %s lane built before the replacement writes where the link leads", lane)
		}
		if laneRun(t, fn, lane, "mkdir -p "+filepath.Join(work, "scratch")+" && echo x > "+filepath.Join(work, "scratch", "f")) == nil {
			t.Errorf("the %s lane writes through the link", lane)
		}
	}
	untouched(t, target)
}

// The same for a project the lane can empty — one with no file it is
// refused the removal of — and for the lanes as /clear builds them: a
// new work directory, the same project name.
func TestALaneThatReplacesTheProjectGainsNothingAtTheRebuild(t *testing.T) {
	proj, target := lanesFixture(t)
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, _, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && rm -rf "+proj+" && ln -s "+target+" "+proj); err != nil {
		t.Fatalf("the write lane could not replace the project: the replacement was not exercised (%v)", err)
	}
	work2, err := workdir.Ensure(proj, "sid2")
	if err != nil {
		t.Fatal(err)
	}
	fn2, _, _, _, err := buildLanes(true, proj, work2, nil, nil, nil, nil)
	if !errors.Is(err, place.ErrReplaced) || fn2 != nil {
		t.Errorf("the rebuild after the replacement: %v, want it refused as a replaced place", err)
	}
	if laneRun(t, fn, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "after")) == nil {
		t.Error("the write lane built before the replacement writes where the link leads")
	}
	untouched(t, target)
}

// A project holding a file the write lane is refused the removal of
// cannot be emptied, and so cannot be replaced: the git configuration
// is one.
func TestAProjectWithAProtectedFileCannotBeReplaced(t *testing.T) {
	proj, target := lanesFixture(t, ".git/config")
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, _, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if laneRun(t, fn, sandbox.LaneWrite, "cd / && rm -rf "+proj+" && ln -s "+target+" "+proj) == nil {
		t.Error("the write lane replaced a project that holds a git configuration")
	}
	if _, _, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil); err != nil {
		t.Errorf("a rebuild in a project nobody replaced: %v", err)
	}
	untouched(t, target)
}

// The scratch directory and the cache are places of their own, in a
// work directory the write lane writes: one replaced by a link is not
// handed to a lane to write. The read lane goes without its scratch,
// and so asks; the cache stays unredirected.
func TestAScratchOrCacheReplacedByALinkIsNotGivenToALane(t *testing.T) {
	proj, target := lanesFixture(t)
	work, err := workdir.Ensure(proj, "sid1")
	if err != nil {
		t.Fatal(err)
	}
	fn, enf, _, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil || !enf.ReadLane {
		t.Fatalf("the first build: %+v, %v", enf, err)
	}
	scratch := filepath.Join(work, "scratch")
	if err := laneRun(t, fn, sandbox.LaneWrite, "rm -rf "+scratch+" && ln -s "+target+" "+scratch); err != nil {
		t.Fatalf("the write lane could not replace the scratch directory (%v)", err)
	}
	fn2, enf2, notes, _, err := buildLanes(true, proj, work, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("the rebuild: %v", err)
	}
	if enf2.ReadLane || !strings.Contains(strings.Join(notes, "\n"), "is no longer the directory this session took") {
		t.Errorf("the rebuild with a replaced scratch: %+v, notes %v — want the read lane off, and told why", enf2, notes)
	}
	for _, lane := range []sandbox.Lane{sandbox.LaneRead, sandbox.LaneWrite} {
		if laneRun(t, fn2, lane, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Errorf("the %s lane writes where the scratch link leads", lane)
		}
	}
	untouched(t, target)
}

// taken takes dir as a place for the length of the test and returns
// it: a test that makes a work directory itself, and not through
// workdir.Ensure, takes it as the session does when it makes one.
func taken(t *testing.T, dir string) string {
	t.Helper()
	p, err := place.Take(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Release)
	return dir
}

// The two things that build the lanes again in a session are /clear and
// the granting of a directory. Either one that finds the project or the
// work directory replaced builds nothing: it says which place was
// replaced, and from there every shell command asks — the lanes built
// before stay in the way, and nothing runs unasked.
func TestARebuildThatFindsAPlaceReplacedPutsEveryCommandBeforeTheOperator(t *testing.T) {
	t.Run("/clear, the project replaced", func(t *testing.T) {
		proj, target := lanesFixture(t)
		work, err := workdir.Ensure(proj, "sid1")
		if err != nil {
			t.Fatal(err)
		}
		fn, enf, _, err := buildExecFn(true, proj, work, nil, nil, nil, nil)
		if err != nil || !enf.Confined {
			t.Fatalf("the first build: %+v, %v", enf, err)
		}
		registry, err := tools.New(proj, nil, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		shellExec := &liveExec{fn: fn}
		registry.SetLaneExec(shellExec.run, enf)
		if err := laneRun(t, fn, sandbox.LaneWrite, "cd / && rm -rf "+proj+" && ln -s "+target+" "+proj); err != nil {
			t.Fatalf("the write lane could not replace the project (%v)", err)
		}
		work2, err := workdir.Ensure(proj, "sid2")
		if err != nil {
			t.Fatal(err)
		}
		notes := strings.Join(rotateWorkDir(registry, shellExec, true, proj, work2, nil, nil, false, nil, nil, nil, nil), "\n")
		if !strings.Contains(notes, "is no longer the directory this session took") || !strings.Contains(notes, "every shell command asks you") {
			t.Errorf("the notes after /clear: %q", notes)
		}
		if got := registry.Enforcement(); got != (sandbox.Enforcement{}) {
			t.Errorf("after /clear the session still runs commands unasked: %+v", got)
		}
		if laneRun(t, shellExec.run, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Error("after /clear the write lane writes where the link leads")
		}
		untouched(t, target)
	})
	t.Run("a grant, the work directory replaced", func(t *testing.T) {
		g, _, _ := newTestGranter(t, nil, false)
		work, err := workdir.Ensure(g.projectDir, "sid1")
		if err != nil {
			t.Fatal(err)
		}
		g.workDir = func() string { return work }
		if notes := rotateWorkDir(g.registry, g.shellExec, true, g.projectDir, work, nil, nil, false, nil, nil, nil, nil); len(notes) != 0 {
			t.Fatalf("giving the session its work directory: %v", notes)
		}
		target := underHome(t, "target")
		if err := runLane(t, g, sandbox.LaneWrite, "cd / && rm -rf "+work+" && ln -s "+target+" "+work); err != nil {
			t.Fatalf("the write lane could not replace its work directory (%v)", err)
		}
		_, err = g.grant(context.Background(), underHome(t, "notes"), true, "slash", g.currentEpoch(), nil)
		if err == nil || !strings.Contains(err.Error(), "is no longer the directory this session took") || !strings.Contains(err.Error(), "every shell command asks you") {
			t.Errorf("the grant: %v, want it refused on the replaced work directory", err)
		}
		if got := g.registry.Enforcement(); got != (sandbox.Enforcement{}) {
			t.Errorf("after the grant the session still runs commands unasked: %+v", got)
		}
		if runLane(t, g, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Error("after the grant the write lane writes where the link leads")
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("the directory the link leads to now holds %d entries", len(entries))
		}
	})
}

// The file tools open the work directory the session took. One that
// has been replaced by a link is not resolved again and opened where
// the link leads: it is refused, and the tools keep what they had.
func TestTheFileToolsDoNotOpenAReplacedWorkDirectory(t *testing.T) {
	base := t.TempDir()
	proj, work, other := filepath.Join(base, "proj"), filepath.Join(base, "work"), filepath.Join(base, "other")
	for _, d := range []string{proj, work, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	taken(t, proj)
	taken(t, work)
	registry, err := tools.New(proj, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.UseWorkDir(work); err != nil {
		t.Fatalf("the work directory as it was taken: %v", err)
	}
	real, _ := filepath.EvalSymlinks(work)
	if got := registry.WorkDir(); got != real {
		t.Errorf("the tools' work directory is %s, want %s", got, real)
	}
	if err := os.Rename(work, work+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, work); err != nil {
		t.Fatal(err)
	}
	if err := registry.UseWorkDir(work); !errors.Is(err, place.ErrReplaced) {
		t.Errorf("a replaced work directory: %v, want it refused", err)
	}
	if got := registry.WorkDir(); got != real {
		t.Errorf("after the refusal the tools' work directory is %s, want it kept at %s", got, real)
	}
	// And the project: a registry is not made on what replaced it.
	if err := os.Rename(proj, proj+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, proj); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.New(proj, nil, 5*time.Second); !errors.Is(err, place.ErrReplaced) {
		t.Errorf("a replaced project: %v, want it refused", err)
	}
}

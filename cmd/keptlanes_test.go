package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A lane writes its work directory, and what it writes it can replace:
// remove it and put a link under its name. The lanes are built more
// than once in a session, and each build resolved the names again:
// after the replacement the next build anchored the write lane on
// wherever the link led (measured on v0.106.0: a file beside the
// project was written). The first answer a name gave is kept
// (ADR-0122 §2). A rebuild after the replacement builds nothing and
// says the directory was replaced, and the lanes built before it do not
// write where the link leads.
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
	if !isReplaced(err) || fn2 != nil {
		t.Errorf("the rebuild after the replacement: %v, want it refused as a replaced directory", err)
	}
	for _, lane := range []sandbox.Lane{sandbox.LaneWrite, sandbox.LaneRead} {
		if laneRun(t, fn, lane, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Errorf("the %s lane built before the replacement writes where the link leads", lane)
		}
		if laneRun(t, fn, lane, "echo x > "+filepath.Join(work, "through")) == nil {
			t.Errorf("the %s lane writes through the link", lane)
		}
	}
	untouched(t, target)
}

// The two things that build the lanes again in a session are /clear and
// the granting of a directory. Either one that finds the project or the
// work directory replaced builds nothing: it says which directory was
// replaced, and from there every shell command asks — the lanes built
// before stay in the way, and nothing runs unasked.
func TestARebuildThatFindsADirectoryReplacedPutsEveryCommandBeforeTheOperator(t *testing.T) {
	t.Run("/clear, the project replaced", func(t *testing.T) {
		// A project the lane can empty: one with no file it is refused
		// the removal of.
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
		if !strings.Contains(notes, "was replaced: it now leads to "+target) || !strings.Contains(notes, "every shell command asks you") {
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
		if err == nil || !strings.Contains(err.Error(), "was replaced: it now leads to "+target) || !strings.Contains(err.Error(), "every shell command asks you") {
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

// The temporary directory is one the lanes write and the profiles are
// anchored on. Where its parent lets it be moved — a TMPDIR the operator
// set — the write lane can move it aside and leave a link. It keeps its
// first answer and stops nothing: the lanes are built again, and they
// are anchored where the directory was, which gives the link nothing.
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
		t.Fatalf("the rebuild: %+v, %v — a replaced temporary directory stops nothing", enf2, err)
	}
	for _, f := range []tools.LaneExecFunc{fn, fn2} {
		if laneRun(t, f, sandbox.LaneWrite, "echo x > "+filepath.Join(target, "after")) == nil {
			t.Error("the write lane writes where the link leads")
		}
	}
	untouched(t, target)
}

// A work directory a command removed, with nothing put under its name,
// is not a replaced one: the lanes are built again, anchored where it
// was.
func TestAWorkDirectoryThatIsGoneIsNotAReplacedOne(t *testing.T) {
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

// The runtime writes under the work directory's name itself, outside
// the sandbox, when it saves an MCP result. Through a work directory a
// lane had replaced by a link the file was made where the link led
// (measured). The save compares the name with its kept answer first.
func TestAResultIsNotSavedThroughAWorkDirectoryThatBecameALink(t *testing.T) {
	base, err := sandbox.ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	work, outside := filepath.Join(base, "work"), filepath.Join(base, "outside")
	for _, d := range []string{work, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// As the build of the lanes does when the session gets the directory.
	if _, err := sandbox.ResolveKept(work); err != nil {
		t.Fatal(err)
	}
	in := newMCPIntake(fixedDir(work))
	if path, err := in.write("srv", "tool", ".txt", []byte("first")); err != nil || filepath.Dir(path) != work {
		t.Fatalf("a save into the work directory as it was: %q, %v", path, err)
	}
	if err := os.Rename(work, work+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, work); err != nil {
		t.Fatal(err)
	}
	if _, err := in.write("srv", "tool", ".txt", []byte("second")); !isReplaced(err) {
		t.Errorf("a save into a work directory that became a link: %v, want it refused", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("a file was made where the link leads: %d entries", len(entries))
	}
}

// An added directory that was replaced fails its own check, and its
// error is not the one that puts every command before the operator:
// that is for the project and the work directory. And the note names
// the directory and where it leads now.
func TestAReplacedAddedDirectoryIsNotAReplacedProject(t *testing.T) {
	replaced := &sandbox.ReplacedError{Dir: "/a", Was: "/a", Now: "/b"}
	if !isReplaced(replaced) {
		t.Error("a replaced project or work directory is not reported as one")
	}
	if isReplaced(&sandbox.AddedRootError{Root: sandbox.AddedRoot{Dir: "/a", Write: true}, Err: replaced}) {
		t.Error("an added directory that was replaced is taken for the project or the work directory")
	}
	if got := replaced.Error(); got != "/a was replaced: it now leads to /b" {
		t.Errorf("the note names the directory as %q", got)
	}
	if got := (&sandbox.ReplacedError{Dir: "/a", Was: "/a"}).Error(); got != "/a was replaced by something that leads nowhere" {
		t.Errorf("a link to nowhere is named as %q", got)
	}
}

// A grant the model asked for returns its error to the model. When the
// rebuild finds the work directory replaced, every command asks from
// there on, and the operator is told why as well: the note goes to the
// notices, not only into the tool's result.
func TestTheOperatorIsToldOfAReplacedDirectoryWhenTheModelAskedForTheGrant(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, false)
	var told []string
	g.notice = func(s string) { told = append(told, s) }
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
	_, err = g.grant(context.Background(), underHome(t, "notes"), true, "request", g.currentEpoch(), map[string]any{"reason": "r", "answer": "approved"})
	if err == nil || !strings.Contains(err.Error(), "was replaced: it now leads to "+target) {
		t.Errorf("the grant: %v, want it refused on the replaced work directory", err)
	}
	if len(told) != 1 || !strings.Contains(told[0], "was replaced: it now leads to "+target) || !strings.Contains(told[0], "every shell command asks you") {
		t.Errorf("the operator was told %q, want the note about the replaced directory", told)
	}
	if got := g.registry.Enforcement(); got != (sandbox.Enforcement{}) {
		t.Errorf("after the grant the session still runs commands unasked: %+v", got)
	}
}

package sandbox

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// probeFixture is a held directory with one probe made in it, and a
// directory beside it holding a file nothing here may touch.
func probeFixture(t *testing.T) (p *probes, name, dir, victim string) {
	t.Helper()
	base := t.TempDir()
	dir, outside := filepath.Join(base, "held"), filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	victim = filepath.Join(outside, "victim")
	if err := os.WriteFile(victim, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := openProbes(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.cleanup() })
	if name, err = p.file(".probe-"); err != nil {
		t.Fatal(err)
	}
	return p, name, dir, victim
}

// soon runs fn and fails the test if it has not come back in two
// seconds; pipe, if there is one, is then opened so that what waits on
// it ends.
func soon(t *testing.T, pipe string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		if pipe != "" {
			if rw, err := os.OpenFile(pipe, os.O_RDWR|syscall.O_NONBLOCK, 0); err == nil {
				defer func() { _ = rw.Close() }()
			}
		}
		<-done
		t.Fatal("it waited")
	}
}

func kept(t *testing.T, path string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != "keep\n" {
		t.Errorf("%s holds %q (%v): it was written through to", path, got, err)
	}
}

// The control writes into the probe that was made, and into nothing
// that was put in its place: whatever sits under the name by then, it
// is refused at once and what it leads to is as it was.
func TestTheControlWritesOnlyTheProbeThatWasMade(t *testing.T) {
	p, name, dir, _ := probeFixture(t)
	if err := p.write(name); err != nil {
		t.Fatalf("the probe as it was made: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != "x\n" {
		t.Errorf("the probe holds %q", got)
	}
	for what, replace := range map[string]func(t *testing.T, path, dir, victim string){
		"a link out of the directory": func(t *testing.T, path, _, victim string) {
			must(t, os.Symlink(victim, path))
		},
		"a link to another name in the directory": func(t *testing.T, path, dir, _ string) {
			must(t, os.WriteFile(filepath.Join(dir, "inside"), []byte("keep\n"), 0o600))
			must(t, os.Symlink("inside", path))
		},
		"a hard link to another file": func(t *testing.T, path, _, victim string) {
			must(t, os.Link(victim, path))
		},
		"another file": func(t *testing.T, path, _, _ string) {
			must(t, os.WriteFile(path, []byte("keep\n"), 0o600))
		},
		"a FIFO nobody reads": func(t *testing.T, path, _, _ string) {
			must(t, syscall.Mkfifo(path, 0o600))
		},
		"a FIFO somebody reads": func(t *testing.T, path, _, _ string) {
			must(t, syscall.Mkfifo(path, 0o600))
			r, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
			must(t, err)
			t.Cleanup(func() { _ = r.Close() })
		},
		"a directory": func(t *testing.T, path, _, _ string) {
			must(t, os.Mkdir(path, 0o700))
		},
		"nothing": func(t *testing.T, path, _, _ string) {},
	} {
		t.Run(what, func(t *testing.T) {
			p, name, dir, victim := probeFixture(t)
			path := filepath.Join(dir, name)
			must(t, os.Remove(path))
			replace(t, path, dir, victim)
			for _, control := range []func(string) error{p.write, p.read} {
				var err error
				soon(t, path, func() { err = control(name) })
				if !errors.Is(err, errProbeReplaced) {
					t.Errorf("%v, want the probe reported as replaced", err)
				}
			}
			if err := p.ours(name); !errors.Is(err, errProbeReplaced) {
				t.Errorf("the name was taken for the probe: %v", err)
			}
			kept(t, victim)
			if strings.Contains(what, "in the directory") {
				kept(t, filepath.Join(dir, "inside"))
			}
			if what == "another file" {
				kept(t, path)
			}
			// And the clean-up leaves it: what is under the name is not
			// what the check made.
			if left := p.cleanup(); len(left) != 0 {
				t.Errorf("left = %v: what replaced the probe was named as the check's leftover", left)
			}
			if _, err := os.Lstat(path); (err == nil) != (what != "nothing") {
				t.Errorf("what replaced the probe was removed by the clean-up: %v", err)
			}
			kept(t, victim)
		})
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// A probe the lane's command would make is made by the control, and
// only under a name that is free: a link waiting there — to a file, or
// to a name that does not exist yet — is not written through.
func TestTheControlMakesAProbeOnlyUnderAFreeName(t *testing.T) {
	p, _, dir, victim := probeFixture(t)
	if err := p.write("AGENTS.md"); err != nil {
		t.Fatalf("a free name: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md")); string(got) != "x\n" {
		t.Errorf("the probe holds %q", got)
	}
	p.drop("AGENTS.md")
	if _, err := os.Lstat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("the dropped probe is still there: %v", err)
	}
	fresh := filepath.Join(filepath.Dir(victim), "fresh")
	for name, target := range map[string]string{"to-victim": victim, "to-nothing": fresh} {
		must(t, os.Symlink(target, filepath.Join(dir, name)))
		if err := p.write(name); !errors.Is(err, errProbeReplaced) {
			t.Errorf("%s: %v, want the name reported as taken", name, err)
		}
	}
	kept(t, victim)
	if _, err := os.Lstat(fresh); !os.IsNotExist(err) {
		t.Errorf("a file was made through a link: %v", err)
	}
}

// The clean-up removes names from the directories it holds. A probe
// directory replaced by a link is not gone through: the files beside
// the project that carry the probes' names stay, the files the check
// made go from the directory it holds, and the link — which the check
// did not make — is neither removed nor named as the check's to delete.
func TestCleanupRemovesNothingThroughAReplacedDirectory(t *testing.T) {
	base := t.TempDir()
	held, outside := filepath.Join(base, "held"), filepath.Join(base, "outside")
	for _, d := range []string{held, outside} {
		must(t, os.Mkdir(d, 0o700))
	}
	names := []string{".env", "plain", "created", "AGENTS.md"}
	for _, n := range names {
		must(t, os.WriteFile(filepath.Join(outside, n), []byte("keep\n"), 0o600))
	}
	root, err := openProbes(held)
	must(t, err)
	dir, err := root.subdir(".probe-")
	must(t, err)
	must(t, dir.fileNamed(".env", "probe\n"))
	must(t, dir.fileNamed("plain", "probe\n"))
	dir.expect("created")
	dir.expect("AGENTS.md")
	if err := dir.inPlace(); err != nil {
		t.Fatalf("a probe directory nobody touched: %v", err)
	}
	// Moved aside, and a link to the outside directory put under its name.
	moved := filepath.Join(held, "moved")
	must(t, os.Rename(dir.path, moved))
	must(t, os.Symlink(outside, dir.path))
	if err := dir.inPlace(); !errors.Is(err, errProbeReplaced) {
		t.Errorf("a replaced probe directory: %v, want it reported", err)
	}
	// The control still writes into the directory that was made.
	must(t, dir.write("AGENTS.md"))
	if _, err := os.Stat(filepath.Join(moved, "AGENTS.md")); err != nil {
		t.Errorf("the control did not write into the directory it holds: %v", err)
	}
	link := dir.path
	left := root.cleanup()
	for _, n := range names {
		kept(t, filepath.Join(outside, n))
	}
	if entries, _ := os.ReadDir(moved); len(entries) != 0 {
		t.Errorf("the check's own files were left in the directory it held: %v", entries)
	}
	if len(left) != 0 {
		t.Errorf("left = %v: something the check did not make was named as its leftover", left)
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Errorf("what was put under the directory's name was removed: %v, %v", st, err)
	}
}

// A probe directory is used only as empty as it was made. Another
// directory moved over its name — the write lane can do that with one
// rename — holds files that are not the check's: the check fails, and
// nothing in that directory is written or removed.
func TestAProbeDirectoryThatIsNotEmptyIsNotUsed(t *testing.T) {
	base := t.TempDir()
	held, theirs := filepath.Join(base, "held"), filepath.Join(base, "theirs")
	for _, d := range []string{held, theirs} {
		must(t, os.Mkdir(d, 0o700))
	}
	names := []string{".env", "plain", "created", "AGENTS.md"}
	for _, n := range names {
		must(t, os.WriteFile(filepath.Join(theirs, n), []byte("keep\n"), 0o600))
	}
	root, err := openProbes(held)
	must(t, err)
	// The moment between the making and the opening, taken: the name
	// the check is about to make is given a directory that is not empty.
	old := mkdirProbe
	mkdirProbe = func(p *probes, name string) error {
		if err := old(p, name); err != nil {
			return err
		}
		// The system call itself: os.Rename refuses a directory as
		// the new name, and rename(2) takes an empty one.
		return syscall.Rename(theirs, p.at(name))
	}
	dir, err := root.subdir(".probe-")
	mkdirProbe = old
	if !errors.Is(err, errProbeReplaced) {
		t.Fatalf("a probe directory that holds files: %v, want it refused", err)
	}
	left := root.cleanup()
	for _, n := range names {
		kept(t, filepath.Join(dir.path, n))
	}
	if len(left) != 0 {
		t.Errorf("left = %v: a directory the check did not make was named as its leftover", left)
	}
}

// A lane's command that did not run to its own end was not refused: a
// process in the write lane can end any of the operator's processes,
// and one that ended a probe made a lane that denies nothing look as
// if it had denied.
func TestAProbeEndedFromOutsideIsNotADenial(t *testing.T) {
	err := runProbe([]string{"/bin/bash", "-c", "kill -TERM $$"})
	if !errors.Is(err, errProbeEnded) {
		t.Fatalf("a probe ended by a signal: %v, want it told apart", err)
	}
	if denial(err, LaneRead, "allowed a write") == nil {
		t.Error("a probe that was ended from outside was counted as denied")
	}
	if err := runProbe([]string{"/bin/bash", "-c", "exit 1"}); err == nil || errors.Is(err, errProbeEnded) || denial(err, LaneRead, "x") != nil {
		t.Errorf("a probe that failed by itself: %v", err)
	}
}

// The bound ends the command and what it started: a shell ended alone
// leaves the program it was waiting for, here a cat on a FIFO, and one
// such was left at every start.
func TestTheBoundEndsWhatTheProbeStarted(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = old }()
	dir := t.TempDir()
	pipe, pidfile := filepath.Join(dir, "pipe"), filepath.Join(dir, "pid")
	must(t, syscall.Mkfifo(pipe, 0o600))
	script := "/bin/cat " + shellQuote(pipe) + " > /dev/null & echo $! > " + shellQuote(pidfile) + "; wait"
	var err error
	soon(t, pipe, func() { err = runProbe([]string{"/bin/bash", "-c", script}) })
	if !errors.Is(err, errProbeTimeout) {
		t.Fatalf("a probe whose child waits on a FIFO: %v, want it ended", err)
	}
	raw, rerr := os.ReadFile(pidfile)
	pid, perr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if rerr != nil || perr != nil || pid <= 1 {
		t.Fatalf("the probe did not say what it started: %q, %v, %v", raw, rerr, perr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the program the probe started is still running after the bound")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A probe command has probeTimeout. One that has not finished is ended
// and is a failed check — for a probe that must fail too: not having
// finished is not having been denied.
func TestAProbeThatDoesNotFinishIsEndedAndFailsItsCheck(t *testing.T) {
	old := probeTimeout
	probeTimeout = 300 * time.Millisecond
	defer func() { probeTimeout = old }()
	pipe := filepath.Join(t.TempDir(), "pipe")
	must(t, syscall.Mkfifo(pipe, 0o600))
	var err error
	start := time.Now()
	soon(t, pipe, func() { err = runProbe([]string{"/bin/bash", "-c", "echo x > " + shellQuote(pipe)}) })
	if !errors.Is(err, errProbeTimeout) {
		t.Fatalf("a probe waiting on a FIFO: %v after %v, want it ended", err, time.Since(start))
	}
	if denial(err, LaneRead, "allowed a write") == nil {
		t.Error("a probe that did not finish was counted as denied")
	}
	if err := denial(nil, LaneRead, "allowed a write"); err == nil || !strings.Contains(err.Error(), "the read lane allowed a write") {
		t.Errorf("a probe that was allowed: %v", err)
	}
	if err := denial(&exec.ExitError{}, LaneRead, "allowed a write"); err != nil {
		t.Errorf("a probe that was denied: %v", err)
	}
	if err := runProbe([]string{"/usr/bin/true"}); err != nil {
		t.Errorf("an ordinary probe: %v", err)
	}
}

// The control no longer goes through the shell, so that the quoted
// path names the file is shown here: whatever a path holds, bash reads
// the quoted form back as that path.
func TestShellQuoteNamesThePathItWasGiven(t *testing.T) {
	for _, path := range []string{
		"/plain/path", "/with space/and\ttab", "/it's/quoted", `/double"quote`, "/dollar$HOME/`tick`",
		"/new\nline", "/semi;colon&amp|pipe", "/glob*?[x]", "/back\\slash", "/-dash", "/日本語/パス", "'",
	} {
		out, err := exec.Command("/bin/bash", "-c", "printf %s "+shellQuote(path)).Output()
		if err != nil || string(out) != path {
			t.Errorf("%q came back as %q (%v)", path, out, err)
		}
	}
}

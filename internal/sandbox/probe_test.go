package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// The control was a shell outside the sandbox writing into the probe's
// path, and a link put under that path had the write go through it
// (measured, ADR-0121). The control writes through the descriptor kept
// from the probe's making and opens no name: whatever sits under the
// name by then, it does not wait, and what the name leads to is as it
// was. The clean-up removes the name it made and nothing the name leads
// to.
func TestTheControlWritesOnlyTheProbeThatWasMade(t *testing.T) {
	p, name, dir, _ := probeFixture(t)
	if err := p.write(name); err != nil {
		t.Fatalf("the probe as it was made: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != "x\n" {
		t.Errorf("the probe holds %q", got)
	}
	if err := p.read(name); err != nil {
		t.Errorf("the probe read back: %v", err)
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
		"a FIFO nobody reads": func(t *testing.T, path, _, _ string) {
			must(t, syscall.Mkfifo(path, 0o600))
		},
		"nothing": func(t *testing.T, path, _, _ string) {},
	} {
		t.Run(what, func(t *testing.T) {
			p, name, dir, victim := probeFixture(t)
			path := filepath.Join(dir, name)
			must(t, os.Remove(path))
			replace(t, path, dir, victim)
			var err error
			soon(t, path, func() { err = p.write(name) })
			if err != nil {
				t.Errorf("the control: %v — it writes the file it holds, whatever the name is now", err)
			}
			kept(t, victim)
			if strings.Contains(what, "in the directory") {
				kept(t, filepath.Join(dir, "inside"))
			}
			if left := p.cleanup(); len(left) != 0 {
				t.Errorf("left = %v", left)
			}
			kept(t, victim)
			if strings.Contains(what, "in the directory") {
				kept(t, filepath.Join(dir, "inside"))
			}
		})
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
		if err := p.write(name); !errors.Is(err, fs.ErrExist) {
			t.Errorf("%s: %v, want the name refused as taken", name, err)
		}
	}
	kept(t, victim)
	if _, err := os.Lstat(fresh); !os.IsNotExist(err) {
		t.Errorf("a file was made through a link: %v", err)
	}
}

// The clean-up removed probes by path, and a probe directory replaced
// by a link had files removed from the directory the link led to
// (measured, ADR-0121). It removes names from the directories it holds:
// the files beside the project that carry the probes' names stay, and
// the files the check made go from the directory it holds.
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
	// Moved aside, and a link to the outside directory put under its name.
	moved := filepath.Join(held, "moved")
	must(t, os.Rename(dir.path, moved))
	must(t, os.Symlink(outside, dir.path))
	// The control still writes into the directory that was made.
	must(t, dir.write("AGENTS.md"))
	if _, err := os.Stat(filepath.Join(moved, "AGENTS.md")); err != nil {
		t.Errorf("the control did not write into the directory it holds: %v", err)
	}
	left := root.cleanup()
	for _, n := range names {
		kept(t, filepath.Join(outside, n))
	}
	if entries, _ := os.ReadDir(moved); len(entries) != 0 {
		t.Errorf("the check's own files were left in the directory it held: %v", entries)
	}
	// What is left to report is the directory the check made, which is
	// no longer under the name it is removed by.
	for _, l := range left {
		if strings.HasPrefix(l, outside) {
			t.Errorf("left names %s: a file beside the project is not the check's leftover", l)
		}
	}
}

// The bound ends the command and what it started: a shell ended alone
// leaves the program it was waiting for, here a cat on a FIFO, and one
// such was left at every start.
func TestTheBoundEndsWhatTheProbeStarted(t *testing.T) {
	old := probeTimeout
	probeTimeout = time.Second // time for the shell to start its program and say which
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
	if derr := Denial(err, LaneRead, "a write into the project", "allowed a write"); derr == nil {
		t.Error("a probe that did not finish was counted as denied")
	} else if want := "the read lane's probe of a write into the project showed nothing: the command did not finish within 300ms"; derr.Error() != want {
		t.Errorf("the note reads %q, want %q", derr, want)
	}
	if err := Denial(nil, LaneRead, "a write", "allowed a write"); err == nil || !strings.Contains(err.Error(), "the read lane allowed a write") {
		t.Errorf("a probe that was allowed: %v", err)
	}
	if err := Denial(&exec.ExitError{}, LaneRead, "a write", "allowed a write"); err != nil {
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

// checksFixture is a project, a write root, a read root and a scratch
// directory under vol, with the three lanes' profiles.
func checksFixture(t *testing.T, vol string) (base string, spec Spec, profiles map[Lane]string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	base, err := os.MkdirTemp(vol, "probe-volume-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = ResolveWriteDir(base)
	home, _ := os.UserHomeDir()
	proj, wroot, rroot, scratch := filepath.Join(base, "proj"), filepath.Join(base, "notes"), filepath.Join(base, "ref"), filepath.Join(base, "scratch")
	for _, d := range []string{proj, wroot, rroot, scratch} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	spec = Spec{ProjectDir: proj, Home: home, DenyExec: DefaultDenyExec, ReadScratch: scratch,
		Added: []AddedRoot{{Dir: wroot, Write: true}, {Dir: rroot}}}
	profiles = map[Lane]string{}
	for _, l := range []Lane{LaneRead, LaneWrite, LaneOperator} {
		p, err := LaneProfile(l, spec)
		if err != nil {
			t.Fatal(err)
		}
		profiles[l] = p
	}
	return base, spec, profiles
}

// The read lane's scratch probe was one fixed name in the scratch
// directory, which the write lane writes: a link put under it
// beforehand — no race in that — had the lane's own write go through
// it. The name is one nobody could have known.
func TestTheScratchProbeHasNoNameToWaitUnder(t *testing.T) {
	_, spec, profiles := checksFixture(t, t.TempDir())
	target := filepath.Join(spec.ReadScratch, "other.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(spec.ReadScratch, ".gem-agent-lane-probe")
	if err := os.Symlink("other.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReadLane(profiles[LaneRead], spec); err != nil {
		t.Errorf("the read lane with a bystander link in its scratch directory: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep\n" {
		t.Errorf("the probe wrote through a link that was waiting for it: %q", got)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the probe removed a name it did not make: %v", err)
	}
}

// The checks pass, twice over, and leave nothing behind — on the volume
// the tests run on, and on one named with PROBE_TEST_VOLUME: a volume
// that is not a local disk gives a file its number and its times in
// ways of its own, and the checks have to pass there too.
func TestTheChecksPassOnTheVolumeNamed(t *testing.T) {
	vol := os.Getenv("PROBE_TEST_VOLUME")
	if vol == "" {
		vol = t.TempDir()
	}
	base, spec, profiles := checksFixture(t, vol)
	for round := 1; round <= 2; round++ {
		if left, err := VerifyReadLane(profiles[LaneRead], spec); err != nil || len(left) != 0 {
			t.Errorf("round %d, the read lane: %v, left %v", round, err, left)
		}
		if left, err := VerifyWriteLane(profiles[LaneWrite], spec); err != nil || len(left) != 0 {
			t.Errorf("round %d, the write lane: %v, left %v", round, err, left)
		}
		if notes, left, err := VerifyAddedRoots(profiles, spec, true); err != nil || len(left) != 0 || len(notes) != 0 {
			t.Errorf("round %d, the added directories: %v, left %v, notes %v", round, err, left, notes)
		}
		fileDir := filepath.Join(base, "file-probe")
		if err := os.Mkdir(fileDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := VerifyFileReadLane(fileDir, spec.Home, func(_, path string) error {
			if filepath.Base(path) == ".env" {
				return errors.New("exit status 3")
			}
			return nil
		}); err != nil {
			t.Errorf("round %d, the file-read check's own probes: %v", round, err)
		}
		for _, d := range []string{spec.ProjectDir, spec.Added[0].Dir, spec.Added[1].Dir, spec.ReadScratch, fileDir} {
			if entries, _ := os.ReadDir(d); len(entries) != 0 {
				var names []string
				for _, e := range entries {
					names = append(names, e.Name())
				}
				t.Errorf("round %d: left in %s: %v", round, filepath.Base(d), names)
			}
		}
		if err := os.Remove(fileDir); err != nil {
			t.Fatal(err)
		}
	}
}

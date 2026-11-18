package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// swapFixture is a project and a write root under home, with a
// directory beside them that no lane may write: the files in it are
// what a replaced probe would lead to.
type swapFixture struct {
	spec     Spec
	profiles map[Lane]string
	outside  string            // the directory beside the project
	kept     map[string]string // every file in it, and what it holds
	stamp    time.Time         // when the directory itself last changed
}

func newSwapFixture(t *testing.T) *swapFixture {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	base, err := os.MkdirTemp(home, ".gem-agent-probe-swap-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, _ = ResolveWriteDir(base)
	f := &swapFixture{outside: filepath.Join(base, "outside"), kept: map[string]string{}}
	proj, added, scratch, approved := filepath.Join(base, "proj"), filepath.Join(base, "notes"), filepath.Join(base, "scratch"), filepath.Join(base, "approved")
	for _, d := range []string{proj, added, scratch, approved, f.outside} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// One file under every name a probe uses, and one more.
	for _, name := range []string{"victim.txt", "AGENTS.md", ".env", "plain", "created"} {
		f.kept[name] = "keep " + name + "\n"
		if err := os.WriteFile(filepath.Join(f.outside, name), []byte(f.kept[name]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if st, err := os.Stat(f.outside); err == nil {
		f.stamp = st.ModTime()
	}
	f.spec = Spec{ProjectDir: proj, Home: home, DenyExec: DefaultDenyExec, ReadScratch: scratch, ApprovedCacheRoot: approved, Added: []AddedRoot{{Dir: added, Write: true}}}
	f.profiles = map[Lane]string{}
	for _, l := range []Lane{LaneRead, LaneWrite, LaneOperator} {
		p, err := LaneProfile(l, f.spec)
		if err != nil {
			t.Fatal(err)
		}
		f.profiles[l] = p
	}
	return f
}

// changed lists what is no longer as it was in the outside directory.
func (f *swapFixture) changed() []string {
	var out []string
	for name, want := range f.kept {
		got, err := os.ReadFile(filepath.Join(f.outside, name))
		if err != nil {
			out = append(out, name+" is gone")
		} else if string(got) != want {
			out = append(out, name+" now holds "+strings.TrimSpace(string(got)))
		}
	}
	entries, _ := os.ReadDir(f.outside)
	for _, e := range entries {
		if _, ok := f.kept[e.Name()]; !ok {
			out = append(out, e.Name()+" was created")
		}
	}
	// A file made there and removed again leaves only this.
	if st, err := os.Stat(f.outside); err != nil || !st.ModTime().Equal(f.stamp) {
		out = append(out, "the directory itself was changed")
	}
	sort.Strings(out)
	return out
}

// racer runs, under the real write-lane profile, a loop that replaces
// every probe it sees in the project and the write root — a file by
// replaceFile, a directory by replaceDir, each a shell command on "$p"
// with the outside directory in "$O"; prepare runs before every look,
// for what a replacement needs ready. It returns a function that stops
// it.
func (f *swapFixture) racer(t *testing.T, prepare, replaceFile, replaceDir string) func() {
	t.Helper()
	script := `
P=` + shellQuote(f.spec.ProjectDir) + `; A=` + shellQuote(f.spec.Added[0].Dir) + `; O=` + shellQuote(f.outside) + `
n=0
while [ $SECONDS -lt 180 ]; do
  n=$((n+1))
  ` + prepare + `
  for p in "$P"/.gem-agent-lane-probe-* "$A"/.gem-agent-root-probe-*; do
    if [ -L "$p" ] || [ -p "$p" ]; then continue; fi
    if [ -d "$p" ]; then
      ` + replaceDir + `
    elif [ -f "$p" ]; then
      ` + replaceFile + `
    fi
  done
done`
	ctx, cancel := context.WithCancel(context.Background())
	argv := Wrap(f.profiles[LaneWrite], "/bin/bash", script)
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = ChildEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	return func() {
		cancel()
		_ = cmd.Wait()
	}
}

// within runs fn and reports whether it came back in time.
func cameBack(d time.Duration, fn func()) bool {
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// The checks make probe files where the lanes can write, and a process
// in the write lane — a child an earlier command left running — can
// replace one between its creation and its use. The control of each
// probe was a shell outside the sandbox writing into the probe's name:
// replaced by a link, the write went through it to a file no lane may
// write, and the clean-up removed files by the same names. Here such a
// process runs under the real write-lane profile while every check is
// run again and again, and nothing outside is touched.
func TestTheChecksDoNotWriteThroughAProbeThatWasReplaced(t *testing.T) {
	f := newSwapFixture(t)
	// What the racer does it does within the write lane: it cannot
	// write the outside directory itself.
	argv := Wrap(f.profiles[LaneWrite], "/bin/bash", "echo x > "+shellQuote(filepath.Join(f.outside, "victim.txt")))
	if exec.Command(argv[0], argv[1:]...).Run() == nil || len(f.changed()) != 0 {
		t.Fatal("the write lane can write the outside directory: the fixture shows nothing")
	}
	for what, r := range map[string][2]string{
		"a probe file replaced by a link":      {`/bin/ln -sf "$O/victim.txt" "$p"`, `:`},
		"a probe directory replaced by a link": {`:`, `/bin/mv "$p" "$P/moved-$n" 2>/dev/null && /bin/ln -s "$O" "$p"`},
	} {
		t.Run(what, func(t *testing.T) {
			stop := f.racer(t, `:`, r[0], r[1])
			defer stop()
			// Fifteen rounds as a rule, and more while no check has met
			// a replaced probe: a run in which the loop never got in
			// between has shown nothing.
			replaced := 0
			for i := 0; i < 15 || (replaced == 0 && i < 200); i++ {
				replaced += f.checks()
				if c := f.changed(); len(c) != 0 {
					t.Fatalf("round %d: outside the lanes, %s", i+1, strings.Join(c, "; "))
				}
			}
			if replaced == 0 {
				t.Error("no check met a replaced probe: the replacement was not exercised")
			}
			t.Logf("%d checks met a replaced probe and failed; nothing outside was touched", replaced)
		})
	}
}

// Replaced by a FIFO, a probe held the shell that wrote into it, and
// the start with it: no check had a bound in time.
func TestTheChecksDoNotWaitOnAProbeThatWasReplaced(t *testing.T) {
	f := newSwapFixture(t)
	stop := f.racer(t, `[ -p "$P/ready" ] || /usr/bin/mkfifo "$P/ready"`, `/bin/mv -f "$P/ready" "$p"`, `:`)
	defer stop()
	replaced := 0
	for i := 0; i < 15 || (replaced == 0 && i < 200); i++ {
		if !cameBack(60*time.Second, func() { replaced += f.checks() }) {
			t.Fatalf("round %d: a check waited on a probe replaced by a FIFO", i+1)
		}
	}
	if replaced == 0 {
		t.Error("no check met a replaced probe: the replacement was not exercised")
	}
	t.Logf("%d checks met a replaced probe and failed; none waited", replaced)
}

// checks runs every check once and counts those that failed on a
// probe that had been replaced.
func (f *swapFixture) checks() (replaced int) {
	_, err := VerifyReadLane(f.profiles[LaneRead], f.spec)
	_, werr := VerifyWriteLane(f.profiles[LaneWrite], f.spec)
	_, _, aerr := VerifyAddedRoots(f.profiles, f.spec, true)
	for _, err := range []error{err, werr, aerr} {
		if errors.Is(err, errProbeReplaced) {
			replaced++
		}
	}
	return replaced
}

// The same, without the race: each probe file in turn — the project's,
// the shared temporary directory's, the approved cache's, the one under
// home — is replaced at the moment it has been made, and at the moment
// its control has written it: by a link to a file outside, and by a
// hard link to another file of the project. Each time the check fails
// and says the probe was replaced, and neither file is written.
func TestAProbeReplacedAtEitherMomentFailsTheCheck(t *testing.T) {
	f := newSwapFixture(t)
	inside := filepath.Join(f.spec.ProjectDir, "inside.txt")
	if err := os.WriteFile(inside, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer func() { probeStep = func(string, string) {} }()
	checks := map[string]struct {
		probes int
		run    func() error
	}{
		"the read lane": {3, func() error {
			_, err := VerifyReadLane(f.profiles[LaneRead], f.spec)
			return err
		}},
		"the write lane": {1, func() error {
			_, err := VerifyWriteLane(f.profiles[LaneWrite], f.spec)
			return err
		}},
	}
	for _, stage := range []string{"made", "controlled"} {
		for what, replace := range map[string]func(path string) error{
			"a link to a file outside": func(path string) error {
				_ = os.Remove(path)
				return os.Symlink(filepath.Join(f.outside, "victim.txt"), path)
			},
			"a hard link to a file of the project": func(path string) error {
				_ = os.Remove(path)
				return os.Link(inside, path)
			},
		} {
			for check, c := range checks {
				for nth := 0; nth < c.probes; nth++ {
					// The nth probe to reach that moment, the first time
					// it does: a control is made twice.
					seen, hit := map[string]bool{}, ""
					probeStep = func(at, path string) {
						if at != stage || seen[path] {
							return
						}
						if seen[path] = true; len(seen) == nth+1 {
							hit = path
							if err := replace(path); err != nil {
								t.Errorf("replacing %s: %v", path, err)
							}
						}
					}
					err := c.run()
					probeStep = func(string, string) {}
					where := check + ", " + what + " at " + stage + ", probe " + hit
					if hit == "" {
						t.Errorf("%s: probe %d never reached that moment", where, nth+1)
					}
					if !errors.Is(err, errProbeReplaced) {
						t.Errorf("%s: %v, want the check failed on a replaced probe", where, err)
					}
					_ = os.Remove(hit) // what the test put there is the test's to remove
					if ch := f.changed(); len(ch) != 0 {
						t.Fatalf("%s: outside the lanes, %s", where, strings.Join(ch, "; "))
					}
					if got, _ := os.ReadFile(inside); string(got) != "keep\n" {
						t.Fatalf("%s: a file of the project now holds %q", where, got)
					}
				}
			}
		}
	}
}

// A lane's command can be made to fail without the lane refusing it
// and without the probe being replaced: the probe closed to everybody,
// or the name a command would make taken by something else. Either
// made a lane that denies nothing read as one that denied. The control
// is made again after the refusal, and a probe that can no longer be
// written here, or a name no longer free, fails the check.
func TestAProbeChangedUnderTheLanesCommandFailsTheCheck(t *testing.T) {
	f := newSwapFixture(t)
	defer func() { probeStep = func(string, string) {} }()
	// An allow-everything profile with the one write refused that the
	// probe is closed to: what a broken lane and a helpful bystander
	// look like together. The real profile refuses these writes itself,
	// so with it the second control would have nothing to tell apart.
	open := "(version 1)(allow default)"
	once := func(stage string, change func(path string)) {
		done := false
		probeStep = func(at, path string) {
			if at == stage && !done {
				done = true
				change(path)
			}
		}
	}
	once("controlled", func(path string) { _ = os.Chmod(path, 0) })
	_, err := VerifyReadLane(open, f.spec)
	probeStep = func(string, string) {}
	if err == nil || strings.Contains(err.Error(), "allowed") {
		t.Errorf("a probe closed to everybody: %v, want the check failed by its second control", err)
	}
	once("dropped", func(path string) { _ = os.Mkdir(path, 0o700) })
	_, err = VerifyWriteLane(open, f.spec)
	probeStep = func(string, string) {}
	if !errors.Is(err, errProbeReplaced) {
		t.Errorf("a name the lane's command would make, taken: %v, want the check failed on it", err)
	}
	if ch := f.changed(); len(ch) != 0 {
		t.Fatalf("outside the lanes, %s", strings.Join(ch, "; "))
	}
}

// The file-read check counts one outcome of its caged read as the
// cage's: the child's own report that the kernel refused it. A child
// that failed for another reason, a probe that is gone, and a probe
// closed to everybody each fail the check.
func TestTheFileReadCheckCountsOnlyTheCagesRefusal(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	refused := fmt.Errorf("%w: exit status 3", ErrCageRefused)
	for what, c := range map[string]struct {
		secret func(path string) error
		passes bool
	}{
		"the cage refused it":                         {func(string) error { return refused }, true},
		"the child read it":                           {func(string) error { return nil }, false},
		"the child failed":                            {func(string) error { return errors.New("exit status 1") }, false},
		"the child did not finish":                    {func(string) error { return fmt.Errorf("%w within 10s", errProbeTimeout) }, false},
		"the probe was removed":                       {func(p string) error { _ = os.Remove(p); return refused }, false},
		"the probe was closed":                        {func(p string) error { _ = os.Chmod(p, 0); return refused }, false},
		"the probe became a link":                     {func(p string) error { _ = os.Remove(p); _ = os.Symlink("probe-ordinary.txt", p); return refused }, false},
		"the probe was closed, and the child said so": {func(p string) error { _ = os.Chmod(p, 0); return errors.New("permission denied") }, false},
	} {
		dir := t.TempDir()
		err := VerifyFileReadLane(dir, filepath.Dir(dir), func(_, path string) error {
			if filepath.Base(path) == ".env" {
				return c.secret(path)
			}
			return nil
		})
		if (err == nil) != c.passes {
			t.Errorf("%s: %v", what, err)
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.Type()&os.ModeSymlink == 0 {
				t.Errorf("%s: the check left %s behind", what, e.Name())
			}
		}
	}
}

// The read lane's own scratch directory is in the work directory, which
// the write lane writes, and the probe that the read lane can write it
// used one fixed name: a link put under that name beforehand — no race
// to win — had `x` written through it. The name is one nobody could
// have known.
func TestTheScratchProbeHasNoNameToWaitUnder(t *testing.T) {
	f := newSwapFixture(t)
	target := filepath.Join(f.spec.ReadScratch, "other.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.spec.ReadScratch, ".gem-agent-lane-probe")
	if err := os.Symlink("other.txt", link); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReadLane(f.profiles[LaneRead], f.spec); err != nil {
		t.Errorf("the read lane with a bystander link in its scratch directory: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep\n" {
		t.Errorf("the probe wrote through a link that was waiting for it: %q", got)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the probe removed a name it did not make: %v", err)
	}
}

// The added directory's check is held to the same: its control is made
// again after the refusal, and its probe directory must still be under
// its name. Here the read lane refuses nothing, and something else
// makes its commands fail — a name taken, a probe closed to everybody —
// or the probe directory is replaced under the commands: each fails the
// check, in the added directory and for the write lane's own probe
// directory.
func TestAnAddedDirectorysProbesAreHeldToTheSame(t *testing.T) {
	f := newSwapFixture(t)
	defer func() { probeStep = func(string, string) {} }()
	once := func(stage, base string, change func(path string)) {
		done := false
		probeStep = func(at, path string) {
			if at == stage && !done && (base == "" || filepath.Base(path) == base) {
				done = true
				change(path)
			}
		}
	}
	weak := map[Lane]string{LaneRead: "(version 1)(allow default)", LaneWrite: f.profiles[LaneWrite], LaneOperator: f.profiles[LaneOperator]}

	once("dropped", "created", func(path string) { _ = os.Mkdir(path, 0o700) })
	_, _, err := VerifyAddedRoots(weak, f.spec, true)
	if !errors.Is(err, errProbeReplaced) {
		t.Errorf("a name the read lane's command would make, taken: %v, want the check failed on it", err)
	}
	// A read lane that refuses writes as it should and reads a
	// credential name in the added directory: the fault the credential
	// probe is there to find, hidden by the probe being closed.
	reads := map[Lane]string{
		LaneRead:  f.profiles[LaneRead] + "(allow file-read* (subpath " + sbplString(f.spec.Added[0].Dir) + "))\n",
		LaneWrite: f.profiles[LaneWrite], LaneOperator: f.profiles[LaneOperator],
	}
	if _, _, err := VerifyAddedRoots(reads, f.spec, true); err == nil || !strings.Contains(err.Error(), "read a credential name") {
		t.Fatalf("the fixture's read lane does not read a credential name: %v", err)
	}
	once("controlled", ".env", func(path string) { _ = os.Chmod(path, 0) })
	_, _, err = VerifyAddedRoots(reads, f.spec, true)
	if err == nil || !strings.Contains(err.Error(), "after the lane refused it") {
		t.Errorf("a credential probe closed to everybody: %v, want the check failed by its second control", err)
	}

	// The probe directory moved aside under the lane's command, and a
	// link to an empty directory put under its name: with the real
	// profiles, so that nothing but the look at the name can tell.
	swap := func(elsewhere string) func(path string) {
		return func(path string) {
			dir := filepath.Dir(path)
			if err := os.Mkdir(elsewhere, 0o700); err != nil {
				t.Error(err)
			}
			if err := os.Rename(dir, dir+".moved"); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(elsewhere, dir); err != nil {
				t.Error(err)
			}
		}
	}
	once("dropped", "created", swap(filepath.Join(f.spec.Added[0].Dir, "elsewhere")))
	_, _, err = VerifyAddedRoots(f.profiles, f.spec, true)
	if !errors.Is(err, errProbeReplaced) {
		t.Errorf("an added directory's probe directory replaced under the command: %v, want the check failed on it", err)
	}
	once("dropped", "AGENTS.md", swap(filepath.Join(f.spec.ProjectDir, "elsewhere")))
	_, err = VerifyWriteLane(f.profiles[LaneWrite], f.spec)
	if !errors.Is(err, errProbeReplaced) {
		t.Errorf("the write lane's probe directory replaced under the command: %v, want the check failed on it", err)
	}
	probeStep = func(string, string) {}
	if ch := f.changed(); len(ch) != 0 {
		t.Fatalf("outside the lanes, %s", strings.Join(ch, "; "))
	}
}

// TestTheChecksPassOnTheVolumeNamed runs every check with the project
// and the added directories on the volume PROBE_TEST_VOLUME names a
// directory of, and holds them to passing and to leaving nothing. It is
// for volumes that are not like a local disk: on FAT and exFAT a file's
// number changes at its first write, and a check that remembered the
// number from the making took every probe there for a replaced one and
// left it behind (found by the review before the release; ADR-0121).
// Without the variable it runs on the temporary directory's volume.
func TestTheChecksPassOnTheVolumeNamed(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	vol := os.Getenv("PROBE_TEST_VOLUME")
	if vol == "" {
		vol = t.TempDir()
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
	spec := Spec{ProjectDir: proj, Home: home, DenyExec: DefaultDenyExec, ReadScratch: scratch,
		Added: []AddedRoot{{Dir: wroot, Write: true}, {Dir: rroot}}}
	profiles := map[Lane]string{}
	for _, l := range []Lane{LaneRead, LaneWrite, LaneOperator} {
		p, err := LaneProfile(l, spec)
		if err != nil {
			t.Fatal(err)
		}
		profiles[l] = p
	}
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
		refused := fmt.Errorf("%w: exit status 3", ErrCageRefused)
		if err := VerifyFileReadLane(fileDir, home, func(_, path string) error {
			if filepath.Base(path) == ".env" {
				return refused
			}
			return nil
		}); err != nil {
			t.Errorf("round %d, the file-read check's own probes: %v", round, err)
		}
		for _, d := range []string{proj, wroot, rroot, scratch, fileDir} {
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

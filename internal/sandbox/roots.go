package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// RootContext is what RefuseRoot judges an added directory against
// (ADR-0099 §5). Every path is the real one; an empty field is not
// judged.
type RootContext struct {
	Home       string
	ProjectDir string
	WorkDir    string
	// ConfigHome is the runtime's configuration home
	// (~/.config/gem-agent) and StateDir its state root
	// (~/.local/state/gem-agent): what later sessions trust and record.
	ConfigHome string
	StateDir   string
	// ConfigFile is the configuration file in effect (--config may name
	// one outside ConfigHome): a write root may not contain it, or the
	// model could rewrite the operator's hooks, approvals and roots for
	// every later session.
	ConfigFile string
	// Roots are the directories already added in this session.
	Roots []AddedRoot
}

// RefuseRoot decides whether path may become an added root at the given
// level (ADR-0099 §5) and returns its real path. It is the one rule the
// startup flags, the configuration and (later) the model's request all
// read. A refusal names the reason in the operator's terms.
func RefuseRoot(path string, write bool, c RootContext) (string, error) {
	real, err := ResolveWriteDir(path)
	if err != nil {
		return "", fmt.Errorf("%s: not found", path)
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%s: not a directory", path)
	}
	refuse := func(why string) (string, error) {
		return "", fmt.Errorf("%s cannot be added: %s", real, why)
	}
	if real == "/" {
		return refuse("it is the root of the filesystem")
	}
	if c.Home != "" && within(real, c.Home) {
		return refuse("it is the home directory or contains it")
	}
	for _, x := range []struct{ dir, what string }{
		{c.ProjectDir, "the project"},
		{c.WorkDir, "the session work directory"},
		{c.ConfigHome, "the gem-agent configuration home"},
		{c.StateDir, "the gem-agent state directory"},
	} {
		if x.dir != "" && within(real, x.dir) && real != x.dir {
			return refuse("it contains " + x.what)
		}
	}
	for _, x := range []struct{ dir, what string }{
		{c.ConfigHome, "the gem-agent configuration home"},
		{c.StateDir, "the gem-agent state directory"},
	} {
		if x.dir != "" && within(x.dir, real) {
			return refuse("it lies inside " + x.what)
		}
	}
	if c.Home != "" {
		h := filepath.Clean(c.Home)
		var creds []string
		for _, d := range credentialDirs {
			creds = append(creds, filepath.Join(h, d))
		}
		for _, f := range credentialFiles {
			creds = append(creds, filepath.Join(h, f))
		}
		for _, cr := range creds {
			if within(cr, real) || within(real, cr) {
				return refuse("it is, lies inside or contains a credential location (" + cr + ")")
			}
		}
		if write && within(filepath.Join(h, "Library"), real) {
			return refuse("a write root may not lie inside ~/Library: programs there run outside the sandbox")
		}
	}
	if write && c.ConfigFile != "" && within(real, c.ConfigFile) {
		return refuse("a write root may not contain the gem-agent configuration file in use (" + c.ConfigFile + ")")
	}
	// A shared scratch root lets any write-lane command rename the root
	// away and put another directory in its place: the profile names a
	// path and the file tools hold a descriptor, so the two would part.
	for _, s := range append(ScratchDirs(), "/private/var/tmp") {
		if within(s, real) {
			return refuse("it lies inside a shared temporary directory (" + s + ")")
		}
	}
	// A root that contains a scratch root or the devices would take the
	// lanes' own allowances with it: the write lanes write scratch under
	// a read root the file tools refuse, and a re-allow over /dev reopens
	// the terminal devices every lane denies.
	for _, s := range append(ScratchDirs(), "/private/var/tmp", "/dev") {
		if within(real, s) {
			return refuse("it contains a shared temporary directory or the devices (" + s + ")")
		}
	}
	// Already covered: the project and the work directory are write
	// roots; an added root covers what lies under it at its level.
	covers := []AddedRoot{{Dir: c.ProjectDir, Write: true}, {Dir: c.WorkDir, Write: true}}
	covers = append(covers, c.Roots...)
	for _, r := range covers {
		if r.Dir != "" && within(r.Dir, real) && (r.Write || !write) {
			return "", fmt.Errorf("%s is already covered by %s (%s)", real, r.Dir, r.Access())
		}
	}
	return real, nil
}

// VerifyAddedRoots probes each added root under the real profiles
// (ADR-0099 §7): the startup probes prove the cage around the project
// and home, not where a new root sits in it. For every root the read
// lane must not create a file there and must not read a credential name
// there, and must read an ordinary file there (the re-allow under a
// tree denied for reach); a write root must be writable by the write
// lane with its persistent names denied, and a read root writable by no
// lane. Each must-fail probe runs first without the sandbox as its
// control. The probe files are created under a random name and removed
// by full path.
//
// A read root the operator's own process cannot write (a read-only
// volume) cannot hold a probe; nothing can write it either, so its
// write probes are moot and its credential probe is left to the
// profile's order test — the note says so.
//
// readLane is whether the startup probes verified the read lane: when
// they did not, every read-lane call already asks, and a read-lane probe
// failing for the same unrelated reason must not be blamed on the root —
// the read-lane probes are skipped with a note, the others still run.
func VerifyAddedRoots(profiles map[Lane]string, spec Spec, readLane bool) (notes []string, err error) {
	if !readLane && len(spec.Added) > 0 {
		notes = append(notes, "added directories: the read lane was not verified, so their read-lane probes were skipped")
	}
	for _, a := range spec.Added {
		n, err := verifyAddedRoot(profiles, a, readLane)
		if err != nil {
			return notes, fmt.Errorf("added directory %s (%s): %w", a.Dir, a.Access(), err)
		}
		notes = append(notes, n...)
	}
	return notes, nil
}

func verifyAddedRoot(profiles map[Lane]string, a AddedRoot, readLane bool) ([]string, error) {
	probeDir, err := os.MkdirTemp(a.Dir, ".gem-agent-root-probe-*")
	if err != nil {
		if a.Write {
			return nil, fmt.Errorf("cannot create a probe in it: %w", err)
		}
		return []string{fmt.Sprintf("added directory %s is not writable here, so its probes were skipped", a.Dir)}, nil
	}
	cred := filepath.Join(probeDir, ".env")
	plain := filepath.Join(probeDir, "plain")
	created := filepath.Join(probeDir, "created")
	persistent := filepath.Join(probeDir, "AGENTS.md")
	defer func() {
		for _, f := range []string{cred, plain, created, persistent} {
			_ = os.Remove(f)
		}
		_ = os.Remove(probeDir)
	}()
	for _, f := range []string{cred, plain} {
		if err := os.WriteFile(f, []byte("probe\n"), 0o600); err != nil {
			return nil, fmt.Errorf("cannot create a probe file in it: %w", err)
		}
	}
	run := func(lane Lane, command string) error {
		argv := Wrap(profiles[lane], "/bin/bash", command)
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = ChildEnv(os.Environ())
		return cmd.Run()
	}
	control := func(command, made string) error {
		cmd := exec.Command("/bin/bash", "-c", command)
		cmd.Env = ChildEnv(os.Environ())
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("the control run of %q failed outside the sandbox (%v) — the probe cannot tell a denial from an unrelated failure", command, err)
		}
		if made != "" {
			_ = os.Remove(made)
		}
		return nil
	}
	mustFail := func(lane Lane, command, made, what string) error {
		if err := control(command, made); err != nil {
			return err
		}
		if run(lane, command) == nil {
			_ = os.Remove(made)
			return fmt.Errorf("the %s lane %s", lane, what)
		}
		return nil
	}
	write := "echo x > " + shellQuote(created)
	if readLane {
		if err := mustFail(LaneRead, write, created, "created a file in it"); err != nil {
			return nil, err
		}
		if err := mustFail(LaneRead, "cat "+shellQuote(cred)+" > /dev/null", "", "read a credential name in it"); err != nil {
			return nil, err
		}
		if err := run(LaneRead, "cat "+shellQuote(plain)+" > /dev/null"); err != nil {
			return nil, fmt.Errorf("the read lane cannot read an ordinary file in it: %w", err)
		}
	}
	if a.Write {
		if err := run(LaneWrite, write); err != nil {
			return nil, fmt.Errorf("the write lane cannot write it: %w", err)
		}
		_ = os.Remove(created)
		if err := mustFail(LaneWrite, "echo x > "+shellQuote(persistent), persistent, "wrote an instruction file's name in it"); err != nil {
			return nil, err
		}
		return nil, nil
	}
	for _, lane := range []Lane{LaneWrite, LaneOperator} {
		if err := mustFail(lane, write, created, "wrote a read-only added directory"); err != nil {
			return nil, err
		}
	}
	return nil, nil
}

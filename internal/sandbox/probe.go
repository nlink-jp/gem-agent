package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// The checks make probes where the lanes write. A shell outside the
// sandbox that wrote into a probe's path wrote through a link put in
// its place, and the removal of probes by path removed files through a
// directory put in their directory's place (measured under the real
// write-lane profile, ADR-0121). What is here keeps the runtime's own
// writes and removals to the probes it made: a probe file stays open
// from its making and is written through that, and a name is removed
// through the directory it was made in, held open.
//
// It does not defend a check's verdict against a process that races the
// check (ADR-0122 §3): a check that passes wrongly makes no lane weaker.

// probeTimeout is how long one probe command may take. An ordinary one
// takes a hundredth of a second and the slowest measured five
// hundredths; one that has not finished by then is waiting on something
// a probe never waits on, and its check fails.
var probeTimeout = 10 * time.Second

// errProbeTimeout is a probe command ended at probeTimeout. It is not
// a denial: a command that did not finish showed nothing.
var errProbeTimeout = errors.New("the command did not finish")

// RunProbe runs argv to its end, or ends it at the bound — the command
// and everything it started, which is why it runs in a process group
// of its own: a shell ended alone leaves the program it was waiting
// for. A probe is a child this runtime spawns like any other: its
// environment is the children's, with extra added. stdin may be nil.
func RunProbe(argv []string, stdin io.Reader, extra ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(ChildEnv(os.Environ()), extra...)
	cmd.Stdin = stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("%w within %s", errProbeTimeout, probeTimeout)
	}
	return err
}

func runProbe(argv []string, extra ...string) error { return RunProbe(argv, nil, extra...) }

// Denial is how a probe that must fail in a lane ended: nil when the
// lane's command failed. what is the probe, and allowed what the lane
// did if the command succeeded. A command that was allowed is a failed
// check, and so is one that did not finish.
func Denial(err error, lane Lane, what, allowed string) error {
	switch {
	case err == nil:
		return fmt.Errorf("the %s lane %s", lane, allowed)
	case errors.Is(err, errProbeTimeout):
		return fmt.Errorf("the %s lane's probe of %s showed nothing: %w", lane, what, err)
	}
	return nil
}

// probes is a directory probes are made in, held open as a root. Every
// probe is made and removed through it by its one name, and a probe
// file is written and read through the descriptor kept from its making.
type probes struct {
	root  *os.Root
	path  string // the directory, as a lane's command names it
	names []string
	made  map[string]*os.File
	subs  []*probes
	name  string // for a probe directory: its name in the one it was made in
}

// openProbes holds the directory at path.
func openProbes(path string) (*probes, error) {
	root, err := bounded.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	return &probes{root: root, path: path, made: map[string]*os.File{}}, nil
}

// at is a probe's path, for a lane's command and for a note.
func (p *probes) at(name string) string { return filepath.Join(p.path, name) }

// randomName is prefix and a number nobody could have known beforehand.
func randomName(prefix string) string {
	return prefix + strconv.FormatUint(rand.Uint64(), 36)
}

// newName makes something under a name that starts with prefix and was
// not there: make is tried under one random name after another.
func newName(prefix string, make func(name string) error) (string, error) {
	for try := 0; ; try++ {
		name := randomName(prefix)
		err := make(name)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, fs.ErrExist) || try > 1000 {
			return "", err
		}
	}
}

// file makes an empty probe file under a random name.
func (p *probes) file(prefix string) (string, error) {
	return newName(prefix, func(name string) error { return p.fileNamed(name, "") })
}

// fileNamed makes the probe file name, which must not be there — what
// is under the name is never opened — puts data in it, and keeps it
// open.
func (p *probes) fileNamed(name, data string) error {
	f, err := p.root.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	p.names = append(p.names, name)
	p.made[name] = f
	_, err = f.WriteString(data)
	return err
}

// expect names a file a lane's command may make here, so that it is
// removed with the rest.
func (p *probes) expect(name string) { p.names = append(p.names, name) }

// subdir makes a probe directory under a random name and holds it.
func (p *probes) subdir(prefix string) (*probes, error) {
	name, err := newName(prefix, func(name string) error { return p.root.Mkdir(name, 0o700) })
	if err != nil {
		return nil, err
	}
	sub := &probes{path: p.at(name), made: map[string]*os.File{}, name: name}
	p.subs = append(p.subs, sub)
	if sub.root, err = p.root.OpenRoot(name); err != nil {
		return sub, err
	}
	return sub, nil
}

// write is the control of a write probe: the write a lane's command is
// about to be refused, made here, outside the sandbox, by the runtime
// itself. A probe made beforehand is written through the descriptor
// kept from its making — its name is not opened again, so whatever sits
// under the name by now is not written. A probe the command would make
// is made, under a name that must be free.
func (p *probes) write(name string) error {
	f, made := p.made[name]
	if !made {
		return p.fileNamed(name, "x\n")
	}
	_, err := f.WriteAt([]byte("x\n"), 0)
	return err
}

// read is the control of a read probe, made the same way.
func (p *probes) read(name string) error {
	f, made := p.made[name]
	if !made {
		return fmt.Errorf("%s: %w", p.at(name), fs.ErrNotExist)
	}
	_, err := f.ReadAt(make([]byte, 1), 0)
	return err
}

// drop removes one probe now, between its control and the lane's
// command that must not be able to make it.
func (p *probes) drop(name string) {
	if f, made := p.made[name]; made {
		_ = f.Close()
		delete(p.made, name)
	}
	_ = p.root.Remove(name)
}

// removeProbe removes one name from a directory the checks hold; path
// is that name's path, for a test that makes a removal fail.
var removeProbe = func(root *os.Root, path string) error { return root.Remove(filepath.Base(path)) }

// cleanup removes what the check made — names from the directories
// held, nothing by path — and releases them. It returns the paths it
// could not remove (ADR-0107 §4). In a synced folder a leftover does
// not stay local — it is copied to the operator's other devices and to
// everyone who shares the folder — so it is reported, never left in
// silence. A name already gone is not one.
func (p *probes) cleanup() (left []string) {
	if p == nil || p.root == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, name := range p.names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if f, made := p.made[name]; made {
			_ = f.Close()
			delete(p.made, name)
		}
		if err := removeProbe(p.root, p.at(name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, p.at(name))
		}
	}
	for _, sub := range p.subs {
		left = append(left, sub.cleanup()...)
		if err := removeProbe(p.root, sub.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, sub.path)
		}
	}
	_ = p.root.Close()
	p.root = nil
	return left
}

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

// The checks make probes where the lanes write, so a process in a lane
// can replace one between its making and its use. A shell outside the
// sandbox that wrote into a probe's path wrote through a link put in
// its place, the removal of probes by path removed files through a
// directory put in their directory's place, and a FIFO held the shell
// and the start (all measured under the real write-lane profile,
// ADR-0121). What is here keeps the checks to the probes they made.

// probeTimeout is how long one probe command may take. An ordinary one
// takes a hundredth of a second and the slowest measured five; one
// that has not finished by then is waiting on something a probe never
// waits on, and its check fails.
var probeTimeout = 10 * time.Second

// errProbeTimeout is a probe command ended at probeTimeout. It is not
// a denial: a command that did not finish showed nothing.
var errProbeTimeout = errors.New("did not finish in time")

// errProbeReplaced is a probe that is no longer the file the check
// made. It shows nothing either way, and the check fails.
var errProbeReplaced = errors.New("was replaced while the sandbox was being checked")

// probeStep is called when a probe file has been made ("made"), when
// its control has written it ("controlled") and when a probe the
// control made has been removed again ("dropped"), with the probe's
// path. It does nothing; a test replaces or changes the probe at that
// moment, the one a process in a lane has to hit by chance.
var probeStep = func(stage, path string) {}

// ErrCageRefused is the file-read child reporting that the kernel
// refused it the read: the one outcome of the file-read check's caged
// read that counts as the cage's doing.
var ErrCageRefused = errors.New("refused by the cage")

// errProbeEnded is a probe command ended by a signal that was not the
// bound's: something else ended it, and it showed nothing either.
var errProbeEnded = errors.New("was ended from outside")

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
		return fmt.Errorf("%w (%s)", errProbeTimeout, probeTimeout)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return fmt.Errorf("%w (%s)", errProbeEnded, ws.Signal())
		}
	}
	return err
}

func runProbe(argv []string, extra ...string) error { return RunProbe(argv, nil, extra...) }

// Denial is how a probe that must fail in a lane ended: nil when the
// lane's command ran to its own end and failed. A command that was
// allowed is a failed check, and so is one that did not finish or was
// ended from outside: a process in the write lane can end any of the
// operator's processes, and a command that was ended was not refused.
func Denial(err error, lane Lane, allowed string) error {
	switch {
	case err == nil:
		return fmt.Errorf("the %s lane %s", lane, allowed)
	case errors.Is(err, errProbeTimeout), errors.Is(err, errProbeEnded):
		return fmt.Errorf("a probe of the %s lane %w", lane, err)
	}
	return nil
}

func denial(err error, lane Lane, allowed string) error { return Denial(err, lane, allowed) }

// probes is a directory probes are made in, held open. Every probe is
// made, written, read and removed through it by its one name: no path
// goes through a name a lane can replace.
type probes struct {
	dir   *bounded.Dir
	path  string // the directory, as a lane's command names it
	names []string
	made  map[string]bounded.FileID // the files made here, by name
	subs  []*probes
	// For a probe directory: where it was made, under what name, and
	// which directory it is.
	parent *probes
	name   string
	self   bounded.FileID
	mine   bool // opened, and as empty as it was made
}

// openProbes holds the directory at path.
func openProbes(path string) (*probes, error) {
	d, err := bounded.DirAt(path)
	if err != nil {
		return nil, err
	}
	return &probes{dir: d, path: path, made: map[string]bounded.FileID{}}, nil
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
	name, err := newName(prefix, func(name string) error { return p.fileNamed(name, "") })
	if err == nil {
		probeStep("made", p.at(name))
	}
	return name, err
}

// fileNamed makes the probe file name, which must not be there, and
// puts data in it.
func (p *probes) fileNamed(name, data string) error {
	f, err := p.dir.Create(name, 0o600)
	if err != nil {
		return err
	}
	p.names = append(p.names, name)
	id, err := bounded.IDOf(f)
	if err == nil {
		p.made[name] = id
		_, err = f.WriteString(data)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// expect names a file a lane's command may make here, so that it is
// removed with the rest.
func (p *probes) expect(name string) { p.names = append(p.names, name) }

// mkdirProbe makes a probe directory; a test puts another directory
// under the name between the making and the opening.
var mkdirProbe = func(p *probes, name string) error { return p.dir.Mkdir(name, 0o700) }

// subdir makes a probe directory under a random name and holds it. It
// is opened from this one as a directory and not through a link, and
// must be as empty as it was made; which directory it is, is kept, to
// tell later whether the name still holds it.
func (p *probes) subdir(prefix string) (*probes, error) {
	name, err := newName(prefix, func(name string) error { return mkdirProbe(p, name) })
	if err != nil {
		return nil, err
	}
	sub := &probes{path: p.at(name), made: map[string]bounded.FileID{}, parent: p, name: name}
	p.subs = append(p.subs, sub)
	if sub.dir, err = p.dir.OpenDir(name); err != nil {
		return sub, err
	}
	if sub.self, err = sub.dir.Self(); err != nil {
		return sub, err
	}
	// It was made empty. One that holds something is another directory,
	// moved over the name since: nothing in it is this check's to write
	// or to remove.
	if held, err := sub.dir.List(1); err != nil || len(held) != 0 {
		_ = sub.dir.Close()
		sub.dir = nil
		return sub, fmt.Errorf("%s %w", sub.path, errProbeReplaced)
	}
	sub.mine = true
	return sub, nil
}

// write is the control of a write probe: the write a lane's command is
// about to be refused, made here, outside the sandbox, by the runtime
// itself. A probe made beforehand is opened without waiting and not
// through a link, and written only if it is the file that was made; a
// probe the command would make is made, and the name must be free.
func (p *probes) write(name string) error {
	want, made := p.made[name]
	if !made {
		if err := p.fileNamed(name, "x\n"); err != nil {
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
			}
			return err
		}
		return nil
	}
	f, err := p.dir.OpenWrite(name)
	if err != nil {
		return p.replacedOr(name, err)
	}
	id, err := bounded.IDOf(f)
	if err == nil && id != want {
		err = fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
	}
	if err == nil {
		_, err = f.WriteString("x\n")
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		probeStep("controlled", p.at(name))
	}
	return err
}

// read is the control of a read probe, made the same way.
func (p *probes) read(name string) error {
	f, err := p.dir.OpenRegular(name)
	if err != nil {
		return p.replacedOr(name, err)
	}
	id, err := bounded.IDOf(f)
	if err == nil && id != p.made[name] {
		err = fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
	}
	if err == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(f, 1<<16))
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// replacedOr is err, or that the probe was replaced when it is no
// longer the file that was made: an open refused because a link, a
// FIFO or a directory sits under the name is that, and says so.
func (p *probes) replacedOr(name string, err error) error {
	if p.ours(name) != nil {
		return fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
	}
	return err
}

// ours reports whether name still holds the file that was made.
func (p *probes) ours(name string) error {
	id, _, err := p.dir.ID(name)
	if want, made := p.made[name]; err != nil || !made || id != want {
		return fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
	}
	return nil
}

// absent reports whether nothing is under name: where a lane's command
// was refused the making of a file, a name that holds something by now
// was not what the lane refused.
func (p *probes) absent(name string) error {
	if _, _, err := p.dir.ID(name); !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s %w", p.at(name), errProbeReplaced)
	}
	return nil
}

// inPlace reports whether a probe directory is still under the name it
// was made under: a lane's command names it by path, and a verdict
// about another directory is not one about this.
func (p *probes) inPlace() error {
	id, _, err := p.parent.dir.ID(p.name)
	if err != nil || id != p.self {
		return fmt.Errorf("%s %w", p.path, errProbeReplaced)
	}
	return nil
}

// drop removes one probe now, between its control and the lane's
// command that must not be able to make it.
func (p *probes) drop(name string) {
	_ = p.dir.Remove(name)
	delete(p.made, name)
	probeStep("dropped", p.at(name))
}

// removeProbe removes one name from a directory the checks hold; path
// is that name's path, for a test that makes a removal fail.
var removeProbe = func(d *bounded.Dir, path string, isDir bool) error {
	if isDir {
		return d.RemoveDir(filepath.Base(path))
	}
	return d.Remove(filepath.Base(path))
}

// cleanup removes what the check made — names from the directories
// held, nothing by path — and releases them. A file is removed while
// its name still holds the file that was made, and a probe directory
// while its name still holds that directory; what a lane's command may
// have made in a probe directory is removed by its name there. What
// was put under a probe's name in the meantime is not the check's, and
// is left. It returns the paths of its own that it could not remove
// (ADR-0107 §4). In a synced folder a leftover does not stay local — it
// is copied to the operator's other devices and to everyone who shares
// the folder — so it is reported, never left in silence. A name
// already gone is not one.
func (p *probes) cleanup() (left []string) {
	if p == nil || p.dir == nil {
		return nil
	}
	seen := map[string]bool{}
	for _, name := range p.names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if want, made := p.made[name]; made {
			if id, _, err := p.dir.ID(name); err != nil || id != want {
				continue // gone, or something else by now
			}
		}
		if err := removeProbe(p.dir, p.at(name), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, p.at(name))
		}
	}
	for _, sub := range p.subs {
		left = append(left, sub.cleanup()...)
		if !sub.mine {
			// Never held as this check's own: an empty directory under
			// the name goes, and nothing is claimed about anything else.
			_ = p.dir.RemoveDir(sub.name)
			continue
		}
		if id, _, err := p.dir.ID(sub.name); err != nil || id != sub.self {
			continue // gone, or another directory by now
		}
		if err := removeProbe(p.dir, sub.path, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			left = append(left, sub.path)
		}
	}
	_ = p.dir.Close()
	p.dir = nil
	return left
}

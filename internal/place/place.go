// Package place holds the directories a session took.
//
// A lane writes the project, the session's work directory, its scratch
// and its cache, and an added directory. What a lane writes it can
// replace: remove the directory and put a link under its name. A name
// is then a different place than it was — and the runtime kept names.
// It built the sandbox profiles from them again at every rebuild, so a
// work directory replaced by a link became, at the next rebuild, a
// lane that wrote wherever the link led, with the startup checks
// passing (measured on both runtimes, ADR-0122). It saved results and
// made its own directories under them by path.
//
// A place is taken once: opened as a directory and held. Everything
// that needs it afterwards asks here by the name it was taken under,
// and gets the place that is held — after a look that the name still
// leads to it — or the error that it does not. Nothing resolves the
// name again.
package place

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// ErrReplaced is a place whose name no longer leads to the directory
// that was taken.
var ErrReplaced = errors.New("is no longer the directory this session took")

// ErrNotTaken is a name no place was taken under. It is a fault in the
// runtime, not something a lane did: a place is taken when the session
// makes it or starts in it, before anything asks for it.
var ErrNotTaken = errors.New("was never taken by this session")

// Place is one directory the session took.
type Place struct {
	dir       *bounded.Dir
	real      string
	canonical string   // the kernel's own name for it, when it gave one
	names     []string // every name it answers to here
}

var (
	mu   sync.Mutex
	held = map[string]*Place{}
)

// Take takes the directory at path: the one the path leads to now,
// links resolved this once. It is for a directory somebody named — the
// project the operator started in — at the moment the session takes it
// for what it is. Taken again under the same name, it is the place
// already held, if the name still leads to it.
func Take(path string) (*Place, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if p, err := Of(abs); !errors.Is(err, ErrNotTaken) {
		return p, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	d, err := bounded.DirAt(real)
	if err != nil {
		return nil, err
	}
	p := &Place{dir: d, real: real, names: []string{abs, real}}
	// The kernel's own name for it — the case the volume keeps — is a
	// name callers use too.
	p.canonical = real
	if kernel, err := d.Path(); err == nil {
		p.canonical = kernel
		p.names = append(p.names, kernel)
	}
	if err := p.InPlace(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return register(p), nil
}

func register(p *Place) *Place {
	mu.Lock()
	defer mu.Unlock()
	for _, n := range p.names {
		held[n] = p
	}
	return p
}

// Of is the place taken under name — the name it was taken by, or its
// real path. The name must still lead to it.
func Of(name string) (*Place, error) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	mu.Lock()
	p := held[abs]
	mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("%s %w", abs, ErrNotTaken)
	}
	if err := p.InPlace(); err != nil {
		return nil, err
	}
	return p, nil
}

// Real is the place's path with every link resolved, as it was when
// the place was taken. A sandbox profile is anchored on it.
func (p *Place) Real() string { return p.real }

// Canonical is the place's path as the kernel names it — the case the
// volume keeps — for a judgement that compares such names.
func (p *Place) Canonical() string { return p.canonical }

// Held is the place taken under name, whether or not the name still
// leads to it: for giving a place up, and for a judgement that needs
// what the session took and not what the name holds now.
func Held(name string) (*Place, bool) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return nil, false
	}
	mu.Lock()
	defer mu.Unlock()
	p := held[abs]
	return p, p != nil
}

// RemoveAll removes name and what is under it from the place: through
// a root that is the directory held, so that what goes is in this
// place whatever the place's name leads to by now.
func (p *Place) RemoveAll(name string) error {
	root, err := p.OpenRoot()
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return root.RemoveAll(name)
}

// Dir is the directory held, for making, writing and removing what is
// in it by one name. It is the place's own: it is not to be closed.
func (p *Place) Dir() *bounded.Dir { return p.dir }

// InPlace reports whether the place's path still leads, the last step
// through no link, to the directory held: what the path holds now and
// what the descriptor holds now are the same directory.
func (p *Place) InPlace() error {
	self, serr := p.dir.Self()
	st, err := os.Lstat(p.real)
	if serr != nil || err != nil || !st.IsDir() {
		return fmt.Errorf("%s %w", p.real, ErrReplaced)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || uint64(sys.Dev) != self.Dev || sys.Ino != self.Ino {
		return fmt.Errorf("%s %w", p.real, ErrReplaced)
	}
	return nil
}

// Sub is the directory name inside the place, taken as a place of its
// own: made if it is not there, opened from the directory held and not
// through a link. What a lane left under the name instead — a link, a
// FIFO, a file — is refused. Taken again, it is the place already
// held, if its name still leads to it.
func (p *Place) Sub(name string, perm os.FileMode) (*Place, error) {
	real := filepath.Join(p.real, name)
	if sub, err := Of(real); !errors.Is(err, ErrNotTaken) {
		return sub, err
	}
	if err := p.dir.Mkdir(name, perm); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	d, err := p.dir.OpenDir(name)
	if err != nil {
		return nil, fmt.Errorf("%s is not a directory: %w", real, err)
	}
	sub := &Place{dir: d, real: real, canonical: filepath.Join(p.canonical, name)}
	for _, n := range p.names {
		sub.names = append(sub.names, filepath.Join(n, name))
	}
	return register(sub), nil
}

// OpenRoot opens the place as an os.Root, for the file tools. A root
// is opened by a path; it is handed back only when it is the directory
// held.
func (p *Place) OpenRoot() (*os.Root, error) {
	root, err := bounded.OpenRoot(p.real)
	if err != nil {
		return nil, err
	}
	d, err := bounded.DirOf(root)
	if err == nil {
		var got, want bounded.FileID
		if got, err = d.Self(); err == nil {
			if want, err = p.dir.Self(); err == nil && got != want {
				err = fmt.Errorf("%s %w", p.real, ErrReplaced)
			}
		}
		_ = d.Close()
	}
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return root, nil
}

// Release gives the place up: it is closed, and its names are free to
// be taken again. The places taken inside it are their own.
func (p *Place) Release() {
	mu.Lock()
	for _, n := range p.names {
		if held[n] == p {
			delete(held, n)
		}
	}
	mu.Unlock()
	_ = p.dir.Close()
}

// Release gives up the place taken under name, if there is one.
func Release(name string) {
	abs, err := filepath.Abs(name)
	if err != nil {
		return
	}
	mu.Lock()
	p := held[abs]
	mu.Unlock()
	if p != nil {
		p.Release()
	}
}

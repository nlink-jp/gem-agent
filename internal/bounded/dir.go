package bounded

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Dir is a directory held open, for a walk that goes down a tree one
// level at a time. What it holds is opened from it by one name, never
// by a path, and by an open that cannot wait: a sub-directory as a
// directory, which a FIFO under its name is refused by, and a file
// without waiting and only if it is a regular one (ADR-0120 §8). A
// name that is a link is not followed.
//
// os.Root has the same one-name opens, but the only way it gives to
// hold a sub-directory, Root.OpenRoot, takes no flags and waits on a
// FIFO. Naming everything by its path from one root needs no handle
// per level and so no OpenRoot, and costs a resolution of the whole
// path at every open: a skill at every bound took 8.2 s that way
// against 0.3 s (measured).
//
// A Dir holds one descriptor, however much of it is listed.
type Dir struct{ f *os.File }

// Entry is one name of a directory, and the kind it had when it was
// looked at.
type Entry struct {
	Name string
	Type fs.FileMode
}

// DirOf opens the directory root holds.
func DirOf(root *os.Root) (*Dir, error) {
	f, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	return &Dir{f}, nil
}

// DirAt opens the directory at path, as a directory, to be held: what
// OpenDir opens, as a Dir. It is for a directory whose names another
// process may replace while they are used — the sandbox checks make
// their probes where the lanes write (ADR-0121).
func DirAt(path string) (*Dir, error) {
	f, err := OpenDir(path)
	if err != nil {
		return nil, err
	}
	return &Dir{f}, nil
}

// Close releases the directory.
func (d *Dir) Close() error { return d.f.Close() }

// FileID tells one file from every other: the device it is on and its
// number there. A name can come to hold another file; a FileID taken
// from what was made says whether it still holds that one.
type FileID struct{ Dev, Ino uint64 }

// IDOf is the identity of an open file.
func IDOf(f *os.File) (FileID, error) {
	rc, err := f.SyscallConn()
	if err != nil {
		return FileID{}, err
	}
	var st unix.Stat_t
	var ferr error
	if err := rc.Control(func(fd uintptr) {
		ferr = again(func() error { return unix.Fstat(int(fd), &st) })
	}); err != nil {
		return FileID{}, err
	}
	if ferr != nil {
		return FileID{}, &os.PathError{Op: "fstat", Path: f.Name(), Err: ferr}
	}
	return FileID{uint64(st.Dev), st.Ino}, nil
}

// Self is the identity of the directory held.
func (d *Dir) Self() (FileID, error) { return IDOf(d.f) }

// Reopen opens the directory held once more, from itself: a second
// descriptor a caller may close, on the same directory whatever its
// name leads to by now.
func (d *Dir) Reopen() (*Dir, error) {
	fd, err := d.openat(".", unix.O_RDONLY|unix.O_DIRECTORY, false)
	if err != nil {
		return nil, err
	}
	return &Dir{os.NewFile(uintptr(fd), d.f.Name())}, nil
}

// Path is the path the kernel holds for the directory (fcntl
// F_GETPATH): links resolved and the case the volume keeps. It is what
// the directory is called now, which need not be the name it was
// opened by.
func (d *Dir) Path() (string, error) {
	var buf [unix.PathMax]byte
	err := d.at(func(dirfd int) error {
		// As sandbox.DescriptorPath asks it.
		if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, uintptr(dirfd), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
			return e
		}
		return nil
	})
	if err != nil {
		return "", &os.PathError{Op: "fcntl", Path: d.f.Name(), Err: err}
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

// Rename gives the name old in the directory the name new there, over
// whatever new held: both are names of this directory, so nothing is
// moved in or out of it.
func (d *Dir) Rename(old, new string) error {
	if !oneName(old) || !oneName(new) {
		return &os.LinkError{Op: "renameat", Old: old, New: new, Err: syscall.EINVAL}
	}
	if err := d.at(func(dirfd int) error { return unix.Renameat(dirfd, old, dirfd, new) }); err != nil {
		return &os.LinkError{Op: "renameat", Old: old, New: new, Err: err}
	}
	return nil
}

// ID is the identity of what is under name, itself and not what it may
// lead to, and its kind.
func (d *Dir) ID(name string) (FileID, fs.FileMode, error) {
	if !oneName(name) {
		return FileID{}, 0, &os.PathError{Op: "fstatat", Path: name, Err: syscall.EINVAL}
	}
	st, err := d.stat(name)
	if err != nil {
		return FileID{}, 0, err
	}
	return FileID{uint64(st.Dev), st.Ino}, kindOf(st.Mode), nil
}

// Mkdir makes the sub-directory name.
func (d *Dir) Mkdir(name string, perm os.FileMode) error {
	if !oneName(name) {
		return &os.PathError{Op: "mkdirat", Path: name, Err: syscall.EINVAL}
	}
	if err := d.at(func(dirfd int) error { return unix.Mkdirat(dirfd, name, uint32(perm.Perm())) }); err != nil {
		return &os.PathError{Op: "mkdirat", Path: name, Err: err}
	}
	return nil
}

// Create makes the file name and opens it for writing. The name must
// not be there: what is — a file, a link, to nowhere as well, a FIFO —
// is never opened, so the open cannot wait and cannot land elsewhere.
func (d *Dir) Create(name string, perm os.FileMode) (*os.File, error) {
	if !oneName(name) {
		return nil, &os.PathError{Op: "openat", Path: name, Err: syscall.EINVAL}
	}
	fd := -1
	err := d.at(func(dirfd int) (err error) {
		fd, err = unix.Openat(dirfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
		return err
	})
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// OpenWrite opens the file name for writing, and only if it is a
// regular file: without waiting, not through a link, and with nothing
// done to it by the open — it is not emptied. A FIFO nobody reads does
// not open that way, and one somebody reads is refused once it is seen.
func (d *Dir) OpenWrite(name string) (*os.File, error) {
	f, err := d.openRegular(name, unix.O_WRONLY)
	if errors.Is(err, unix.ENXIO) {
		return nil, &os.PathError{Op: "open", Path: name, Err: ErrNotRegular}
	}
	return f, err
}

// Remove removes name from the directory, whatever it is but a
// directory: a link goes, and what it led to stays.
func (d *Dir) Remove(name string) error { return d.unlink(name, 0) }

// RemoveDir removes the empty sub-directory name, and nothing that is
// not a directory.
func (d *Dir) RemoveDir(name string) error { return d.unlink(name, unix.AT_REMOVEDIR) }

func (d *Dir) unlink(name string, flags int) error {
	if !oneName(name) {
		return &os.PathError{Op: "unlinkat", Path: name, Err: syscall.EINVAL}
	}
	if err := d.at(func(dirfd int) error { return unix.Unlinkat(dirfd, name, flags) }); err != nil {
		return &os.PathError{Op: "unlinkat", Path: name, Err: err}
	}
	return nil
}

// List lists at most n names from the start of the directory, in
// directory order, each with its kind. There is no "all of them": n
// below 1 lists nothing.
//
// The kind is looked at name by name, from the directory. A name that
// cannot be looked at ends the listing there, with what came before it
// and the error; one that is gone by then is left out and not counted.
// That is how a listing through an os.Root goes, and the skill walk
// listed that way when the pins now on disk were taken: listed by the
// kind the directory itself records, a tree holding a name that cannot
// be looked at — an ACL can deny it — has more entries, and another
// digest.
//
// A directory that can be read and not searched lists nothing: its
// names can be read, and nothing under them can.
func (d *Dir) List(n int) ([]Entry, error) {
	// A descriptor of its own, closed on the way out: a listing keeps
	// state in the descriptor it reads through, and a walk holds a Dir
	// for every level it is in.
	fd, err := d.openat(".", unix.O_RDONLY|unix.O_DIRECTORY, false)
	if err != nil {
		return nil, err
	}
	l := os.NewFile(uintptr(fd), d.f.Name())
	defer func() { _ = l.Close() }()
	var out []Entry
	for len(out) < n {
		names, rerr := l.Readdirnames(n - len(out))
		listed, err := d.kinds(names)
		out = append(out, listed...)
		if err != nil {
			return out, err
		}
		if errors.Is(rerr, io.EOF) || (rerr == nil && len(names) == 0) {
			break
		}
		if rerr != nil {
			return out, rerr
		}
	}
	return out, nil
}

// kinds gives each of names its kind, in their order: one that is gone
// since it was listed is left out, and one that cannot be looked at
// ends it there, with what came before it and the error.
func (d *Dir) kinds(names []string) ([]Entry, error) {
	var out []Entry
	for _, name := range names {
		typ, err := d.kind(name)
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EBADF) {
			continue
		}
		if err != nil {
			return out, err
		}
		out = append(out, Entry{Name: name, Type: typ})
	}
	return out, nil
}

// kind is the kind of name, itself and not what it may lead to.
func (d *Dir) kind(name string) (fs.FileMode, error) {
	st, err := d.stat(name)
	if err != nil {
		return 0, err
	}
	return kindOf(st.Mode), nil
}

// stat looks at name itself, not at what it may lead to.
func (d *Dir) stat(name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	err := d.at(func(dirfd int) error { return unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) })
	if err != nil {
		return st, &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	return st, nil
}

// kindOf is the kind a mode says, as os gives kinds: what it does not
// know reads as a regular file there, and is refused when it is opened.
func kindOf(mode uint16) fs.FileMode {
	switch mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return fs.ModeDir
	case unix.S_IFLNK:
		return fs.ModeSymlink
	case unix.S_IFIFO:
		return fs.ModeNamedPipe
	case unix.S_IFSOCK:
		return fs.ModeSocket
	case unix.S_IFCHR:
		return fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK, unix.S_IFWHT:
		return fs.ModeDevice
	}
	return 0
}

// OpenDir opens the sub-directory name.
func (d *Dir) OpenDir(name string) (*Dir, error) {
	fd, err := d.openat(name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, true)
	if err != nil {
		return nil, err
	}
	return &Dir{os.NewFile(uintptr(fd), name)}, nil
}

// OpenRegular opens the file name for reading, and only if it is a
// regular file: what OpenRegular does under a root, for one name here
// — except that a link is not followed, to a regular file either.
func (d *Dir) OpenRegular(name string) (*os.File, error) {
	return d.openRegular(name, unix.O_RDONLY)
}

// openRegular opens name for reading or for writing, as OpenRegular
// and OpenWrite describe.
func (d *Dir) openRegular(name string, access int) (*os.File, error) {
	fd, err := d.openat(name, access|unix.O_NONBLOCK|unix.O_NOFOLLOW, true)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	err = again(func() error { return unix.Fstat(fd, &st) })
	if err == nil && st.Mode&unix.S_IFMT != unix.S_IFREG {
		err = ErrNotRegular
	}
	if err == nil {
		// Cleared before the file is handed on: what is read from it is
		// read as from any open file.
		err = unix.SetNonblock(fd, false)
	}
	if err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), name), nil
}

// Readlink is the target of the link name.
func (d *Dir) Readlink(name string) (string, error) {
	if !oneName(name) {
		return "", &os.PathError{Op: "readlinkat", Path: name, Err: syscall.EINVAL}
	}
	for size := 128; ; size *= 2 {
		buf := make([]byte, size)
		var n int
		err := d.at(func(dirfd int) (err error) {
			n, err = unix.Readlinkat(dirfd, name, buf)
			return err
		})
		if err != nil {
			return "", &os.PathError{Op: "readlinkat", Path: name, Err: err}
		}
		if n < size {
			return string(buf[:n]), nil
		}
	}
}

// openat opens name from the directory. checked says name came from
// outside: it must then be one name of the directory — an open from a
// directory resolves a path, and `..`, like any other.
func (d *Dir) openat(name string, flags int, checked bool) (int, error) {
	if checked && !oneName(name) {
		return -1, &os.PathError{Op: "openat", Path: name, Err: syscall.EINVAL}
	}
	fd := -1
	err := d.at(func(dirfd int) (err error) {
		fd, err = unix.Openat(dirfd, name, flags|unix.O_CLOEXEC, 0)
		return err
	})
	if err != nil {
		return -1, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return fd, nil
}

// at runs fn on the directory's descriptor, which stays open while it
// runs, and again when a signal interrupted it.
func (d *Dir) at(fn func(dirfd int) error) error {
	rc, err := d.f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(dirfd uintptr) {
		ferr = again(func() error { return fn(int(dirfd)) })
	}); err != nil {
		return err
	}
	return ferr
}

// again runs fn until it is not interrupted by a signal.
func again(fn func() error) error {
	for {
		if err := fn(); err != unix.EINTR {
			return err
		}
	}
}

// oneName reports whether name is one name of a directory: not empty,
// not the directory itself or its parent, and with no separator.
func oneName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\x00")
}

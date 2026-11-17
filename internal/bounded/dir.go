package bounded

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"
	"syscall"

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

// Close releases the directory.
func (d *Dir) Close() error { return d.f.Close() }

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
		if errors.Is(rerr, io.EOF) || (rerr == nil && len(names) == 0) {
			break
		}
		if rerr != nil {
			return out, rerr
		}
	}
	return out, nil
}

// kind is the kind of name, itself and not what it may lead to.
func (d *Dir) kind(name string) (fs.FileMode, error) {
	var st unix.Stat_t
	err := d.at(func(dirfd int) error { return unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) })
	if err != nil {
		return 0, &os.PathError{Op: "fstatat", Path: name, Err: err}
	}
	// The kinds as os gives them: what it does not know reads as a
	// regular file there, and is refused when it is opened.
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		return fs.ModeDir, nil
	case unix.S_IFLNK:
		return fs.ModeSymlink, nil
	case unix.S_IFIFO:
		return fs.ModeNamedPipe, nil
	case unix.S_IFSOCK:
		return fs.ModeSocket, nil
	case unix.S_IFCHR:
		return fs.ModeDevice | fs.ModeCharDevice, nil
	case unix.S_IFBLK, unix.S_IFWHT:
		return fs.ModeDevice, nil
	}
	return 0, nil
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
	fd, err := d.openat(name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW, true)
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

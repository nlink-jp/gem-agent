package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// ReplacedError is a directory the lanes are anchored on that no longer
// resolves to where it did when this process first resolved it.
type ReplacedError struct {
	Dir string // the name
	Was string // the real path it had
}

func (e *ReplacedError) Error() string {
	return fmt.Sprintf("%s no longer leads to %s", e.Dir, e.Was)
}

// keptDirs holds, for a directory's name, the real path it resolved to the
// first time this process asked.
var keptDirs sync.Map

// ResolveKept is ResolveWriteDir for a directory the lanes are anchored
// on: the project, the work directory, the temporary directory, an
// added directory. The first answer a name gives is kept, and every
// later call returns it (ADR-0122 §2).
//
// The lanes are built more than once — at startup, at /clear, at each
// grant — and a lane can replace a directory it writes by a link:
// remove it, and put the link under its name. Resolved again at a later
// build, the name anchored the profile on wherever the link led
// (measured). A name that resolves elsewhere by now comes back as the
// kept answer and a *ReplacedError. A name that is simply gone keeps
// its answer and is no error: a work directory that was removed is made
// again where it was.
//
// Nothing is held open and nothing is compared but the path: a
// directory replaced between this call and what the caller does next
// is a race, and outside what the sandbox is held against.
func ResolveKept(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	now, err := ResolveWriteDir(abs)
	if was, ok := keptDirs.Load(abs); ok {
		switch {
		case err == nil && now == was.(string):
			return now, nil
		case err != nil && gone(abs):
			return was.(string), nil
		}
		return was.(string), &ReplacedError{Dir: abs, Was: was.(string)}
	}
	if err != nil {
		return "", err
	}
	keptDirs.Store(abs, now)
	// The real path is a name too, and the one the file tools and the
	// MCP intake use: asked about later, it must still lead to itself.
	keptDirs.LoadOrStore(now, now)
	return now, nil
}

// gone reports whether nothing is under the name: not a link either.
func gone(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, fs.ErrNotExist)
}

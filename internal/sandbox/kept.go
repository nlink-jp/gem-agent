package sandbox

import (
	"fmt"
	"path/filepath"
	"sync"
)

// keptDirs holds, for a directory's name, the real path it resolved to
// the first time this process asked.
var keptDirs sync.Map

// ResolveKept is ResolveWriteDir for a directory the lanes are anchored
// on — the project, the work directory and what the runtime makes in
// it, the temporary directory. The first answer a name gives is kept,
// and every later call returns it (ADR-0122 §2).
//
// The lanes are built more than once — at startup, at /clear, at each
// grant — and a command in the write lane can replace a directory it
// writes by a link: remove it, and put the link under its name.
// Resolved again at a later build, the name anchored the profile on
// wherever the link led (measured). With the first answer kept, the
// profile stays anchored on the name, and the kernel refuses a write
// that goes through the link.
func ResolveKept(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	if was, ok := keptDirs.Load(abs); ok {
		return was.(string), nil
	}
	real, err := ResolveWriteDir(abs)
	if err != nil {
		return "", err
	}
	keptDirs.Store(abs, real)
	return real, nil
}

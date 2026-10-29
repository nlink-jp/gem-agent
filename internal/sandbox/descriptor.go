package sandbox

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// DescriptorPath returns the path the kernel holds for an open file
// (fcntl F_GETPATH): symlinks resolved, the on-disk case, no firmlink
// prefix (measured 2026-10-29: /Users/x/WORKS -> /Users/x/works,
// /tmp -> /private/tmp, a link to its target). A grant reads the root
// it opened back through this, so a path swapped between the
// judgement and the open cannot redirect it (ADR-0099 §12).
func DescriptorPath(f *os.File) (string, error) {
	var buf [unix.PathMax]byte
	if _, _, e := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), uintptr(unix.F_GETPATH), uintptr(unsafe.Pointer(&buf[0]))); e != 0 {
		return "", fmt.Errorf("F_GETPATH: %w", e)
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n]), nil
}

// CanonicalDir is a directory's real path as the kernel names it:
// resolved like ResolveWriteDir, then opened and read back through
// DescriptorPath, so the case is the volume's, not the typed one.
// The containment checks compare strings, and the default APFS volume
// folds case, so a judgement on the typed case let
// /users/x/library/launchagents pass as not-under ~/Library (review of
// ADR-0099 step 2, measured). Where the directory cannot be opened the
// resolved path is returned with the error.
func CanonicalDir(dir string) (string, error) {
	real, err := ResolveWriteDir(dir)
	if err != nil {
		return "", err
	}
	f, err := os.Open(real)
	if err != nil {
		return real, err
	}
	defer func() { _ = f.Close() }()
	p, err := DescriptorPath(f)
	if err != nil {
		return real, err
	}
	return p, nil
}

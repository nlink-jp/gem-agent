package cmd

import (
	"syscall"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// currentProject is the project directory every command resolves
// (ADR-0101): the working directory as the kernel reports it, then its
// name as the kernel gives it back for an open descriptor
// (CanonicalDir, ADR-0099). os.Getwd would return $PWD, whose spelling
// is whatever the shell was given — the same directory reached in
// another Unicode form was a different project to every record keyed by
// it.
func currentProject() (string, error) {
	cwd, err := syscall.Getwd()
	if err != nil {
		return "", err
	}
	if p, err := sandbox.CanonicalDir(cwd); err == nil {
		return p, nil
	}
	return sandbox.ResolveWriteDir(cwd)
}

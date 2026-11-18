package cmd

import (
	"errors"
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// isReplaced reports that the project or the work directory no longer
// resolves to where it did (ADR-0122 §2). An added directory that was
// replaced is not that: it fails its own check, and is dropped or not
// added like one that is gone.
func isReplaced(err error) bool {
	var added *sandbox.AddedRootError
	if errors.As(err, &added) {
		return false
	}
	var r *sandbox.ReplacedError
	return errors.As(err, &r)
}

// replacedNote is what the operator is told when a rebuild of the lanes
// finds the project or the work directory replaced: which one, what the
// session does now, and what brings the sandbox back: the directory put
// back, and then a restart. /clear does only for a work directory, and
// "a new session" reads as /clear.
func replacedNote(err error) string {
	return fmt.Sprintf("sandbox unverified: %v — every shell command asks you now; put that directory back, then restart gem-agent to have the sandbox again", err)
}

// keptWorkDir is the check before the runtime writes a file of its own
// under the work directory's name, outside the sandbox: a work
// directory a lane replaced by a link is not written through
// (ADR-0122 §2). "" has nothing to check.
func keptWorkDir(dir string) error {
	if dir == "" {
		return nil
	}
	if _, err := sandbox.ResolveKept(dir); isReplaced(err) {
		return fmt.Errorf("the session work directory: %w", err)
	}
	return nil
}

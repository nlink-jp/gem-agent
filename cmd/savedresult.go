package cmd

import (
	"os"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// openSavedResult opens a result saved earlier under a name in the work
// directory, to compare it with one about to be saved there: the file
// itself and no link to one, a regular file, and by an open that cannot
// wait (ADR-0120 §9). It is here and not beside fileSHA256, its one
// caller, because ADRs cite mcpresult.go by line and an import added
// there moves every line they cite.
func openSavedResult(path string) (*os.File, error) {
	return bounded.OpenRegularPathNoFollow(path)
}

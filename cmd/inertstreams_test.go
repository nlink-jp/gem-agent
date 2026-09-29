package cmd

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/spf13/cobra"
)

const hostileText = "answer\x1b]52;c;RVNDUFdO\a\x1b[2J\rtail\n"

// A terminal gets the text inert; a pipe or a file gets every byte, which is
// ADR-0042 §4's contract for the reader it was written for.
func TestInertStreamsOnlyWhereATerminalReads(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outTTY, errTTY bool
		wantOut        string
		wantErr        string
	}{
		{"both terminals", true, true, "answer]52;c;RVNDUFdO[2Jtail\n", "answer]52;c;RVNDUFdO[2Jtail\n"},
		{"stdout piped, stderr a terminal", false, true, hostileText, "answer]52;c;RVNDUFdO[2Jtail\n"},
		{"neither a terminal", false, false, hostileText, hostileText},
	} {
		var out, errOut bytes.Buffer
		c := &cobra.Command{}
		c.SetOut(&out)
		c.SetErr(&errOut)
		inertStreams(c, func(w io.Writer) bool {
			return (w == &out && tc.outTTY) || (w == &errOut && tc.errTTY)
		})
		fmt.Fprint(c.OutOrStdout(), hostileText)
		fmt.Fprint(c.ErrOrStderr(), hostileText)
		if out.String() != tc.wantOut || errOut.String() != tc.wantErr {
			t.Errorf("%s: stdout %q, stderr %q; want %q, %q", tc.name, out.String(), errOut.String(), tc.wantOut, tc.wantErr)
		}
	}
}

// A buffer is not a terminal; the check must not wrap what tests and pipes
// hand the command.
func TestTerminalWriterRefusesANonFile(t *testing.T) {
	if terminalWriter(&bytes.Buffer{}) {
		t.Fatal("a bytes.Buffer was taken for a terminal")
	}
}

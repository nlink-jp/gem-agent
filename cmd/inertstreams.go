package cmd

import (
	"io"
	"os"

	"github.com/nlink-jp/gem-agent/internal/inert"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// inertStreams makes the command's stdout and stderr inert where each one
// is a terminal, and leaves it alone where it is not (ADR-0093 §3). The
// plain REPL and -p write the model's text to stdout and its tool details,
// approval prompts and questions to stderr; a terminal executes some of
// those bytes, a pipe or a file does not. This is `ls -q` / `ls -w`: the
// host's own convention for text it did not write.
//
// ADR-0042 §4's contract — stdout is model text only — keeps its meaning for
// the reader it was written for: a program reading a pipe gets every byte.
//
// Called once, at the top of runREPL, so every later print is covered
// without knowing it. The TUI writes to the terminal through Bubble Tea,
// not through these streams, and makes its own text inert at its ingress.
func inertStreams(cmd *cobra.Command, isTerminal func(io.Writer) bool) {
	if w := cmd.OutOrStdout(); isTerminal(w) {
		cmd.SetOut(inert.Writer(w))
	}
	if w := cmd.ErrOrStderr(); isTerminal(w) {
		cmd.SetErr(inert.Writer(w))
	}
}

// terminalWriter reports whether w is a terminal.
func terminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

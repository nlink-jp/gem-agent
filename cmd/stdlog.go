package cmd

import (
	"io"
	"log"
)

// quietStdLog sends the standard logger nowhere while the TUI owns
// the terminal, and returns the restore. gem-agent itself never logs
// there, but genai prints a failed stream with log.Printf; under the
// TUI that raw line would be drawn over the screen, while the turn's
// own error already says it in the operator's words (ADR-0100 §3).
// The plain REPL and -p keep it on stderr.
func quietStdLog() (restore func()) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	return func() { log.SetOutput(prev) }
}

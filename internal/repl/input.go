// Package repl implements the interactive input reader. The one
// load-bearing behaviour is paste safety: a multi-line paste must become
// ONE input, not one input plus stray lines — a single-line read here
// would fire one LLM call per pasted line (and, on exit, leak leftover
// lines to the parent shell as commands).
package repl

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Reader reads user inputs.
type Reader struct {
	in  *bufio.Reader
	out io.Writer
}

// NewReader creates a Reader. Pass an already-shared *bufio.Reader when
// other components (the approval gate) read the same stream —
// bufio.NewReader returns it unchanged, so both see one buffer and no
// input is stranded in a second buffer.
func NewReader(in io.Reader, out io.Writer) *Reader {
	return &Reader{in: bufio.NewReader(in), out: out}
}

// Read prompts and returns the next input. Lines already buffered behind
// the first one (the multi-line-paste case: the terminal delivers the
// whole paste in one flush) are aggregated into the same input.
// Returns io.EOF when the stream ends with no pending input (Ctrl+D).
func (r *Reader) Read(prompt string) (string, error) {
	fmt.Fprint(r.out, prompt)

	first, err := r.in.ReadString('\n')
	if err != nil && first == "" {
		return "", err
	}

	lines := []string{strings.TrimRight(first, "\r\n")}
	for err == nil && r.in.Buffered() > 0 {
		var next string
		next, err = r.in.ReadString('\n')
		if next != "" || err == nil {
			lines = append(lines, strings.TrimRight(next, "\r\n"))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// Package approve implements the MITL gate — the primary defense layer of
// ADR-0001. Mutating tool calls require per-call human approval; "always"
// registers the tool in a session-scoped allowlist that is never persisted.
package approve

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Decision is the outcome of an approval prompt.
type Decision int

const (
	// Denied — the tool call must not run; the LLM receives a denial result.
	Denied Decision = iota
	// Approved — this single call may run.
	Approved
	// AlwaysThisSession — run now and skip prompts for this tool name for
	// the rest of the session.
	AlwaysThisSession
)

// Gate prompts on out/reads decisions from in.
type Gate struct {
	in     *bufio.Reader
	out    io.Writer
	always map[string]bool
}

// New creates a gate reading decisions from in and prompting on out.
func New(in io.Reader, out io.Writer) *Gate {
	return &Gate{
		in:     bufio.NewReader(in),
		out:    out,
		always: map[string]bool{},
	}
}

// Approve asks the user whether the named tool may run. detail is a short
// human-readable summary of what the call will do (command line, file
// path). EOF or read errors deny — failing closed is the only safe
// default for an approval gate.
func (g *Gate) Approve(toolName, detail string) bool {
	if g.always[toolName] {
		return true
	}
	fmt.Fprintf(g.out, "\n[approval] %s\n  %s\n  allow? [y]es / [n]o / [a]lways this session: ", toolName, detail)
	for {
		line, err := g.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(g.out, "(no input — denied)")
			return false
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "n", "no", "":
			return false
		case "a", "always":
			g.always[toolName] = true
			return true
		default:
			fmt.Fprint(g.out, "  please answer y / n / a: ")
			if err != nil {
				return false
			}
		}
	}
}

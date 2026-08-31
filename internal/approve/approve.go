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

// purposeOrNone renders the model's declaration, or names its absence.
// A blank line where the "why" belongs reads as a rendering bug; the
// operator should be able to tell "it did not say" from "nothing to
// say".
func purposeOrNone(purpose string) string {
	if strings.TrimSpace(purpose) == "" {
		return "(no purpose declared)"
	}
	return purpose
}

// Approve asks the user whether the named tool may run. detail is a short
// human-readable summary of what the call will do (command line, file
// path); purpose is the model's own one-sentence declaration of why it
// wants the call (ADR-0047 — context for the human, never a gate input);
// reason, when non-empty, says why auto-approve escalated instead
// of running it. mustPrompt says the session allowlist may not answer
// this call (Block-tier, or an "always" policy — ADR-0021 §5); answering
// 'a' on such a prompt still registers the allowlist, which future
// non-Block calls use. 'N' denies with a typed reason (ADR-0060),
// read from the next line; an empty reason line is a plain deny.
// EOF or read errors deny — failing closed is the only safe default
// for an approval gate.
func (g *Gate) Approve(toolName, detail, purpose, reason string, mustPrompt bool) (approved, fromAllowlist bool, denyReason string) {
	if !mustPrompt && g.always[toolName] {
		// One keystroke standing in for this call: the learner must
		// not read it as a decision made here (ADR-0048 §1).
		return true, true, ""
	}
	fmt.Fprintf(g.out, "\n[approval] %s\n  %s\n", toolName, detail)
	// Printed even when empty: an undeclared purpose is a fact about the
	// call the operator is being asked to approve (ADR-0047 §4).
	fmt.Fprintf(g.out, "  ↪ %s\n", purposeOrNone(purpose))
	if reason != "" {
		fmt.Fprintf(g.out, "  ⚠ %s\n", reason)
	}
	fmt.Fprint(g.out, "  allow? [y]es / [n]o / [N]o with reason / [a]lways this session: ")
	for {
		line, err := g.in.ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(g.out, "(no input — denied)")
			return false, false, ""
		}
		// 'N' is checked before the lowercase switch: it is the one
		// answer whose case is load-bearing (ADR-0060 §1).
		if strings.TrimSpace(line) == "N" {
			fmt.Fprint(g.out, "  deny reason (empty = deny without one): ")
			reasonLine, rerr := g.in.ReadString('\n')
			if rerr != nil && reasonLine == "" {
				return false, false, "" // EOF mid-question: plain deny
			}
			return false, false, strings.TrimSpace(reasonLine)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true, false, ""
		case "n", "no", "":
			return false, false, ""
		case "a", "always":
			g.always[toolName] = true
			// The keystroke that registers the allowlist is itself an
			// operator decision about this call.
			return true, false, ""
		default:
			fmt.Fprint(g.out, "  please answer y / n / N / a: ")
			if err != nil {
				return false, false, ""
			}
		}
	}
}

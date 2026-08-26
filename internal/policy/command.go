package policy

import (
	"regexp"
	"strings"
)

// CommandKey derives the aggregation-and-matching key for a shell
// command (ADR-0045 §3). The key is a syntactic fact about what runs:
// the first token, extended by the second token only when that token has
// subcommand shape. `go test`, `make build` and `git status` keep two
// tokens; `ls -la` and `touch newfile.txt` reduce to their head.
//
// The second return value is false when no key may be derived. A key
// that names one or two tokens must be the whole truth about what the
// command runs, so anything that can hide a target behind those tokens
// gets no key at all — and a call with no key matches no learned rule
// and takes the ordinary ladder.
//
// One function serves both sides on purpose. The learner aggregates the
// operator's decisions by this key and the gate matches live calls by
// it; two implementations would drift, and a drift here means a rule
// firing on a command the operator never approved.
func CommandKey(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" || !plainCommand(trimmed) {
		return "", false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return "", false
	}
	head := fields[0]
	// A path — absolute, relative, or `./script.sh` — names a file whose
	// contents can change under a key that says nothing about them.
	// Bare names resolve through PATH, which is the operator's own
	// environment.
	if strings.ContainsAny(head, "/=") || !plainHead.MatchString(head) {
		return "", false
	}
	if len(fields) > 1 && subcommand.MatchString(fields[1]) {
		return head + " " + fields[1], true
	}
	return head, true
}

// plainHead bounds the command name itself: letters, digits, and the
// punctuation real command names use.
var plainHead = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._+-]*$`)

// subcommand matches the shape of a subcommand word — not a flag, not a
// path, not a filename. `test`, `build`, `status`, `filter-branch` pass;
// `-la`, `README.md`, `./x` do not.
var subcommand = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// plainCommand reports whether the command is a single, statically
// readable invocation.
//
// Segment separators are excluded because a key derived from the first
// segment would say nothing about the rest of the line. Dynamic
// construction is excluded because the real target is not in the text
// (the agent-skeleton finding). Redirection is excluded because it
// writes somewhere the key does not name.
func plainCommand(command string) bool {
	if strings.ContainsAny(command, "|;&<>\n") {
		return false
	}
	if strings.Contains(command, "$(") || strings.Contains(command, "${") ||
		strings.Contains(command, "`") {
		return false
	}
	// `eval` as a word — an `eval` inside an argument is not the shell
	// builtin, and a filename containing it must not disqualify a key.
	for _, f := range strings.Fields(command) {
		if f == "eval" {
			return false
		}
	}
	return true
}

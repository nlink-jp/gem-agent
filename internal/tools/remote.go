package tools

import "fmt"

// RemoteErrorKind says whose words a failed MCP call's text is
// (ADR-0075 §1): the server's, delivered as a result or as a JSON-RPC
// rejection, or gem-agent's own, when the call could not be completed.
type RemoteErrorKind int

const (
	// RemoteResult: the server answered, and marked the answer an error
	// (isError). The text is the server's.
	RemoteResult RemoteErrorKind = iota + 1
	// RemoteRejected: the server refused the call at the protocol level
	// (a JSON-RPC error object). The text is the server's.
	RemoteRejected
	// RemoteIncomplete: gem-agent could not complete the call — transport,
	// timeout, exit, framing. The text is gem-agent's own cause.
	RemoteIncomplete
)

// String names the kind for the transcript record.
func (k RemoteErrorKind) String() string {
	switch k {
	case RemoteResult:
		return "result"
	case RemoteRejected:
		return "rejected"
	case RemoteIncomplete:
		return "incomplete"
	}
	return "unknown"
}

// ServerSpoke reports whether the text is the server's words.
func (k RemoteErrorKind) ServerSpoke() bool {
	return k == RemoteResult || k == RemoteRejected
}

// RemoteError is a failed MCP call with its provenance (ADR-0075 §1).
// The MCP adapter returns it through Run's error return; the executor
// detects it with errors.As — never by matching text, the ADR-0040 rule
// for RoundLimitError — renders it, and counts consecutive identical
// texts per tool. Server is the name the operator gave the server in
// mcp.json; Tool is the remote tool's own name, which appears only
// inside the rendered text (wrapped as data like any result).
type RemoteError struct {
	Server string
	Tool   string
	Kind   RemoteErrorKind
	Text   string
	// Sent records whether the call's arguments were written to the
	// server. True for every result and rejection (the server answered
	// the call); for an incomplete call it separates a timeout or exit
	// after the request left from a server that could not be started
	// or written to — the note says only what happened.
	Sent bool
}

// Error renders the failure with its provenance; the executor adds the
// `error:` prefix the audit outcome and the attach guards test.
func (e *RemoteError) Error() string {
	switch e.Kind {
	case RemoteResult:
		return fmt.Sprintf("MCP server %q answered %s with an error:\n%s", e.Server, e.Tool, e.Text)
	case RemoteRejected:
		return fmt.Sprintf("MCP server %q rejected the call to %s: %s", e.Server, e.Tool, e.Text)
	default:
		return fmt.Sprintf("gem-agent could not complete %s on MCP server %q: %s", e.Tool, e.Server, e.Text)
	}
}

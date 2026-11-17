package tools

import "context"

// NeedsOperator is what a tool's Run returns when the call cannot go on
// until the operator has answered a question that only the tool's owner
// knows how to put (ADR-0119: a project skill that differs from its
// trusted record). Nothing has been done yet.
//
// The agent calls Resolve OUTSIDE the floor that abandons a slow tool: a
// dialog is not a wedged call, and an abandoned one would leave a
// question on screen that nothing is waiting for. Whatever the tool has
// to read or write stays inside Run, under that floor.
//
// Resolve returns the context to run the tool under once more, or nil
// and the result the model is given instead. The agent resolves at most
// twice for one call; if the tool still reports the condition after
// that, StillUnresolved is the result.
// OperatorResolves bounds how often one call may put its question: the
// question, and once more when what it was about moved while the
// operator was answering.
const OperatorResolves = 2

type NeedsOperator interface {
	error
	Resolve(ctx context.Context) (retry context.Context, result string)
	StillUnresolved() string
}

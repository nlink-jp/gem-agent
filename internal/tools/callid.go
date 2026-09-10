package tools

import "context"

// callIDKey carries the tool call's id into Run (ADR-0083 §3): a tool
// whose effect must be applied by the agent loop rather than by its own
// goroutine — find_tools and mcp_load stage a load request under the
// call id, and the loop commits or discards it with the call's result.
type callIDKey struct{}

// WithCallID returns ctx carrying the tool call's id.
func WithCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callIDKey{}, id)
}

// CallID returns the tool call's id from ctx, or "" when the tool is not
// being run by the agent loop (a test, or a tool calling another).
func CallID(ctx context.Context) string {
	id, _ := ctx.Value(callIDKey{}).(string)
	return id
}

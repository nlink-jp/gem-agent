package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ADR-0075 §1: the three provenances render as three shapes, each
// naming the server and the tool, and the value survives wrapping so the
// executor can read it with errors.As instead of matching text.
func TestRemoteErrorRendersProvenance(t *testing.T) {
	cases := []struct {
		kind RemoteErrorKind
		head string
		name string
	}{
		{RemoteResult, `MCP server "bigquery" answered execute_sql with an error:` + "\nRequired parameter is missing: query", "result"},
		{RemoteRejected, `MCP server "bigquery" rejected the call to execute_sql: Required parameter is missing: query`, "rejected"},
		{RemoteIncomplete, `gem-agent could not complete execute_sql on MCP server "bigquery": Required parameter is missing: query`, "incomplete"},
	}
	for _, c := range cases {
		re := &RemoteError{Server: "bigquery", Tool: "execute_sql", Kind: c.kind, Text: "Required parameter is missing: query"}
		if got := re.Error(); got != c.head {
			t.Errorf("%v rendered %q, want %q", c.kind, got, c.head)
		}
		if c.kind.String() != c.name {
			t.Errorf("%v named %q, want %q", c.kind, c.kind.String(), c.name)
		}
		wrapped := fmt.Errorf("run: %w", re)
		var back *RemoteError
		if !errors.As(wrapped, &back) || back != re {
			t.Errorf("%v not recoverable through a wrap", c.kind)
		}
	}
	if !RemoteResult.ServerSpoke() || !RemoteRejected.ServerSpoke() || RemoteIncomplete.ServerSpoke() {
		t.Error("ServerSpoke: the server's words are the result and the rejection, never the incomplete call")
	}
	for _, k := range []RemoteErrorKind{RemoteResult, RemoteRejected, RemoteIncomplete} {
		if strings.HasPrefix((&RemoteError{Kind: k}).Error(), "error:") {
			t.Errorf("%v: the error: prefix is the executor's, not the value's", k)
		}
	}
}

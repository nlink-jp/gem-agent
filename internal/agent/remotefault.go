package agent

import (
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0075: a remote tool that answers different arguments with one
// error text is reporting its own state, and the loop guard — keyed on
// identical arguments by design — cannot see it. The executor counts
// consecutive identical failure texts per tool within a turn and, at
// the threshold, says so in the function response: the runtime's own
// words, outside the nonce tag, stating what it measured and the action
// to take. No retry, no classification of the text, no hard stop — the
// round ladder stays the ceiling.
const mcpFaultThreshold = 3

// mcpFault is one tool's current streak: the failure text and how many
// consecutive calls returned exactly it.
type mcpFault struct {
	text  string
	count int
}

// remoteFault updates the per-turn fault ledger for one call and returns
// the runtime note to attach, or "". remote is the typed failure the
// executor returned (nil when the call did not fail as a remote call);
// ran says the tool's Run returned on its own — a denied or interrupted
// call is not an outcome of the server and leaves the ledger alone. At
// the threshold, and on every further identical answer, the transcript
// gets an mcp_fault record; the operator is told once per streak.
func (a *Agent) remoteFault(name string, remote *tools.RemoteError, ran bool, round int) string {
	if remote == nil {
		if ran {
			delete(a.mcpFaults, name)
		}
		return ""
	}
	if a.mcpFaults == nil {
		a.mcpFaults = map[string]*mcpFault{}
	}
	f := a.mcpFaults[name]
	if f == nil || f.text != remote.Text {
		f = &mcpFault{text: remote.Text}
		a.mcpFaults[name] = f
	}
	f.count++
	if f.count < mcpFaultThreshold {
		return ""
	}
	a.logRecord("mcp_fault", map[string]any{
		"server": remote.Server, "tool": name, "kind": remote.Kind.String(), "sent": remote.Sent,
		"count": f.count, "round": round, "error": clip(remote.Text, 300),
	})
	if f.count == mcpFaultThreshold {
		a.notify(fmt.Sprintf("MCP server %q: %s has failed %d times in a row with the same error — the model has been told to report it to you",
			remote.Server, name, f.count))
	}
	return remoteFaultNote(name, remote, f.count)
}

// remoteFaultNote is the runtime's note (ADR-0075 §3): the tool by its
// registry name — the identifier the model already holds unwrapped in
// every request, never the server-supplied one — what was measured, and
// the action. It says nothing about whose fault it is: the runtime
// measured that the arguments went out unchanged and that the answer
// repeated, and no more.
func remoteFaultNote(name string, remote *tools.RemoteError, count int) string {
	if remote.Kind.ServerSpoke() {
		return fmt.Sprintf("gem-agent: MCP server %q has answered %s with this same error %d times in a row. gem-agent sent each call's arguments to the server exactly as you wrote them, removing only its own %s field. Tell the user what you asked and what the server answered, and ask how to proceed.",
			remote.Server, name, count, PurposeArg)
	}
	if remote.Sent {
		return fmt.Sprintf("gem-agent: %d calls in a row to %s could not be completed, each failing the same way (the result above says how). gem-agent sent each call's arguments exactly as you wrote them. Tell the user what you asked and what happened, and ask how to proceed.",
			count, name)
	}
	return fmt.Sprintf("gem-agent: %d calls in a row to %s could not be completed — each failed before the call reached the server (the result above says how). Tell the user what you asked and what happened, and ask how to proceed.",
		count, name)
}

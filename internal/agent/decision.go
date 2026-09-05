package agent

import (
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// Decision is the one reading of a tool call that every gate shares
// (ADR-0073 §4): whether the call changes state, and what the rule
// tier says about it. Three places once computed this separately —
// the session-allowlist floor, the auto ladder and the policy gate —
// and each missed a floor the others had (ADR-0072 §1.1, §4.5, §4.9).
// The architecture test pins risk.Classify to this file.
type Decision struct {
	// Tool is the registered tool, nil when the name is unknown.
	Tool *tools.Tool
	// Mutating reports that this call changes state: the tool's own
	// word, which for shell_exec depends on the declared lane.
	Mutating bool
	// Verdict is the rule tier's.
	Verdict risk.Verdict
}

// Floor reports a verdict no policy, allowlist answer or model tier
// may lift: Block, or a Review only the operator may answer.
func (d Decision) Floor() bool {
	return d.Verdict.Tier == risk.Block || d.Verdict.OperatorOnly
}

// decide is the single decision point.
func (a *Agent) decide(tc llm.ToolCall) Decision {
	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		return Decision{Mutating: true, Verdict: risk.Verdict{Tier: risk.Review, Reason: "unknown tool"}}
	}
	mutating := tool.MutatesFor(tc.Args)
	v := risk.Classify(tc.Name, mutating, tc.Args, a.registry.ProjectDir(), a.registry.WorkDir())
	if tc.Name == tools.ShellExecName && !a.registry.Confined() && v.Tier != risk.Block {
		// Unconfined mode (--no-sandbox): the approval buys none of the
		// lane's constraints, so it is not an ordinary write-lane call
		// (ADR-0073 §5) — the operator alone approves, and neither the
		// model tier, a session allowlist nor a policy lifts it.
		v = risk.Verdict{Tier: risk.Review, OperatorOnly: true,
			Reason: "unconfined shell (the sandbox is off): no lane bounds this command — the operator decides, not the model tier"}
	}
	return Decision{Tool: tool, Mutating: mutating, Verdict: v}
}

// laneOf names the lane a shell call runs in for the audit record —
// "read", "write", "operator", prefixed "unconfined:" when no sandbox
// applies it — and "" for any other tool.
func (a *Agent) laneOf(tc llm.ToolCall) string {
	if tc.Name != tools.ShellExecName {
		return ""
	}
	lane := tools.ShellLane(tc.Args).String()
	if !a.registry.Confined() {
		return "unconfined:" + lane
	}
	return lane
}

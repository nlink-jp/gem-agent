package agent

import (
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
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
	// Invalid is set when the call cannot be judged at all — an
	// `access` value that names no lane — and is refused before any
	// gate rather than gated as something it is not (review F7).
	Invalid error
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
	if tc.Name == tools.ShellExecName {
		if access, _ := tc.Args["access"].(string); access != "" {
			if _, err := sandbox.ParseLane(access); err != nil {
				return Decision{Tool: tool, Mutating: true, Invalid: err,
					Verdict: risk.Verdict{Tier: risk.Review, Reason: err.Error()}}
			}
		}
	}
	mutating := tool.MutatesFor(tc.Args)
	args := tc.Args
	if tc.Name == "write_file" || tc.Name == "edit_file" {
		// Judge what the file IS by its real name: a link named
		// `notes.md` pointing at `AGENTS.md` is an AGENTS.md write
		// (final review R2). An unresolvable path keeps its spelling
		// and fails at the open as before.
		if p, ok := tc.Args["path"].(string); ok {
			if real, err := a.registry.RealPath(p); err == nil && real != "" {
				args = make(map[string]any, len(tc.Args))
				for k, v := range tc.Args {
					args[k] = v
				}
				args["path"] = real
			}
		}
	}
	v := risk.Classify(tc.Name, mutating, args, a.registry.ProjectDir(), a.registry.WorkDir())
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

// laneOf names the lane a shell call runs in, for the approval detail
// and the audit records — "read", "write", "operator"; prefixed
// "unconfined:" when no sandbox applies it, "unverified:" when the
// read lane was declared but this machine has no verified read lane
// (the call is then gated like a write-lane call); "invalid" when the
// access value names no lane — and "" for any other tool.
func (a *Agent) laneOf(tc llm.ToolCall) string {
	if tc.Name != tools.ShellExecName {
		return ""
	}
	if access, _ := tc.Args["access"].(string); access != "" {
		if _, err := sandbox.ParseLane(access); err != nil {
			return "invalid"
		}
	}
	lane := tools.ShellLane(tc.Args)
	switch {
	case !a.registry.Confined():
		return "unconfined:" + lane.String()
	case lane == sandbox.LaneRead && !a.registry.ReadLane():
		return "unverified:" + lane.String()
	}
	return lane.String()
}

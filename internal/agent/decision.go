package agent

import (
	"fmt"
	"strings"

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
	// OverCeiling is set when the session's lane ceiling is below the
	// lane this call's effect needs (ADR-0080 §3). It is a refusal, not
	// an escalation: the gate can be answered by the session allowlist,
	// so a ceiling that escalated would be a ceiling an earlier 'a'
	// could spend. CeilingReason is the operator-facing why.
	OverCeiling bool
	// CeilingReason is the model-facing why, in English like every other
	// tool result. CeilingKind is the same fact machine-readable, so the
	// operator-facing prompt can be rendered from the language catalog
	// instead of shipping this sentence to a Japanese screen (ADR-0079).
	CeilingReason string
	CeilingKind   ceilingKind
	// CeilingUnbounded is set when the ceiling is in force and this
	// call's effects are outside what it can bound — an MCP tool, whose
	// server runs outside every Seatbelt profile and whose effects the
	// rule tier cannot read (ADR-0077).
	//
	// What it buys is narrow and deliberate: no standing shortcut
	// answers such a call. A session allowlist and a `never` policy were
	// both written for a session with no ceiling, and the ceiling is the
	// newer, narrower statement — a read-only session ran an allowlisted
	// MCP write with no prompt at all while the banner said it changed
	// nothing (independent review).
	//
	// It does NOT take the call away from the model tier. ADR-0080 §5
	// puts the mode in front of that tier on purpose, and it was
	// measured escalating an MCP write and passing an MCP read; making
	// these operator-only would remove the judgment the ADR chose and
	// stop every lookup in a read-only session. §5 is a judgment, §3 is
	// the kernel denial, and the ADR says not to blur them — the
	// documents that promised "every MCP call asks you" were the ones
	// in the wrong.
	CeilingUnbounded bool
}

// ceilingKind names why a call exceeded the ceiling.
type ceilingKind int

const (
	ceilingWithin ceilingKind = iota
	ceilingShell
	// ceilingState covers every other mutating built-in. It says
	// "changes state outside the session scratch" rather than naming
	// files, because the set is not only the file tools: web_search and
	// web_fetch are mutating for their egress, and telling the operator
	// a search "changes files" is a sentence that is simply untrue
	// (independent review, 2026-09-09). A truthful generic beats a
	// per-tool list nobody keeps in sync.
	ceilingState
	ceilingMemory
)

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
	d := Decision{Tool: tool, Mutating: mutating, Verdict: v}
	ceiling := a.Ceiling()
	if kind, reason := overCeiling(tc.Name, mutating, laneOrDefault(tc), ceiling); kind != ceilingWithin {
		d.OverCeiling, d.CeilingKind, d.CeilingReason = true, kind, reason
	}
	d.CeilingUnbounded = ceiling < sandbox.LaneOperator && strings.HasPrefix(tc.Name, mcpPrefix)
	return d
}

// mcpPrefix names a tool that belongs to an MCP server. One spelling,
// read by the ceiling's exemption and by its must-prompt rule.
const mcpPrefix = "mcp__"

// laneOrDefault is the lane a shell call declared; anything else has no
// declared lane and reads as the read lane, which never exceeds a
// ceiling on its own.
func laneOrDefault(tc llm.ToolCall) sandbox.Lane {
	if tc.Name != tools.ShellExecName {
		return sandbox.LaneRead
	}
	access, _ := tc.Args["access"].(string)
	lane, err := sandbox.ParseLane(access)
	if err != nil {
		return sandbox.LaneRead // already refused as Invalid
	}
	return lane
}

// overCeiling maps a call to the lane its effect needs and compares it
// with the session's ceiling, so one setting bounds every tool instead
// of a list kept per tool (ADR-0080 §3).
//
// An MCP tool is never over the ceiling here. The rule tier cannot read
// another server's effects (ADR-0077), and a ceiling that guessed would
// be guessing about the one place no profile reaches; ADR-0080 §5 states
// the ceiling to the model tier instead, which is a judgment and is
// documented as one.
func overCeiling(name string, mutating bool, declared, ceiling sandbox.Lane) (ceilingKind, string) {
	if ceiling >= sandbox.LaneOperator {
		return ceilingWithin, "" // no ceiling in force
	}
	if name == tools.ShellExecName {
		if declared <= ceiling {
			return ceilingWithin, ""
		}
		return ceilingShell, fmt.Sprintf(
			"this session is capped at the %s lane and the command declared %s", ceiling, declared)
	}
	if strings.HasPrefix(name, mcpPrefix) || !mutating {
		return ceilingWithin, ""
	}
	if memoryWrite(name) {
		return ceilingMemory, fmt.Sprintf(
			"this session is capped at the %s lane, and a memory write changes what every later session trusts", ceiling)
	}
	// No `ceiling >= LaneWrite` case: Ceiling.Lane yields LaneRead or
	// LaneOperator only, and LaneOperator returned above. A write
	// ceiling — expressible, not decided (ADR-0080 §1) — would need one,
	// and that is where to add it.
	return ceilingState, fmt.Sprintf("this session is capped at the %s lane, and this tool changes state outside it", ceiling)
}

// ceilingPrompt renders the refusal for the operator, from the language
// catalog rather than the model-facing sentence.
func (a *Agent) ceilingPrompt(d Decision, tc llm.ToolCall) string {
	ceiling := a.Ceiling()
	switch d.CeilingKind {
	case ceilingShell:
		return fmt.Sprintf(a.msgs.CeilingShellFmt, ceiling, laneOrDefault(tc))
	case ceilingMemory:
		return fmt.Sprintf(a.msgs.CeilingMemoryFmt, ceiling)
	default:
		return fmt.Sprintf(a.msgs.CeilingStateFmt, ceiling)
	}
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

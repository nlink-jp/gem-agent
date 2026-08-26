package agent

import (
	"strings"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// PurposeArg is the reserved argument every approval-gated tool takes:
// the model's one-sentence declaration of why the call is needed
// (ADR-0047). Gemini 3 practically never writes a preamble as a text
// part when it calls a tool — 1 turn in 349, measured — so the
// motivation lived only in the thought stream, which is display-only
// and wiped the moment the round ends in a call. This field is the
// place where intent is written down.
//
// It is gem-agent's field, not the tool's contract: stripped before the
// tool runs, kept in the history (replay fidelity), and never evidence
// for any gate.
const PurposeArg = "purpose"

// purposeDescription is what the model reads. It asks for the goal
// rather than a paraphrase of the arguments, because the arguments are
// already on the approval prompt — a "purpose" that restates them adds
// a line and no information.
const purposeDescription = "Why this call is needed, in ONE sentence, in the language the operator is using. " +
	"The operator sees this on the approval prompt, so state the goal it serves " +
	"(e.g. \"staging the report so the next call can upload it to Slack\"), " +
	"not a restatement of the other arguments."

// CallPurpose returns the purpose declared on a call, or "" when the
// model omitted it. Absence is reported, never punished (ADR-0047 §4).
func CallPurpose(tc llm.ToolCall) string {
	s, _ := tc.Args[PurposeArg].(string)
	return strings.TrimSpace(s)
}

// gatedForPurpose reports whether a tool advertises the purpose
// argument. Scope is the static Mutating flag, not the live per-tool
// policy: the advertised schema must stay byte-identical for the whole
// session or the implicit cache (ADR-0018) re-warms on every policy
// change.
func gatedForPurpose(t *tools.Tool) bool {
	return t.Mutating && !declaresPurpose(t.Parameters)
}

// declaresPurpose reports whether a schema already has an argument of
// that name. An MCP server is free to publish one; shadowing it — and
// then stripping it before the call — would break that tool, so the
// injection stands down instead.
func declaresPurpose(params map[string]any) bool {
	props, ok := params["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, taken := props[PurposeArg]
	return taken
}

// withPurposeParam returns params with the purpose property declared and
// required. The input is never modified: registry schemas are shared,
// and a tool definition rebuilt per turn must stay identical.
//
// Schemas arrive from two worlds — gem-agent's own Go literals and MCP
// servers' decoded JSON — so a required list can be []string or []any.
// A schema that is not a plain object is passed through untouched:
// advertising a required field on a shape this does not understand
// would break the tool for the sake of an annotation.
func withPurposeParam(params map[string]any) map[string]any {
	if t, ok := params["type"].(string); ok && t != "object" {
		return params
	}
	out := make(map[string]any, len(params)+2)
	for k, v := range params {
		out[k] = v
	}
	out["type"] = "object"
	props := make(map[string]any, len(params)+1)
	if existing, ok := out["properties"].(map[string]any); ok {
		for k, v := range existing {
			props[k] = v
		}
	}
	props[PurposeArg] = map[string]any{
		"type":        "string",
		"description": purposeDescription,
	}
	out["properties"] = props
	out["required"] = appendRequired(out["required"], PurposeArg)
	return out
}

func appendRequired(existing any, name string) []string {
	var req []string
	switch v := existing.(type) {
	case []string:
		req = append(req, v...)
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				req = append(req, s)
			}
		}
	}
	for _, r := range req {
		if r == name {
			return req
		}
	}
	return append(req, name)
}

// toolDefs builds the declarations advertised to the model, injecting
// the purpose argument into every approval-gated tool. Built-ins and
// MCP tools go through the same path, so no tool definition repeats the
// boilerplate and a freshly loaded server is covered automatically.
func toolDefs(registry *tools.Registry) ([]llm.ToolDef, map[string]bool) {
	var defs []llm.ToolDef
	injected := map[string]bool{}
	for _, t := range registry.List() {
		params := t.Parameters
		if gatedForPurpose(t) {
			params = withPurposeParam(params)
			injected[t.Name] = true
		}
		defs = append(defs, llm.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}
	return defs, injected
}

// stripPurpose removes the reserved argument from what a tool actually
// receives — and from what the risk evaluator reads. The map itself is
// never modified: it lives in the history, and replay depends on it
// being exactly what the model emitted.
//
// Only calls whose schema gem-agent extended are stripped; an argument
// a server declared itself belongs to that server.
func (a *Agent) stripPurpose(name string, args map[string]any) map[string]any {
	if !a.purposeTools[name] {
		return args
	}
	if _, ok := args[PurposeArg]; !ok {
		return args
	}
	out := make(map[string]any, len(args)-1)
	for k, v := range args {
		if k != PurposeArg {
			out[k] = v
		}
	}
	return out
}

// declaredPurpose returns the purpose to display for a call, empty when
// the tool does not carry gem-agent's field or the model omitted it.
func (a *Agent) declaredPurpose(tc llm.ToolCall) string {
	if !a.purposeTools[tc.Name] {
		return ""
	}
	return CallPurpose(tc)
}

// callSig is the loop-detector signature with the purpose removed: the
// guard compares repeated calls, and a model that re-words its
// justification every round while repeating the identical call would
// otherwise never trip it (ADR-0047 §3).
func (a *Agent) callSig(tc llm.ToolCall) string {
	return canonicalCallSig(llm.ToolCall{Name: tc.Name, Args: a.stripPurpose(tc.Name, tc.Args)})
}

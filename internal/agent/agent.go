// Package agent implements the tool-calling loop: user input → model →
// approval-gated tool execution → function responses → repeat until the
// model answers with text only (or the round cap trips).
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/nlk/guard"
)

// Approver gates mutating tool calls (see internal/approve for the
// interactive implementation). reason is empty for an ordinary prompt
// and carries the escalation cause when auto-approve declined to run
// the call unattended — the operator must be able to see why they are
// being asked.
type Approver interface {
	Approve(toolName, detail, reason string) bool
}

// SessionLog receives session records. May be nil.
type SessionLog interface {
	Log(kind string, data any) error
}

// Agent holds one conversation.
type Agent struct {
	backend  llm.Backend
	registry *tools.Registry
	gate     Approver
	log      SessionLog
	system   string
	maxTurns int

	onToolCall func(tc llm.ToolCall)
	onUsage    func(promptTokens, outputTokens int)
	onAuto     func(tc llm.ToolCall, d AutoDecision)
	onAttach   func(atts []mention.Attachment, problems []mention.Problem)

	mu   sync.Mutex // guards autoApprove (toggled from the UI goroutine)
	auto bool

	history  []llm.Message
	toolDefs []llm.ToolDef
}

// Options configures New.
type Options struct {
	Backend  llm.Backend
	Registry *tools.Registry
	Gate     Approver
	Log      SessionLog // optional
	System   string
	MaxTurns int
	// OnToolCall, when set, observes every tool call before it is gated
	// and executed — the REPL uses it to show activity for read-only
	// calls that never hit the approval prompt (a silent pause reads as
	// a hang).
	OnToolCall func(tc llm.ToolCall)
	// OnUsage, when set, receives per-round token usage (prompt tokens
	// approximate the current context size; output tokens the round's
	// generation) — the TUI footer consumes it.
	OnUsage func(promptTokens, outputTokens int)
	// AutoApprove starts the session in auto-approve mode (ADR-0004).
	AutoApprove bool
	// OnAutoDecision, when set, observes each auto-mode verdict so the
	// UI can show what ran without asking, and why.
	OnAutoDecision func(tc llm.ToolCall, d AutoDecision)
	// OnAttach, when set, reports what an @-reference pulled in (and
	// what it could not) so the operator sees it landed.
	OnAttach func(atts []mention.Attachment, problems []mention.Problem)
}

// New creates an agent.
func New(opts Options) *Agent {
	var defs []llm.ToolDef
	for _, t := range opts.Registry.List() {
		defs = append(defs, llm.ToolDef{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return &Agent{
		backend:    opts.Backend,
		registry:   opts.Registry,
		gate:       opts.Gate,
		log:        opts.Log,
		system:     opts.System,
		maxTurns:   opts.MaxTurns,
		onToolCall: opts.OnToolCall,
		onUsage:    opts.OnUsage,
		onAuto:     opts.OnAutoDecision,
		onAttach:   opts.OnAttach,
		auto:       opts.AutoApprove,
		toolDefs:   defs,
	}
}

// Reset clears the conversation history (REPL /clear).
func (a *Agent) Reset() { a.history = nil }

// AutoApprove reports whether auto-approve mode is on.
func (a *Agent) AutoApprove() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.auto
}

// SetAutoApprove turns auto-approve mode on or off (UI toggle).
func (a *Agent) SetAutoApprove(on bool) {
	a.mu.Lock()
	a.auto = on
	a.mu.Unlock()
}

// AddContext appends an out-of-band note to the conversation history
// without starting a turn — the `!` direct-shell mode uses it so the
// model sees what the user ran and what came back. Must not be called
// while Run is in flight (the UI's phase machine guarantees that).
func (a *Agent) AddContext(text string) {
	a.history = append(a.history, llm.Message{Role: llm.RoleUser, Content: text})
	a.logRecord("user_context", map[string]any{"content": clip(text, 2000)})
}

// HistoryLen reports the number of history messages (REPL status display).
func (a *Agent) HistoryLen() int { return len(a.history) }

// Run executes one user turn to completion. onText receives streamed
// model text as it arrives. Returns the model's final text.
func (a *Agent) Run(ctx context.Context, input string, onText func(string)) (string, error) {
	// @-references become attachments carried beside the text; the text
	// the operator typed is left exactly as written.
	atts, problems := mention.Expand(input, a.registry.ProjectDir(), mention.DefaultLimits())
	if a.onAttach != nil && (len(atts) > 0 || len(problems) > 0) {
		a.onAttach(atts, problems)
	}
	msg := llm.Message{Role: llm.RoleUser, Content: input}
	for _, att := range atts {
		msg.Attachments = append(msg.Attachments, llm.Attachment{
			Ref: att.Ref, Kind: att.Kind, Content: att.Content,
		})
	}
	a.history = append(a.history, msg)
	a.logRecord("user", map[string]any{"content": input, "attachments": attachRefs(atts)})

	for round := 0; round < a.maxTurns; round++ {
		// Fresh nonce tag per LLM call (nlk/guard contract: a previous
		// response may echo the tag name, so reuse across calls is
		// unsafe). The system prompt's {{DATA_TAG}} placeholder and the
		// tool results in the history view are bound to this turn's tag.
		tag := guard.NewTagWithPrefix("tool_output")
		resp, err := a.backend.ChatStream(ctx, tag.Expand(a.system), wrapToolMessages(a.history, tag), a.toolDefs, onText)
		if err != nil {
			return "", err
		}
		if a.onUsage != nil && (resp.PromptTokens > 0 || resp.OutputTokens > 0) {
			a.onUsage(resp.PromptTokens, resp.OutputTokens)
		}

		// A response with neither text nor tool calls carries nothing to
		// replay, and storing it would put an empty part in every later
		// request ("parts[0].data: required oneof field 'data' must have
		// one initialized field" — a 400 that poisons the whole session,
		// observed in the field). Report it instead of recording it.
		if resp.Content == "" && len(resp.ToolCalls) == 0 {
			a.logRecord("assistant_empty", map[string]any{
				"round": round, "finish_reason": resp.FinishReason,
				"block_reason": resp.BlockReason, "thought_tokens": resp.ThoughtTokens,
				"output_tokens": resp.OutputTokens, "prompt_tokens": resp.PromptTokens,
			})
			return "", emptyResponseError(resp)
		}

		// The assistant turn is appended verbatim — including thought
		// signatures — because the next request replays it (Gemini 3
		// hard requirement; see internal/llm).
		a.history = append(a.history, llm.Message{
			Role:            llm.RoleAssistant,
			Content:         resp.Content,
			ToolCalls:       resp.ToolCalls,
			ThoughtPartSigs: resp.ThoughtPartSigs,
			TextPartSig:     resp.TextPartSig,
		})
		a.logRecord("assistant", assistantRecord(resp))

		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		for _, tc := range resp.ToolCalls {
			if a.onToolCall != nil {
				a.onToolCall(tc)
			}
			result := a.execCall(ctx, tc)
			a.history = append(a.history, llm.Message{
				Role:       llm.RoleTool,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Content:    result,
			})
			a.logRecord("tool_result", map[string]any{
				"id": tc.ID, "name": tc.Name, "result": clip(result, 2000),
			})
		}
	}
	return "", fmt.Errorf("reached max turns (%d) without a final answer — /clear to reset, or raise [agent].max_turns", a.maxTurns)
}

// emptyResponseError explains a response that carried nothing, naming
// the cause the API reported. "The model returned nothing" is not
// actionable on its own: a thinking budget spent before any text was
// emitted and a safety block look identical from the outside.
func emptyResponseError(resp *llm.Response) error {
	switch {
	case resp.BlockReason != "":
		// Naming the escape hatch matters: security material (an
		// incident runbook, a phishing analysis) trips the configurable
		// dangerous-content filter routinely, and without this the
		// operator has no way to tell a policy block from a bug.
		return fmt.Errorf("the model provider blocked this request (%s) — the conversation content tripped a content filter; set [model].safety = \"relaxed\" or \"off\" in ~/.config/gem-agent/config.toml if this is legitimate work, or /clear and rephrase",
			resp.BlockReason)
	case resp.FinishReason == "MAX_TOKENS":
		return fmt.Errorf("the model hit its output limit before answering (%d reasoning tokens spent); raise [model].max_output_tokens or ask for something narrower",
			resp.ThoughtTokens)
	case resp.FinishReason == "SAFETY":
		return fmt.Errorf("the model stopped without answering: its response tripped a content filter (SAFETY); set [model].safety = \"relaxed\" or \"off\" if this is legitimate work, or rephrase")
	case resp.FinishReason == "RECITATION":
		return fmt.Errorf("the model stopped without answering (RECITATION: the answer looked like verbatim recitation); rephrase the request")
	case resp.FinishReason != "" && resp.FinishReason != "STOP":
		return fmt.Errorf("the model returned no text (finish reason %s); try rephrasing, or /clear to start a fresh conversation", resp.FinishReason)
	default:
		return fmt.Errorf("the model returned an empty response (no text, no tool calls; finish reason %q); try rephrasing, or /clear to start a fresh conversation",
			resp.FinishReason)
	}
}

func attachRefs(atts []mention.Attachment) []string {
	refs := make([]string, 0, len(atts))
	for _, a := range atts {
		refs = append(refs, a.Ref)
	}
	return refs
}

// wrapToolMessages returns a copy of the history where every tool result
// and every @-attachment is enclosed in this turn's nonce tag. Raw
// content stays in the stored history — wrapping happens at send time
// precisely because the tag must change every call.
//
// Attachments are wrapped for the same reason tool output is: the
// operator chose the file, but not what is inside it.
func wrapToolMessages(history []llm.Message, tag guard.Tag) []llm.Message {
	out := make([]llm.Message, len(history))
	copy(out, history)
	for i := range out {
		if out[i].Role == llm.RoleTool {
			out[i].Content = wrapUntrusted(out[i].Content, tag)
			continue
		}
		if len(out[i].Attachments) == 0 {
			continue
		}
		var b strings.Builder
		b.WriteString(out[i].Content)
		for _, att := range out[i].Attachments {
			fmt.Fprintf(&b, "\n\nAttached %s (%s), quoted as data:\n%s",
				att.Kind, att.Ref, wrapUntrusted(att.Content, tag))
		}
		out[i].Content = b.String()
		out[i].Attachments = nil
	}
	return out
}

func wrapUntrusted(content string, tag guard.Tag) string {
	wrapped, err := tag.Wrap(content)
	if err != nil {
		// Collision with a nonce generated microseconds ago means the
		// content is adversarially echoing tag names. Withhold it
		// rather than ship it unwrapped.
		return "[content withheld: data-tag collision]"
	}
	return wrapped
}

// execCall runs one tool call and always returns a result string for the
// model: denials and failures are results the model must see, never
// silent drops (Gemini pairs every function call with a response).
func (a *Agent) execCall(ctx context.Context, tc llm.ToolCall) string {
	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", tc.Name)
	}
	if tool.Mutating {
		approved, reason := false, ""
		if a.AutoApprove() {
			d := a.decideAuto(ctx, tc)
			if a.onAuto != nil {
				a.onAuto(tc, d)
			}
			a.logRecord("auto_decision", map[string]any{
				"name": tc.Name, "approved": d.Approved,
				"tier": d.Tier.String(), "reason": d.Reason, "model": d.ModelConsulted,
			})
			approved = d.Approved
			if !approved {
				reason = EscalationReason(d)
			}
		}
		if !approved && !a.gate.Approve(tc.Name, CallDetail(tc), reason) {
			return "Tool execution denied by the user. Do not retry the same call; ask the user how to proceed instead."
		}
	}
	out, err := tool.Run(ctx, tc.Args)
	if err != nil {
		return "error: " + err.Error()
	}
	if out == "" {
		return "(no output)"
	}
	return out
}

// CallDetail renders a one-line human-readable summary of a tool call for
// the approval prompt and event display.
func CallDetail(tc llm.ToolCall) string {
	// The interesting argument first, whole-line, for the common tools.
	if cmd, ok := tc.Args["command"].(string); ok && tc.Name == "shell_exec" {
		return clip(cmd, 300)
	}
	keys := make([]string, 0, len(tc.Args))
	for k := range tc.Args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, clip(fmt.Sprintf("%v", tc.Args[k]), 120)))
	}
	if len(parts) == 0 {
		return "(no arguments)"
	}
	return clip(strings.Join(parts, " "), 300)
}

func assistantRecord(resp *llm.Response) map[string]any {
	rec := map[string]any{"content": resp.Content}
	if len(resp.ToolCalls) > 0 {
		var calls []map[string]any
		for _, tc := range resp.ToolCalls {
			calls = append(calls, map[string]any{"id": tc.ID, "name": tc.Name, "args": tc.Args})
		}
		rec["tool_calls"] = calls
	}
	if resp.PromptTokens > 0 || resp.OutputTokens > 0 {
		rec["tokens"] = map[string]int{"prompt": resp.PromptTokens, "output": resp.OutputTokens}
	}
	return rec
}

func (a *Agent) logRecord(kind string, data any) {
	if a.log == nil {
		return
	}
	// A broken session log must not kill a working session.
	_ = a.log.Log(kind, data)
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

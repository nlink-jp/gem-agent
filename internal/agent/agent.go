// Package agent implements the tool-calling loop: user input → model →
// approval-gated tool execution → function responses → repeat until the
// model answers with text only (or the round cap trips).
package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/nlk/guard"
)

// Approver gates mutating tool calls (see internal/approve for the
// interactive implementation).
type Approver interface {
	Approve(toolName, detail string) bool
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
		toolDefs:   defs,
	}
}

// Reset clears the conversation history (REPL /clear).
func (a *Agent) Reset() { a.history = nil }

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
	a.history = append(a.history, llm.Message{Role: llm.RoleUser, Content: input})
	a.logRecord("user", map[string]any{"content": input})

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

// wrapToolMessages returns a copy of the history where every tool result
// is enclosed in this turn's nonce tag. Raw results stay in the stored
// history — wrapping happens at send time precisely because the tag must
// change every call.
func wrapToolMessages(history []llm.Message, tag guard.Tag) []llm.Message {
	out := make([]llm.Message, len(history))
	copy(out, history)
	for i := range out {
		if out[i].Role != llm.RoleTool {
			continue
		}
		wrapped, err := tag.Wrap(out[i].Content)
		if err != nil {
			// Collision with a nonce generated microseconds ago means
			// the content is adversarially echoing tag names. Withhold
			// it rather than ship it unwrapped.
			wrapped = "[tool output withheld: data-tag collision]"
		}
		out[i].Content = wrapped
	}
	return out
}

// execCall runs one tool call and always returns a result string for the
// model: denials and failures are results the model must see, never
// silent drops (Gemini pairs every function call with a response).
func (a *Agent) execCall(ctx context.Context, tc llm.ToolCall) string {
	tool, ok := a.registry.Get(tc.Name)
	if !ok {
		return fmt.Sprintf("error: unknown tool %q", tc.Name)
	}
	if tool.Mutating && !a.gate.Approve(tc.Name, CallDetail(tc)) {
		return "Tool execution denied by the user. Do not retry the same call; ask the user how to proceed instead."
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

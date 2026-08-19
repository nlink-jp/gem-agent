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
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/session"
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
	onNotice   func(msg string)

	mu   sync.Mutex // guards auto and window (both set from the UI goroutine)
	auto bool
	// window is the model's input token limit, 0 while unknown. Set
	// asynchronously: the footer's lookup feeds it (ADR-0006).
	window int

	// Compaction state (ADR-0006). Touched only from the agent goroutine.
	autoCompact     bool
	compactAtPct    int
	lastPrompt      int  // prompt tokens of the most recent round
	compactedAt     int  // history length right after the last compaction
	compactFailures int  // consecutive failures; two disables auto-compaction
	warnedNoCut     bool // "nothing safe to compact" is said once, not per round

	// policy is the operator's per-tool approval policy (ADR-0008). The
	// zero value leaves every tool at the default behaviour.
	policy policy.Policy

	// instructionTools: results of these tools bypass the nonce wrap
	// (ADR-0010). Set at construction, read-only afterwards.
	instructionTools map[string]bool

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
	// OnNotice, when set, receives in-turn notices (a retry after a
	// content-filter block, a compaction) so the operator sees what
	// happened.
	OnNotice func(msg string)
	// Policy is the per-tool approval policy (ADR-0008).
	Policy policy.Policy
	// InstructionTools names tools whose results are instruction-grade
	// rather than untrusted data, exempting them from the nonce wrap
	// (ADR-0010: load_skill, whose reads are confined to operator-
	// installed skill directories). Widen this list only with an ADR —
	// it is a hole in ADR-0001 unless the tool's reads are bounded.
	InstructionTools []string
	// AutoCompact enables automatic history compaction (ADR-0006), and
	// CompactAtPct is the share of the model's input window at which it
	// fires. Compaction still needs a known window; see SetContextWindow.
	AutoCompact  bool
	CompactAtPct int
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
		onNotice:   opts.OnNotice,
		auto:       opts.AutoApprove,
		toolDefs:   defs,

		policy:       opts.Policy,
		autoCompact:  opts.AutoCompact,
		compactAtPct: opts.CompactAtPct,

		instructionTools: toSet(opts.InstructionTools),
	}
}

func toSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// Reset clears the conversation history (REPL /clear).
func (a *Agent) Reset() {
	a.history = nil
	a.compactedAt = 0
	a.lastPrompt = 0
}

// SetHistory replaces the conversation with a restored transcript
// (--continue / --resume, ADR-0005). Like AddContext, it must not be
// called while Run is in flight.
func (a *Agent) SetHistory(history []llm.Message) {
	a.history = history
	// Not len(history): compactedAt exists to stop a compaction from
	// firing twice at the same size, and a restore is not a compaction.
	// A resumed session that is already near the window must be free to
	// compact on its first round.
	a.compactedAt = 0
	a.lastPrompt = 0
}

// SetContextWindow records the model's input token limit, which
// auto-compaction measures against. It arrives asynchronously (the
// lookup that feeds the footer), so it is guarded — 0 means "unknown",
// and auto-compaction stays off until it is known.
func (a *Agent) SetContextWindow(tokens int) {
	a.mu.Lock()
	a.window = tokens
	a.mu.Unlock()
}

func (a *Agent) contextWindow() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.window
}

// SetPolicy replaces the per-tool approval policy (ADR-0008), which the
// settings panel edits mid-session. Guarded because the UI goroutine
// sets it while the agent goroutine reads it per tool call.
func (a *Agent) SetPolicy(p policy.Policy) {
	a.mu.Lock()
	a.policy = p
	a.mu.Unlock()
}

func (a *Agent) toolPolicy(tool string) policy.Decision {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.policy.For(tool)
}

// AutoCompact reports whether automatic compaction is on.
func (a *Agent) AutoCompact() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.autoCompact
}

// SetAutoCompact turns automatic compaction on or off (settings panel).
func (a *Agent) SetAutoCompact(on bool) {
	a.mu.Lock()
	a.autoCompact = on
	a.mu.Unlock()
}

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
	a.appendMessage(llm.Message{Role: llm.RoleUser, Content: text})
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
	a.appendMessage(msg)

	filterRetries := 0

	for round := 0; round < a.maxTurns; round++ {
		// Compaction happens between rounds, before the request that
		// would overflow — a long tool loop is where the window actually
		// runs out (ADR-0006).
		a.maybeAutoCompact(ctx)

		// Fresh nonce tag per LLM call (nlk/guard contract: a previous
		// response may echo the tag name, so reuse across calls is
		// unsafe). The system prompt's {{DATA_TAG}} placeholder and the
		// tool results in the history view are bound to this turn's tag.
		tag := guard.NewTagWithPrefix("tool_output")
		resp, err := a.backend.ChatStream(ctx, tag.Expand(a.system), wrapToolMessages(a.history, tag, a.instructionTools), a.toolDefs, onText)
		if err != nil {
			return "", err
		}
		if resp.PromptTokens > 0 {
			a.lastPrompt = resp.PromptTokens
		}
		if a.onUsage != nil && (resp.PromptTokens > 0 || resp.OutputTokens > 0) {
			a.onUsage(resp.PromptTokens, resp.OutputTokens)
			a.logRecord("usage", map[string]int{
				"prompt": resp.PromptTokens, "output": resp.OutputTokens, "thoughts": resp.ThoughtTokens,
			})
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
			// A content-filter block is not deterministic: the same
			// request, re-sent, usually goes through, because what gets
			// rated is the text this attempt happened to generate.
			// Retrying once beats handing the operator a dead turn — but
			// only once, so a genuinely refused request still surfaces.
			if resp.BlockReason != "" && filterRetries < maxFilterRetries {
				filterRetries++
				a.notify(fmt.Sprintf("content filter blocked the response (%s) — retrying once", resp.BlockReason))
				continue
			}
			return "", emptyResponseError(resp)
		}

		// The assistant turn is appended verbatim — including thought
		// signatures — because the next request replays it (Gemini 3
		// hard requirement; see internal/llm). The transcript records it
		// just as verbatim, for the same reason: a resumed session
		// (ADR-0005) replays these signatures too.
		a.appendMessage(llm.Message{
			Role:            llm.RoleAssistant,
			Content:         resp.Content,
			ToolCalls:       resp.ToolCalls,
			ThoughtPartSigs: resp.ThoughtPartSigs,
			TextPartSig:     resp.TextPartSig,
		})

		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		for _, tc := range resp.ToolCalls {
			if a.onToolCall != nil {
				a.onToolCall(tc)
			}
			result := a.execCall(ctx, tc)
			a.appendMessage(llm.Message{
				Role:       llm.RoleTool,
				ToolName:   tc.Name,
				ToolCallID: tc.ID,
				Content:    result,
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
		// Measured: this fires intermittently on the same request, at
		// every [model].safety setting — the generated text differs per
		// attempt, and PROHIBITED_CONTENT comes from a filter the
		// configurable categories do not cover. So the honest advice is
		// "retry", not "change a setting".
		return fmt.Errorf("the model provider's content filter blocked this exchange (%s). It fires intermittently on the same request, so sending it again often works; narrowing the request, or /clear to drop large documents from the context, helps too. [model].safety adjusts the configurable categories but does not cover this one",
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

// appendMessage adds one message to the conversation and records it in
// the transcript verbatim. Every history append goes through here: a
// message that reaches the model but not the transcript is a hole in a
// resumed session, and the two would drift silently (ADR-0005).
func (a *Agent) appendMessage(m llm.Message) {
	a.history = append(a.history, m)
	a.logRecord(session.KindMessage, m)
}

// wrapToolMessages returns a copy of the history where every tool result
// and every @-attachment is enclosed in this turn's nonce tag. Raw
// content stays in the stored history — wrapping happens at send time
// precisely because the tag must change every call.
//
// Attachments are wrapped for the same reason tool output is: the
// operator chose the file, but not what is inside it.
//
// instructionTools are the exception (ADR-0010): a skill body is an
// instruction file the operator installed, same trust tier as the
// AGENTS.md already injected unwrapped — and wrapping it as data while
// the system prompt forbids following data would leave every skill
// half-inert. The exemption is safe only because that tool's reads are
// confined to discovered skill directories.
func wrapToolMessages(history []llm.Message, tag guard.Tag, instructionTools map[string]bool) []llm.Message {
	out := make([]llm.Message, len(history))
	copy(out, history)
	for i := range out {
		if out[i].Role == llm.RoleTool {
			if instructionTools[out[i].ToolName] {
				continue
			}
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
	if a.gated(tool.Mutating, tc) {
		approved, reason := false, ""
		// A tool the operator marked "always" skips the ladder: the
		// question is settled, and spending a model round on it would
		// both cost a request and risk answering it differently.
		if a.AutoApprove() && a.toolPolicy(tc.Name) != policy.AlwaysAsk {
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

// gated reports whether this call goes through the approval machinery at
// all, applying the operator's per-tool policy (ADR-0008) on top of the
// default "mutating tools ask" rule.
//
// The one subtlety is `never`: it skips the gate, but it does not lift
// the rule tier's Block floor. A tool whose effect varies per call —
// shell_exec above all — must not become "run anything unattended"
// because of one config line, so a Block verdict still asks.
func (a *Agent) gated(mutating bool, tc llm.ToolCall) bool {
	switch a.toolPolicy(tc.Name) {
	case policy.AlwaysAsk:
		return true
	case policy.NeverAsk:
		if !mutating {
			return false
		}
		tool, ok := a.registry.Get(tc.Name)
		if !ok {
			return true
		}
		return risk.Classify(tc.Name, tool.Mutating, tc.Args, a.registry.ProjectDir()).Tier == risk.Block
	default:
		return mutating
	}
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

// maxFilterRetries bounds automatic retries of a content-filter block.
// One is enough for an intermittent filter; more would look like an
// attempt to wear the filter down.
const maxFilterRetries = 1

// notify reports an in-turn event the operator should see (a retry, a
// degraded path) without failing the turn.
func (a *Agent) notify(msg string) {
	a.logRecord("notice", map[string]any{"message": msg})
	if a.onNotice != nil {
		a.onNotice(msg)
	}
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

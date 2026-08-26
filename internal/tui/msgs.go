// Package tui implements the interactive terminal UI (ADR-0002):
// Bubble Tea in inline mode — completed conversation flushes to the
// terminal's native scrollback, only the live region (streaming text,
// status, input box) is managed. The agent core stays UI-agnostic; the
// agent goroutine talks to the UI exclusively through Program.Send.
package tui

import (
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// TextDelta carries one streamed chunk of model text.
type TextDelta string

// AskRequest is the ask_user tool's dialog (ADR-0036): the model's
// question with 2-8 options. Resp receives the chosen index, or -1
// when the operator declines (Esc).
type AskRequest struct {
	Question string
	Options  []string
	Resp     chan int
}

// StreamUpdate is turn observability from the backend (ADR-0033):
// Kind "chunk" (heartbeat), "thought" (a live thought-summary delta),
// or "retry" (a scheduled backoff retry).
type StreamUpdate struct {
	Kind    string
	Thought string
	Attempt int
	Max     int
	Cause   string
	DelayMS int
}

// ToolCall announces a tool invocation (shown as an event line).
// Purpose is the model's declaration of why it wants the call
// (ADR-0047); empty for read-only tools, which are not gated and carry
// no such field.
type ToolCall struct {
	Name    string
	Detail  string
	Purpose string
}

// ToolDone signals that a tool call finished (executed, denied, or
// skipped) — the stall detector re-arms on this, never on stream
// chunks, which a side-call (risk/progress review) also produces.
type ToolDone struct {
	Name string
}

// TurnDone signals the end of an agent turn.
type TurnDone struct {
	Err error
}

// AutoApproved reports a tool call that auto mode let through, with the
// reason — the operator must be able to see what ran unattended.
type AutoApproved struct {
	Tool   string
	Reason string
	Tier   string
}

// AutoMode reports the current auto-approve state for the status line.
type AutoMode bool

// Attached reports what @-references pulled in, and what they could not
// — a silently dropped reference would look like the file was read.
type Attached struct {
	Lines []string
	Notes []string
}

// ShellDone signals completion of a direct (!-prefixed) shell command.
// Interrupted marks a Ctrl+C'd run: a message queued during it is
// handed back rather than auto-sent (ADR-0007's rule, which the shell
// path previously ignored — ADR-0021).
// Diagram carries finished box art from render_diagram (ADR-0043 §2).
// The art is a side effect of the tool call, never something the model
// repeats: it goes to scrollback verbatim, never through the Markdown
// renderer, exactly as shell output does.
type Diagram struct{ Art string }

type ShellDone struct {
	Output      string
	Interrupted bool
}

// Usage carries one LLM round's token counts. Prompt tokens approximate
// the current context size; output tokens are the round's generation.
type Usage struct {
	Prompt int
	Output int
	// Cached is the share of Prompt served from the implicit cache
	// (ADR-0018) — the footer shows it so "is caching firing" is a
	// glance, not an investigation.
	Cached int
}

// ContextWindow reports the model's input token limit once known.
// Assumed marks a family-default guess (Vertex publisher metadata omits
// inputTokenLimit) — the footer renders it with a "~" so an estimate
// never masquerades as a measured value.
type ContextWindow struct {
	Tokens  int
	Assumed bool
}

// Output carries plain lines to the scrollback from work running
// outside the event loop — /riskbook learn's progress and draft, for
// one (ADR-0050). Attached exists for two other things and neither
// fits: its Lines are attachments (📎) and its Notes warnings (⚠);
// a draft rendered as a column of warnings reads as a column of
// problems.
type Output struct{ Lines []string }

// ApprovalRequest asks the operator to approve a mutating tool call.
// The gate goroutine blocks on Resp until the UI answers 'y', 'n' or 'a'.
type ApprovalRequest struct {
	Tool   string
	Detail string
	// Purpose is the model's own one-sentence declaration of why it
	// wants this call (ADR-0047). The arguments say what will run and
	// Reason says why the operator is being asked; without this the
	// third question — why the agent wants it — had no answer anywhere
	// on screen. Empty when the model declared nothing, which is shown
	// as such rather than hidden.
	Purpose string
	// Reason is non-empty when auto-approve escalated this call instead
	// of running it — the operator needs to know why they are being
	// asked, and which tier objected.
	Reason string
	Resp   chan byte
}

// sender is the slice of *tea.Program the gate needs (testable).
type sender interface {
	Send(tea.Msg)
}

// Gate is the TUI-backed approval gate. The session allowlist lives
// here, not in the UI — the UI only ever answers one question.
type Gate struct {
	mu     sync.Mutex
	prog   sender
	always map[string]bool
}

// NewGate creates a gate; SetProgram must be called before the first
// turn runs (the REPL only starts turns from inside the running program,
// so this ordering is structural).
func NewGate() *Gate {
	return &Gate{always: map[string]bool{}}
}

// SetProgram binds the running Bubble Tea program.
func (g *Gate) SetProgram(p sender) {
	g.mu.Lock()
	g.prog = p
	g.mu.Unlock()
}

// Approve implements agent.Approver. Fails closed when no program is
// bound. mustPrompt says the session allowlist may not answer this call
// (Block-tier, or an "always" policy — ADR-0021 §5); an 'a' answered on
// such a prompt still registers, for future non-Block calls.
func (g *Gate) Approve(toolName, detail, purpose, reason string, mustPrompt bool) (approved, fromAllowlist bool) {
	g.mu.Lock()
	if !mustPrompt && g.always[toolName] {
		g.mu.Unlock()
		// One keystroke standing in for this call: the learner must not
		// read it as a decision made here (ADR-0048 §1).
		return true, true
	}
	prog := g.prog
	g.mu.Unlock()
	if prog == nil {
		return false, false
	}
	resp := make(chan byte, 1)
	prog.Send(ApprovalRequest{Tool: toolName, Detail: detail, Purpose: purpose, Reason: reason, Resp: resp})
	switch <-resp {
	case 'y':
		return true, false
	case 'a':
		g.mu.Lock()
		g.always[toolName] = true
		g.mu.Unlock()
		// The keystroke that registers the allowlist is itself an
		// operator decision about this call.
		return true, false
	default:
		return false, false
	}
}

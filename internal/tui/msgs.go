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
type ToolCall struct {
	Name   string
	Detail string
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

// ApprovalRequest asks the operator to approve a mutating tool call.
// The gate goroutine blocks on Resp until the UI answers 'y', 'n' or 'a'.
type ApprovalRequest struct {
	Tool   string
	Detail string
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
func (g *Gate) Approve(toolName, detail, reason string, mustPrompt bool) bool {
	g.mu.Lock()
	if !mustPrompt && g.always[toolName] {
		g.mu.Unlock()
		return true
	}
	prog := g.prog
	g.mu.Unlock()
	if prog == nil {
		return false
	}
	resp := make(chan byte, 1)
	prog.Send(ApprovalRequest{Tool: toolName, Detail: detail, Reason: reason, Resp: resp})
	switch <-resp {
	case 'y':
		return true
	case 'a':
		g.mu.Lock()
		g.always[toolName] = true
		g.mu.Unlock()
		return true
	default:
		return false
	}
}

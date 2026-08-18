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

// ToolCall announces a tool invocation (shown as an event line).
type ToolCall struct {
	Name   string
	Detail string
}

// TurnDone signals the end of an agent turn.
type TurnDone struct {
	Err error
}

// Usage carries one LLM round's token counts. Prompt tokens approximate
// the current context size; output tokens are the round's generation.
type Usage struct {
	Prompt int
	Output int
}

// ContextWindow reports the model's input token limit once known
// (config override or async model-metadata fetch).
type ContextWindow int

// ApprovalRequest asks the operator to approve a mutating tool call.
// The gate goroutine blocks on Resp until the UI answers 'y', 'n' or 'a'.
type ApprovalRequest struct {
	Tool   string
	Detail string
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
// bound.
func (g *Gate) Approve(toolName, detail string) bool {
	g.mu.Lock()
	if g.always[toolName] {
		g.mu.Unlock()
		return true
	}
	prog := g.prog
	g.mu.Unlock()
	if prog == nil {
		return false
	}
	resp := make(chan byte, 1)
	prog.Send(ApprovalRequest{Tool: toolName, Detail: detail, Resp: resp})
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

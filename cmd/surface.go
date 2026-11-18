package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// surface is the one place that decides where operator-facing output
// goes (ADR-0124 §2.4): to the TUI program once it runs, to the stream
// (stderr, the startup tee) until then and in every session that has no
// TUI. What is said stays with the caller; the text each route prints is
// what the thirteen `if prog != nil` branches it replaces printed.
//
// One type whose program field decides, rather than an interface with a
// TUI and a stream implementation: the program is late-bound, so the same
// value has to be the stream before it is the TUI.
type surface struct {
	w        io.Writer
	msgs     *uitext.Messages
	oneShot  bool
	plainAsk askFunc
	// prog is set once, before the program runs; the agent only executes
	// inside prog.Run, so no caller sees it half-set.
	prog *tea.Program
	// hookSink, while set, collects hook notices instead of showing them
	// (collectHookNotices).
	hookSink *[]string
}

func (s *surface) setProgram(p *tea.Program) { s.prog = p }

// notice puts a one-line warning in front of the operator wherever they
// are: the TUI's note strip, stderr otherwise.
func (s *surface) notice(msg string) {
	if s.prog != nil {
		s.prog.Send(tui.Attached{Notes: []string{msg}})
		return
	}
	fmt.Fprintf(s.w, "[⚠ %s]\n", msg)
}

// hookNotice is a hook runner's notice. While a slash command runs the
// hooks (/clear, ADR-0071) the notices are collected instead: the slash
// handler executes inside the TUI's Update, and Program.Send from there
// never returns — Bubble Tea's message channel is unbuffered and Update
// is its only consumer (review round 4: /clear with a session_start hook
// froze the TUI). Notes raised there ride back in the slash output.
func (s *surface) hookNotice(msg string) {
	if s.hookSink != nil {
		*s.hookSink = append(*s.hookSink, msg)
		return
	}
	s.notice(msg)
}

// collectHookNotices appends hook notices to into until stop is called.
// Only hook notices: notice itself is not collected, as it never was.
func (s *surface) collectHookNotices(into *[]string) (stop func()) {
	s.hookSink = into
	return func() { s.hookSink = nil }
}

// toolCall shows a tool call of the main loop. Headless runs (-p, piped
// stdin) get the declared purpose on its own line too (ADR-0047 §5): the
// transcript of a scripted run is the only record it leaves behind.
func (s *surface) toolCall(name, detail, purpose string) {
	if s.prog != nil {
		s.prog.Send(tui.ToolCall{Name: name, Detail: detail, Purpose: purpose})
		return
	}
	fmt.Fprintf(s.w, "\n[tool] %s %s\n", name, detail)
	if purpose != "" {
		fmt.Fprintf(s.w, "[tool] ↪ %s\n", purpose)
	}
}

// childToolCall shows a tool call of the agentic search child, so the
// operator watches the delegation happen instead of a silent pause.
func (s *surface) childToolCall(name, detail string) {
	if s.prog != nil {
		s.prog.Send(tui.ToolCall{Name: "↳ " + name, Detail: detail})
		return
	}
	fmt.Fprintf(s.w, "\n[tool ↳] %s %s\n", name, detail)
}

// toolDone is the tool-finished signal; only the TUI has state to leave.
func (s *surface) toolDone(name string) {
	if s.prog != nil {
		s.prog.Send(tui.ToolDone{Name: name})
	}
}

// childToolDone is toolDone for the search child's calls.
func (s *surface) childToolDone(name string) { s.toolDone("↳ " + name) }

func (s *surface) attached(lines, notes []string) {
	if s.prog != nil {
		s.prog.Send(tui.Attached{Lines: lines, Notes: notes})
		return
	}
	for _, l := range lines {
		fmt.Fprintln(s.w, "[📎 "+l+"]")
	}
	for _, n := range notes {
		fmt.Fprintln(s.w, "[⚠ "+n+"]")
	}
}

func (s *surface) autoApproved(tool, reason, tier string) {
	if s.prog != nil {
		s.prog.Send(tui.AutoApproved{Tool: tool, Reason: reason, Tier: tier})
		return
	}
	fmt.Fprintf(s.w, "[%s]\n", fmt.Sprintf(strings.TrimSpace(s.msgs.AutoApprovedFmt), tier, tool+": "+reason))
}

func (s *surface) output(lines []string) {
	if s.prog != nil {
		s.prog.Send(tui.Output{Lines: lines})
		return
	}
	for _, l := range lines {
		fmt.Fprintln(s.w, l)
	}
}

// usage, stream and contextWindow feed the TUI's footer and status line;
// a session without one shows none of them.
func (s *surface) usage(u llm.Usage) {
	if s.prog != nil {
		s.prog.Send(tui.Usage{Prompt: u.Prompt, Output: u.Output, Cached: u.Cached})
	}
}

func (s *surface) stream(ev llm.StreamEvent) {
	if s.prog != nil {
		s.prog.Send(tui.StreamUpdate{Kind: ev.Kind, Thought: ev.Thought,
			Attempt: ev.Attempt, Max: ev.Max, Cause: ev.Cause, DelayMS: ev.DelayMS})
	}
}

func (s *surface) contextWindow(tokens int, assumed bool) {
	if s.prog != nil {
		s.prog.Send(tui.ContextWindow{Tokens: tokens, Assumed: assumed})
	}
}

// ask is a structured mid-turn choice (ADR-0036): one-shot has nobody to
// ask, the TUI shows a dialog, the plain REPL reads a number from the
// SHARED stdin reader — a second bufio.Reader over the same fd strands
// typed-ahead input in one buffer while the other blocks (AGENTS.md
// gotcha; review round 3).
func (s *surface) ask(ctx context.Context, question string, options []string) (int, error) {
	if s.oneShot {
		return oneShotAsk(ctx, question, options)
	}
	if s.prog != nil {
		resp := make(chan int, 1)
		s.prog.Send(tui.AskRequest{Question: question, Options: options, Resp: resp})
		idx := <-resp
		if idx < 0 {
			return 0, errAskDeclined
		}
		return idx, nil
	}
	return s.plainAsk(ctx, question, options)
}

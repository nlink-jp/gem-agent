package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

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
// value has to be the stream before the TUI runs and again after it ends.
type surface struct {
	w io.Writer
	// term is the terminal itself, for warnings from goroutines the
	// session does not run (backgroundWarning): w is the startup tee,
	// which records lines while the start is still going.
	term     io.Writer
	msgs     *uitext.Messages
	oneShot  bool
	plainAsk askFunc
	// prog is set before the program runs and cleared when it ends:
	// a Send to a program that has ended is dropped without a word
	// (Bubble Tea selects on its cancelled context), so what the exit
	// says — a session_end hook's notice — went nowhere (ADR-0124
	// addendum, Found 5). Atomic: a goroutine the session started, or
	// the telemetry exporter's, may still report as the program ends.
	prog atomic.Pointer[tea.Program]
	// hookSink, while set, collects hook notices instead of showing them
	// (collectHookNotices).
	hookSink *[]string
	// ended is set when the TUI has ended (endTUI). The exit's own
	// speakers — the session_end hooks, the exporter's flush — reach the
	// stream then; what a turn still running reports (one left behind by
	// the third Ctrl+C on a wedged tool, a late window lookup) is
	// dropped, as the ended program dropped it: it would land after the
	// exit summary.
	ended atomic.Bool

	// bg guards the background warnings' bookkeeping. pending holds the
	// ones written to the terminal before a TUI ran, which its first
	// clear wipes: they are replayed as notes when it starts, as the
	// startup warnings ride the banner. unshown holds the ones sent to
	// the program and not yet taken by it: written to the terminal when
	// it ends, since an ending program drops them.
	bg      sync.Mutex
	noTUI   bool
	pending []string
	unshown []string
}

// setProgram routes output to p, and replays the background warnings
// the terminal showed before it.
func (s *surface) setProgram(p *tea.Program) {
	s.bg.Lock()
	defer s.bg.Unlock()
	s.prog.Store(p)
	for _, msg := range s.pending {
		s.sendBackground(p, msg)
	}
	s.pending = nil
}

// withoutTUI says no program will run: nothing is kept for a replay.
func (s *surface) withoutTUI() {
	s.bg.Lock()
	defer s.bg.Unlock()
	s.noTUI, s.pending = true, nil
}

// endTUI is called when the program has returned: a Send to an ended
// program is dropped without a word, so the exit's notices go to the
// stream, the background warnings the program never took are written
// there, and the rest is dropped on purpose (ADR-0124 addendum, Found 5).
func (s *surface) endTUI() {
	s.bg.Lock()
	defer s.bg.Unlock()
	s.ended.Store(true)
	s.prog.Store(nil)
	for _, msg := range s.unshown {
		fmt.Fprintf(s.term, "warning: %s\n", msg)
	}
	s.unshown = nil
}

// backgroundWarning is a warning from a goroutine outside the session's
// own — the telemetry exporter's error handler. It never blocks its
// caller: that handler also runs inside /clear's flush, in the TUI's
// Update, where a Send never returns. Under the TUI it joins the note
// strip; a direct write to the terminal landed on the input line and the
// next redraw covered it (ADR-0124 addendum, Found 6). Before the TUI it
// is written and kept for the replay; after it, written.
func (s *surface) backgroundWarning(msg string) {
	s.bg.Lock()
	defer s.bg.Unlock()
	if p := s.prog.Load(); p != nil {
		s.sendBackground(p, msg)
		return
	}
	fmt.Fprintf(s.term, "warning: %s\n", msg)
	if !s.ended.Load() && !s.noTUI {
		s.pending = append(s.pending, msg)
	}
}

// sendBackground sends msg to p without blocking and remembers it until
// the TUI takes it. Called under s.bg.
func (s *surface) sendBackground(p *tea.Program, msg string) {
	s.unshown = append(s.unshown, msg)
	go p.Send(tui.BackgroundNote{Text: msg, Shown: func() { s.backgroundShown(msg) }})
}

func (s *surface) backgroundShown(msg string) {
	s.bg.Lock()
	defer s.bg.Unlock()
	for i, m := range s.unshown {
		if m == msg {
			s.unshown = append(s.unshown[:i], s.unshown[i+1:]...)
			return
		}
	}
}

// notice puts a one-line warning in front of the operator wherever they
// are: the TUI's note strip, stderr otherwise.
func (s *surface) notice(msg string) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.Attached{Notes: []string{msg}})
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
	if s.ended.Load() {
		// The exit's session_end hooks: the program has ended, the
		// stream is the operator's screen again.
		fmt.Fprintf(s.w, "[⚠ %s]\n", msg)
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
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.ToolCall{Name: name, Detail: detail, Purpose: purpose})
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
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.ToolCall{Name: "↳ " + name, Detail: detail})
		return
	}
	fmt.Fprintf(s.w, "\n[tool ↳] %s %s\n", name, detail)
}

// toolDone is the tool-finished signal; only the TUI has state to leave.
func (s *surface) toolDone(name string) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.ToolDone{Name: name})
	}
}

// childToolDone is toolDone for the search child's calls.
func (s *surface) childToolDone(name string) { s.toolDone("↳ " + name) }

func (s *surface) attached(lines, notes []string) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.Attached{Lines: lines, Notes: notes})
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
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.AutoApproved{Tool: tool, Reason: reason, Tier: tier})
		return
	}
	fmt.Fprintf(s.w, "[%s]\n", fmt.Sprintf(strings.TrimSpace(s.msgs.AutoApprovedFmt), tier, tool+": "+reason))
}

func (s *surface) output(lines []string) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.Output{Lines: lines})
		return
	}
	for _, l := range lines {
		fmt.Fprintln(s.w, l)
	}
}

// usage, stream and contextWindow feed the TUI's footer and status line;
// a session without one shows none of them.
func (s *surface) usage(u llm.Usage) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.Usage{Prompt: u.Prompt, Output: u.Output, Cached: u.Cached})
	}
}

func (s *surface) stream(ev llm.StreamEvent) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.StreamUpdate{Kind: ev.Kind, Thought: ev.Thought,
			Attempt: ev.Attempt, Max: ev.Max, Cause: ev.Cause, DelayMS: ev.DelayMS})
	}
}

func (s *surface) contextWindow(tokens int, assumed bool) {
	if s.ended.Load() {
		return
	}
	if p := s.prog.Load(); p != nil {
		p.Send(tui.ContextWindow{Tokens: tokens, Assumed: assumed})
	}
}

// ask is a structured mid-turn choice (ADR-0036): one-shot has nobody to
// ask, the TUI shows a dialog, the plain REPL reads a number from the
// SHARED stdin reader — a second bufio.Reader over the same fd strands
// typed-ahead input in one buffer while the other blocks (AGENTS.md
// gotcha; review round 3).
func (s *surface) ask(ctx context.Context, question string, options []string) (int, error) {
	if s.ended.Load() {
		return 0, errAskDeclined
	}
	if s.oneShot {
		return oneShotAsk(ctx, question, options)
	}
	if p := s.prog.Load(); p != nil {
		resp := make(chan int, 1)
		p.Send(tui.AskRequest{Question: question, Options: options, Resp: resp})
		idx := <-resp
		if idx < 0 {
			return 0, errAskDeclined
		}
		return idx, nil
	}
	return s.plainAsk(ctx, question, options)
}

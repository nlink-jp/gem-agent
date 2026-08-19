package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func lipglossWidth(s string) int { return ansi.StringWidth(s) }

// capture collects Println output and turn starts.
type capture struct {
	mu      sync.Mutex
	printed []string
	turns   []string
}

func (c *capture) printer(args ...any) tea.Cmd {
	c.mu.Lock()
	for _, a := range args {
		if s, ok := a.(string); ok {
			c.printed = append(c.printed, s)
		}
	}
	c.mu.Unlock()
	return nil
}

func (c *capture) all() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.printed, "\n")
}

func newTestModel(c *capture) Model {
	return New(Options{
		StartTurn: func(ctx context.Context, input string) {
			c.turns = append(c.turns, input)
		},
		Slash:   slashStub,
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return "MD[" + s + "]" }
		},
	})
}

func slashStub(cmd string) (string, bool, bool) {
	switch cmd {
	case "/quit":
		return "bye", false, true
	case "/nope":
		return "unknown command \"/nope\"", true, false
	default:
		return "slash:" + cmd, false, false
	}
}

func press(m Model, key tea.KeyMsg) Model {
	next, _ := m.Update(key)
	return next.(Model)
}

func enter() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyEnter} }

func runeMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestSubmitStartsTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("こんにちは")
	m = press(m, enter())

	if m.phase != phaseRunning {
		t.Fatalf("phase = %v, want running", m.phase)
	}
	if len(c.turns) != 1 || c.turns[0] != "こんにちは" {
		t.Errorf("turns = %v", c.turns)
	}
	if !strings.Contains(c.all(), "こんにちは") {
		t.Error("user line not flushed to scrollback")
	}
	if m.ta.Value() != "" {
		t.Error("input not cleared after submit")
	}
}

// TestMultiLineInputRoutes: three ways to add a newline without
// submitting — Ctrl+J, Alt/Option+Enter, and a trailing backslash.
// (Shift+Enter is not among them: terminals send it as a plain CR.)
func TestMultiLineInputRoutes(t *testing.T) {
	c := &capture{}

	// Ctrl+J
	m := newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	m = press(m, runeMsg("x"))
	if !strings.Contains(m.ta.Value(), "\n") || m.phase != phaseInput {
		t.Errorf("Ctrl+J should insert a newline, got %q phase=%v", m.ta.Value(), m.phase)
	}

	// Alt+Enter
	m = newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if !strings.Contains(m.ta.Value(), "\n") {
		t.Errorf("Alt+Enter should insert a newline, got %q", m.ta.Value())
	}
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("Alt+Enter must not submit")
	}

	// Trailing backslash + Enter
	m = newTestModel(c)
	m.ta.SetValue("first\\")
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.ta.Value() != "first\n" {
		t.Errorf("backslash continuation = %q, want %q", m.ta.Value(), "first\n")
	}
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("backslash continuation must not submit")
	}

	// A plain Enter on a multi-line draft still submits the whole thing.
	m = press(m, runeMsg("second"))
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if len(c.turns) != 1 || !strings.Contains(c.turns[0], "\n") {
		t.Errorf("multi-line submit = %v", c.turns)
	}
}

// TestMentionTabCompletion: Tab completes an @-reference in place, and
// is inert otherwise (it must never drop a tab character into a message).
func TestMentionTabCompletion(t *testing.T) {
	c := &capture{}
	newM := func() Model {
		m := New(Options{
			Printer: c.printer,
			RenderFactory: func(width int) func(string) string {
				return func(s string) string { return s }
			},
			Slash: slashStub,
			CompletePath: func(prefix string) []string {
				all := []string{"README.md", "main.go", "makefile", "src/"}
				var out []string
				for _, p := range all {
					if strings.HasPrefix(p, prefix) {
						out = append(out, p)
					}
				}
				return out
			},
		})
		return m
	}

	// Unique match completes fully.
	m := newM()
	m.ta.SetValue("これ直して @RE")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "これ直して @README.md" {
		t.Errorf("unique completion = %q", m.ta.Value())
	}

	// Ambiguous match advances to the common prefix, then lists.
	m = newM()
	m.ta.SetValue("@ma")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "@ma" {
		t.Errorf("ambiguous completion = %q, want the common prefix", m.ta.Value())
	}
	if !strings.Contains(c.all(), "main.go") || !strings.Contains(c.all(), "makefile") {
		t.Errorf("candidates should be listed when Tab cannot advance: %q", c.all())
	}

	// No @-reference under the cursor: Tab is inert.
	m = newM()
	m.ta.SetValue("普通の文章")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "普通の文章" {
		t.Errorf("Tab must not alter a plain message: %q", m.ta.Value())
	}

	// A finished reference (space after it) is not re-completed.
	m = newM()
	m.ta.SetValue("@README.md を")
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.ta.Value() != "@README.md を" {
		t.Errorf("completed reference should be left alone: %q", m.ta.Value())
	}
}

func TestAttachedNoticeIsVisible(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(Attached{
		Lines: []string{"attached file: README.md (120 bytes)"},
		Notes: []string{"@nope.txt: not found"},
	})
	m = next.(Model)
	out := c.all()
	if !strings.Contains(out, "📎") || !strings.Contains(out, "README.md") {
		t.Errorf("attachment notice missing: %q", out)
	}
	if !strings.Contains(out, "⚠") || !strings.Contains(out, "not found") {
		t.Errorf("failed reference must be reported, not dropped: %q", out)
	}
}

func TestPlaceholderTeachesKeys(t *testing.T) {
	m := newTestModel(&capture{})
	if !strings.Contains(m.ta.Placeholder, "Ctrl+J") {
		t.Errorf("placeholder should teach the newline key: %q", m.ta.Placeholder)
	}
}

func TestSubmitSeparatesTurns(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("hi")
	m = press(m, enter())
	// The user echo is preceded by a blank line so a new turn is
	// visually separated from the previous output.
	found := false
	c.mu.Lock()
	for _, s := range c.printed {
		if strings.HasPrefix(s, "\n") && strings.Contains(s, "hi") {
			found = true
		}
	}
	c.mu.Unlock()
	if !found {
		t.Error("user echo should carry a leading blank line separator")
	}
}

func TestEmptySubmitIgnored(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m = press(m, enter())
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("empty submit must be a no-op")
	}
}

func TestSlashCommandDoesNotStartTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("/tools")
	m = press(m, enter())
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Error("slash command must not start a turn")
	}
	if !strings.Contains(c.all(), "slash:/tools") {
		t.Error("slash output not printed")
	}
}

func TestAutoModeToggleAndIndicator(t *testing.T) {
	c := &capture{}
	state := false
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash:      slashStub,
		ModelName:  "m",
		ToggleAuto: func() bool { state = !state; return state },
	})

	if strings.Contains(m.View(), "auto") {
		t.Error("auto indicator must be absent while off")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !m.autoMode || !state {
		t.Fatal("shift+tab should turn auto mode on")
	}
	if !strings.Contains(m.View(), "auto") {
		t.Error("status line must show the auto indicator while on")
	}
	if !strings.Contains(c.all(), "auto-approve: ON") {
		t.Errorf("toggle should announce the new state: %q", c.all())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.autoMode || state {
		t.Fatal("shift+tab should turn auto mode off again")
	}

	// External state changes (config default, /auto in the plain REPL)
	// arrive as a message.
	next, _ := m.Update(AutoMode(true))
	m = next.(Model)
	if !strings.Contains(m.View(), "auto") {
		t.Error("AutoMode message should update the indicator")
	}
}

// TestAutoModeToggleDuringRun: a long agent loop started in manual mode
// would otherwise demand an approval per step until it finished — the
// toggle has to work mid-run, not only at the prompt.
func TestAutoModeToggleDuringRun(t *testing.T) {
	c := &capture{}
	state := false
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return "MD[" + s + "]" }
		},
		Slash:      slashStub,
		StartTurn:  func(ctx context.Context, input string) {},
		ModelName:  "m",
		ToggleAuto: func() bool { state = !state; return state },
	})
	m.ta.SetValue("long task")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("expected a running turn")
	}

	// Streamed text already arrived; the notice must land after it.
	next, _ := m.Update(TextDelta("partial output"))
	m = next.(Model)

	m = press(m, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !m.autoMode || !state {
		t.Fatal("shift+tab during a run should turn auto mode on")
	}
	if m.phase != phaseRunning {
		t.Error("toggling must not disturb the running turn")
	}
	out := c.all()
	iText := strings.Index(out, "MD[partial output]")
	iNotice := strings.Index(out, "auto-approve: ON")
	if iText == -1 || iNotice == -1 || iText > iNotice {
		t.Errorf("notice should follow the streamed text: %q", out)
	}
	if !strings.Contains(m.View(), "auto") {
		t.Error("status line should show the indicator immediately")
	}

	// Ctrl+C during a run must still interrupt, not toggle.
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.autoMode {
		t.Error("Ctrl+C must not flip auto mode")
	}
}

func TestAutoApprovedEventIsVisible(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(AutoApproved{Tool: "shell_exec", Reason: "local build", Tier: "review"})
	m = next.(Model)
	out := c.all()
	if !strings.Contains(out, "auto-approved") || !strings.Contains(out, "local build") || !strings.Contains(out, "review") {
		t.Errorf("auto-approval must be visible with tier and reason: %q", out)
	}
}

// TestShellMode: "!cmd" runs the shell runner (never the LLM turn),
// shows the command and its output, and returns to input phase.
func TestShellMode(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	var shellCmds []string
	m.shell = func(ctx context.Context, command string) { shellCmds = append(shellCmds, command) }

	m.ta.SetValue("!git status")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("shell mode should enter running phase")
	}
	if len(c.turns) != 0 {
		t.Fatal("! input must not start an LLM turn")
	}
	if len(shellCmds) != 1 || shellCmds[0] != "git status" {
		t.Fatalf("shell runner got %v", shellCmds)
	}
	if !strings.Contains(c.all(), "git status") {
		t.Error("command line not echoed to scrollback")
	}

	next, _ := m.Update(ShellDone{Output: "on branch main\n"})
	m = next.(Model)
	if m.phase != phaseInput {
		t.Error("ShellDone should return to input phase")
	}
	if !strings.Contains(c.all(), "on branch main") {
		t.Error("shell output not emitted")
	}

	// Bare "!" is a no-op.
	m.ta.SetValue("!")
	m = press(m, enter())
	if len(shellCmds) != 1 {
		t.Error("bare ! must not run anything")
	}
}

// TestUnknownSlashCommandStandsOut: an unknown command is an error, and
// errors carry the ✗ marker so they never blend into dim meta text
// (color alone is not relied upon — plain theme keeps the marker).
func TestUnknownSlashCommandStandsOut(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("/nope")
	m = press(m, enter())
	if m.phase != phaseInput {
		t.Fatal("unknown command must not start a turn")
	}
	if !strings.Contains(c.all(), "✗") || !strings.Contains(c.all(), "/nope") {
		t.Errorf("error output should carry the ✗ marker: %q", c.all())
	}
}

// TestHistoryDownGuard is the regression test for the recorded org
// lesson: Down outside history navigation must not touch the draft.
func TestHistoryDownGuard(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"first", "second"}
	m.ta.SetValue("typing in progress")

	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "typing in progress" {
		t.Fatalf("Down with histIdx=-1 destroyed the draft: %q", m.ta.Value())
	}
}

func TestHistoryNavigation(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"first", "second"}
	m.ta.SetValue("draft")

	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "second" {
		t.Fatalf("Up should recall the latest entry, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "first" {
		t.Fatalf("second Up should go older, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	if m.ta.Value() != "first" {
		t.Fatalf("Up at the oldest entry should stay, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "second" {
		t.Fatalf("Down should go newer, got %q", m.ta.Value())
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	if m.ta.Value() != "draft" {
		t.Fatalf("Down past the newest entry should restore the draft, got %q", m.ta.Value())
	}
	if m.histIdx != -1 {
		t.Error("navigation state should be cleared after draft restore")
	}
}

func TestEditLeavesNavigationMode(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.history = []string{"recalled"}
	m = press(m, tea.KeyMsg{Type: tea.KeyUp})
	m = press(m, runeMsg("x"))
	if m.histIdx != -1 {
		t.Error("editing a recalled entry should leave navigation mode")
	}
}

// TestPasteDoesNotSubmit: pasted newlines insert into the textarea; only
// a human Enter submits.
func TestPasteDoesNotSubmit(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line1\nline2"), Paste: true}
	m = press(m, paste)
	if m.phase != phaseInput || len(c.turns) != 0 {
		t.Fatal("paste must not start a turn")
	}
	if !strings.Contains(m.ta.Value(), "line1") || !strings.Contains(m.ta.Value(), "line2") {
		t.Errorf("pasted content lost: %q", m.ta.Value())
	}
}

func TestStreamFlushOrder(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())

	next, _ := m.Update(TextDelta("before tool"))
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "read_file", Detail: "path=a"})
	m = next.(Model)
	next, _ = m.Update(TextDelta("after tool"))
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)

	out := c.all()
	iText := strings.Index(out, "MD[before tool]")
	iTool := strings.Index(out, "read_file")
	iAfter := strings.Index(out, "MD[after tool]")
	if iText == -1 || iTool == -1 || iAfter == -1 {
		t.Fatalf("missing segments in scrollback: %q", out)
	}
	if !(iText < iTool && iTool < iAfter) {
		t.Errorf("scrollback order broken: %q", out)
	}
	if m.phase != phaseInput {
		t.Error("TurnDone should return to input phase")
	}
	if m.live.Len() != 0 {
		t.Error("live buffer should be empty after TurnDone")
	}
}

// TestConsecutiveTextDeltas reproduces the live crash: Bubble Tea copies
// the model by value on every Update, and a strings.Builder held by
// value panics on the second WriteString after a copy. Two consecutive
// deltas with reassignment in between mimic the real event loop.
func TestConsecutiveTextDeltas(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())

	for _, chunk := range []string{"こん", "にち", "は！"} {
		next, _ := m.Update(TextDelta(chunk))
		m = next.(Model)
	}
	next, _ := m.Update(TurnDone{})
	m = next.(Model)

	if !strings.Contains(c.all(), "MD[こんにちは！]") {
		t.Errorf("accumulated stream lost: %q", c.all())
	}
}

func TestFooterShowsModelUsageAndProject(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash:      slashStub,
		ModelName:  "gemini-3.7-flash",
		ProjectDir: "~/works/demo",
	})

	// Before any usage: placeholders, never garbage.
	v := m.View()
	if !strings.Contains(v, "gemini-3.7-flash") || !strings.Contains(v, "~/works/demo") {
		t.Fatalf("footer missing static fields: %q", v)
	}
	if !strings.Contains(v, "ctx –/–") {
		t.Errorf("unknown usage/window should show placeholders: %q", v)
	}

	// Usage accumulates: ctx tracks the last round, total accumulates.
	next, _ := m.Update(Usage{Prompt: 1000, Output: 200})
	m = next.(Model)
	next, _ = m.Update(Usage{Prompt: 11800, Output: 500})
	m = next.(Model)
	next, _ = m.Update(ContextWindow{Tokens: 1_048_576})
	m = next.(Model)

	v = m.View()
	if !strings.Contains(v, "ctx 12.3k/1.0M (1%)") {
		t.Errorf("footer occupancy wrong: %q", v)
	}
	if !strings.Contains(v, "total 13.5k") {
		t.Errorf("footer total wrong: %q", v)
	}

	// A family-default guess renders with "~" — an estimate must never
	// masquerade as a measured value.
	next, _ = m.Update(ContextWindow{Tokens: 1_048_576, Assumed: true})
	m = next.(Model)
	if !strings.Contains(m.View(), "ctx 12.3k/~1.0M") {
		t.Errorf("assumed window should carry ~: %q", m.View())
	}
}

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{999: "999", 1000: "1.0k", 12345: "12.3k", 1_048_576: "1.0M"}
	for n, want := range cases {
		if got := humanTokens(n); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestApprovalFlow(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("do it")
	m = press(m, enter())

	resp := make(chan byte, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "shell_exec", Detail: "make build", Resp: resp})
	m = next.(Model)
	m = dialogSeen(m)
	if m.phase != phaseApproval {
		t.Fatal("approval request should switch phase")
	}
	if !strings.Contains(m.View(), "shell_exec") {
		t.Error("approval view should show the tool")
	}

	if strings.Contains(m.View(), "⚠") {
		t.Error("an ordinary prompt has no escalation reason to show")
	}

	m = press(m, runeMsg("a"))
	select {
	case got := <-resp:
		if got != 'a' {
			t.Errorf("answer = %c", got)
		}
	case <-time.After(time.Second):
		t.Fatal("gate never received the answer")
	}
	if m.phase != phaseRunning {
		t.Error("answer should return to running phase")
	}
}

// TestViewLinesClippedToWidth pins the resize-artifact fix: no line of
// the managed view may exceed the terminal width, or soft wrapping
// desyncs the inline renderer's height accounting and stale frames
// stack up on screen.
func TestViewLinesClippedToWidth(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 24, Height: 40})
	m = next.(Model)

	// Streaming phase with long lines is the worst case.
	m.ta.SetValue("go")
	m = press(m, enter())
	next, _ = m.Update(TextDelta(strings.Repeat("長い行です。", 40) + "\n" + strings.Repeat("x", 500)))
	m = next.(Model)

	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipglossWidth(line); w >= 24 {
			t.Errorf("view line %d is %d cells wide (must stay under 24)", i, w)
		}
	}
	// Input phase (hint line) must clip too.
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	for i, line := range strings.Split(m.View(), "\n") {
		if w := lipglossWidth(line); w >= 24 {
			t.Errorf("input view line %d is %d cells wide", i, w)
		}
	}
}

// TestShrinkClearsScreenOnce: the first size report performs the ADR-0003
// startup clear (banner follows it inside the same sequence, so nothing
// is lost); growth must not clear; a genuine width shrink clears to sweep
// re-wrapped stale frames and resets the line counter.
func TestShrinkClearsScreenOnce(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)

	next, cmd := m.Update(tea.WindowSizeMsg{Width: 60, Height: 40})
	m = next.(Model)
	if cmd == nil {
		t.Error("first size report should run the startup clear sequence")
	}
	next, cmd = m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = next.(Model)
	if cmd != nil {
		t.Error("growth must not clear the screen")
	}
	m.printed = 7
	next, cmd = m.Update(tea.WindowSizeMsg{Width: 50, Height: 40})
	m = next.(Model)
	if cmd == nil {
		t.Error("shrink must trigger a screen clear")
	}
	if m.printed != 0 {
		t.Errorf("shrink clear must reset the line counter, got %d", m.printed)
	}
}

// TestBottomPinning pins ADR-0003: the view pads from the top so the
// input block sits at the window bottom, the banner prints through the
// line counter after the startup clear, and the padding floors at zero
// once the conversation fills the screen.
func TestBottomPinning(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash:  slashStub,
		Banner: []string{"banner line 1", "banner line 2"},
	})

	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = next.(Model)
	if m.printed != 2 {
		t.Fatalf("banner should be counted: printed = %d", m.printed)
	}
	if !strings.Contains(c.all(), "banner line 1") {
		t.Fatal("banner not emitted through the TUI")
	}

	v := m.View()
	core := strings.Count(clipLines(m.viewContent(), m.width), "\n") + 1
	wantPad := 24 - 2 - core - 1
	gotPad := 0
	for _, r := range v {
		if r != '\n' {
			break
		}
		gotPad++
	}
	if gotPad != wantPad {
		t.Errorf("top padding = %d, want %d", gotPad, wantPad)
	}

	// A wide line counts its wrapped physical lines.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 20, Height: 24}) // no shrink reset on first... width shrinks: clear resets
	m = next.(Model)
	if m.printed != 0 {
		t.Fatalf("shrink clear should reset the counter: %d", m.printed)
	}
	before := m.printed
	cmd := m.emit(strings.Repeat("x", 45)) // 45 cells / 20 wide = 3 physical lines
	_ = cmd
	if m.printed-before != 3 {
		t.Errorf("wrapped emit counted %d physical lines, want 3", m.printed-before)
	}

	// Fill beyond the screen: padding floors at zero.
	m.printed = 1000
	v = m.View()
	if strings.HasPrefix(v, "\n\n") {
		t.Error("padding must floor at zero when the screen is full")
	}
}

// TestZeroSizedTerminalStaysUsable: a terminal that reports no size
// (some pty harnesses; any failed ioctl) must not leave the input box
// with a negative width, where nothing the operator types is drawn.
func TestZeroSizedTerminalStaysUsable(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m = next.(Model)

	if m.width < minWidth || m.height < minHeight {
		t.Fatalf("size floors not applied: %dx%d", m.width, m.height)
	}
	m = press(m, runeMsg("a"))
	m = press(m, runeMsg("b"))
	if m.ta.Value() != "ab" {
		t.Fatalf("typing lost at zero size: %q", m.ta.Value())
	}
	if !strings.Contains(m.View(), "ab") {
		t.Errorf("typed text not rendered at zero size: %q", m.View())
	}
}

// TestResizeNeverQueriesTerminal pins the OSC-leak fix: resizing must
// rebuild the renderer through the injected factory only — a renderer
// that queries the terminal mid-session turns the reply into phantom
// keystrokes in the input box.
func TestResizeNeverQueriesTerminal(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	if got := m.render("x"); got != "MD[x]" {
		t.Fatalf("resize replaced the injected renderer factory: %q", got)
	}
}

func approvalReq(reason string) (ApprovalRequest, chan byte) {
	resp := make(chan byte, 1)
	return ApprovalRequest{Tool: "shell_exec", Detail: "make build", Reason: reason, Resp: resp}, resp
}

func openApproval(t *testing.T, m Model, reason string) (Model, chan byte) {
	t.Helper()
	req, resp := approvalReq(reason)
	next, _ := m.Update(req)
	return dialogSeen(next.(Model)), resp
}

// dialogSeen rewinds the type-ahead grace window (ADR-0021): these
// tests press keys as an operator who has read the dialog, not as a
// keystroke that was already in flight when it appeared.
func dialogSeen(m Model) Model {
	m.approvalAt = m.approvalAt.Add(-2 * approvalGrace)
	return m
}

// TestApprovalSelectionKeys is the IME-safety contract: y/n/a are
// swallowed by a Japanese IME's composition, so the dialog must also be
// operable with arrows/Tab + Enter, which reach the app untouched.
func TestApprovalSelectionKeys(t *testing.T) {
	c := &capture{}
	m, resp := openApproval(t, newTestModel(c), "")

	if m.choice != choiceAllow {
		t.Fatalf("ordinary prompt should start on 許可, got %d", m.choice)
	}
	// Right/Tab move forward, Left/Up back, and it wraps.
	m = press(m, tea.KeyMsg{Type: tea.KeyRight})
	if m.choice != choiceDeny {
		t.Errorf("Right should move to 拒否, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.choice != 2 {
		t.Errorf("Tab should move to 常に許可, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.choice != 3 {
		t.Errorf("Tab should move to 今後聞かない, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyTab})
	if m.choice != choiceAllow {
		t.Errorf("Tab should wrap to 許可, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.choice != len(approvalOptions)-1 {
		t.Errorf("Left should wrap backwards to the last option, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.choice != 2 {
		t.Errorf("Left should step back to 常に許可, got %d", m.choice)
	}

	// Enter confirms the highlighted option — no letter typed.
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got != 'a' {
			t.Errorf("Enter confirmed %c, want a", got)
		}
	case <-time.After(time.Second):
		t.Fatal("Enter did not answer the gate")
	}
	if m.phase != phaseRunning {
		t.Error("answering should leave the approval phase")
	}
}

// TestApprovalEscalationDefaultsToDeny: a reflexive Enter must not
// approve what the risk ladder objected to.
func TestApprovalEscalationDefaultsToDeny(t *testing.T) {
	c := &capture{}
	m, resp := openApproval(t, newTestModel(c), "auto-approve blocked by rule (always asks): recursive force delete")
	if m.choice != choiceDeny {
		t.Fatalf("escalated prompt should start on 拒否, got %d", m.choice)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-resp; got != 'n' {
		t.Errorf("Enter on an escalation answered %c, want n", got)
	}
}

func TestApprovalLetterShortcutsStillWork(t *testing.T) {
	c := &capture{}
	for _, tc := range []struct {
		key  string
		want byte
	}{{"y", 'y'}, {"n", 'n'}, {"a", 'a'}} {
		m, resp := openApproval(t, newTestModel(c), "")
		m = press(m, runeMsg(tc.key))
		select {
		case got := <-resp:
			if got != tc.want {
				t.Errorf("%q answered %c, want %c", tc.key, got, tc.want)
			}
		case <-time.After(time.Second):
			t.Errorf("%q did not answer the gate", tc.key)
		}
		_ = m
	}
}

func TestApprovalEscDenies(t *testing.T) {
	c := &capture{}
	m, resp := openApproval(t, newTestModel(c), "")
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := <-resp; got != 'n' {
		t.Errorf("Esc answered %c, want n", got)
	}
	_ = m
}

// TestApprovalSelectionVisible: the highlighted option carries a marker
// as well as styling, so it is identifiable under theme = plain too.
func TestApprovalSelectionVisible(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	m, _ = openApproval(t, m, "")

	v := m.View()
	if !strings.Contains(v, "▶") {
		t.Error("selection marker missing")
	}
	for _, opt := range approvalOptions {
		if !strings.Contains(v, opt.label) {
			t.Errorf("option %q not rendered", opt.label)
		}
	}
	if !strings.Contains(v, "Enter") {
		t.Error("dialog should teach the Enter/selection route")
	}
}

// TestApprovalShowsEscalationReason: in auto mode the operator's first
// question is "why is this asking at all?" — the reason gets its own
// marked line in the dialog, not a dim suffix on the arguments.
func TestApprovalShowsEscalationReason(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())

	next, _ = m.Update(ApprovalRequest{
		Tool:   "shell_exec",
		Detail: "rm -rf build",
		Reason: "auto-approve blocked by rule (always asks): recursive force delete",
		Resp:   make(chan byte, 1),
	})
	m = next.(Model)

	v := m.View()
	if !strings.Contains(v, "⚠") {
		t.Fatalf("escalation reason missing its marker: %q", v)
	}
	if !strings.Contains(v, "blocked by rule") || !strings.Contains(v, "recursive force delete") {
		t.Errorf("dialog should name the tier and the cause: %q", v)
	}
}

func TestCtrlCClearsThenQuits(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("half-typed")
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if m.ta.Value() != "" {
		t.Fatal("first Ctrl+C should clear the input")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("Ctrl+C on empty input should quit")
	}
}

func TestCtrlCInterruptsTurn(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	canceled := make(chan struct{})
	m.startTurn = func(ctx context.Context, input string) {
		go func() {
			<-ctx.Done()
			close(canceled)
		}()
	}
	m.ta.SetValue("long task")
	m = press(m, enter())
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Error("Ctrl+C during a turn should cancel the turn context")
	}
}

// /compact makes an LLM call, so it must run like a turn — the slash
// handler is synchronous and would freeze the UI mid-call.
func TestCompactRunsAsATurnNotAsASlashCommand(t *testing.T) {
	c := &capture{}
	var started int
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) { c.turns = append(c.turns, input) },
		Compact:   func(ctx context.Context) { started++ },
		Slash:     slashStub,
		Printer:   c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	m.ta.SetValue("/compact")
	m = press(m, enter())

	if started != 1 {
		t.Fatalf("compact starter called %d times", started)
	}
	if m.phase != phaseRunning {
		t.Errorf("phase = %v, want phaseRunning while the summariser works", m.phase)
	}
	if m.cancelTurn == nil {
		t.Error("a running compaction must be interruptible (Ctrl+C)")
	}
	if strings.Contains(c.all(), "slash:/compact") {
		t.Error("/compact went through the synchronous slash handler")
	}
	// And it ends like a turn.
	next, _ := m.Update(TurnDone{})
	if next.(Model).phase != phaseInput {
		t.Error("TurnDone did not return the UI to the prompt")
	}
}

// With no compaction wired (one-shot, tests), /compact must still fall
// through to the slash handler rather than silently doing nothing.
func TestCompactFallsBackToTheSlashHandler(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("/compact")
	m = press(m, enter())
	if !strings.Contains(c.all(), "slash:/compact") {
		t.Errorf("printed = %q", c.all())
	}
}

// ADR-0007: typing during a turn used to be dropped on the floor, with
// no characters appearing — the operator retyped the message.
func TestTypingDuringATurnIsKeptAndQueued(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatalf("phase = %v", m.phase)
	}

	// Typing mid-run reaches the box.
	m = press(m, runeMsg("no, the other file"))
	if m.ta.Value() != "no, the other file" {
		t.Fatalf("mid-run typing was dropped: %q", m.ta.Value())
	}

	// Enter queues rather than sending: the agent loop owns the
	// conversation until it returns.
	m = press(m, enter())
	if m.pending != "no, the other file" {
		t.Fatalf("pending = %q", m.pending)
	}
	if m.ta.Value() != "" {
		t.Errorf("input box should be cleared after queueing, got %q", m.ta.Value())
	}
	if len(c.turns) != 1 {
		t.Fatalf("a queued message was sent mid-turn: turns = %v", c.turns)
	}
	if !strings.Contains(c.all(), "queued") {
		t.Errorf("queueing was silent:\n%s", c.all())
	}

	// A clean finish sends it as the next turn.
	next, _ := m.Update(TurnDone{})
	m = next.(Model)
	if len(c.turns) != 2 || c.turns[1] != "no, the other file" {
		t.Fatalf("queued message not sent after a clean turn: %v", c.turns)
	}
	if m.pending != "" {
		t.Error("pending was not cleared")
	}
}

// A message written during a turn that then failed was written against a
// world that no longer exists. Hand it back rather than firing it.
func TestQueuedMessageIsHandedBackWhenTheTurnFails(t *testing.T) {
	for name, err := range map[string]error{
		"error":       errors.New("backend exploded"),
		"interrupted": context.Canceled,
	} {
		t.Run(name, func(t *testing.T) {
			c := &capture{}
			m := newTestModel(c)
			m.ta.SetValue("first")
			m = press(m, enter())
			m = press(m, runeMsg("follow-up"))
			m = press(m, enter())

			next, _ := m.Update(TurnDone{Err: err})
			m = next.(Model)
			if len(c.turns) != 1 {
				t.Fatalf("queued message was sent into a failed turn: %v", c.turns)
			}
			if m.ta.Value() != "follow-up" {
				t.Errorf("queued message not returned to the input box: %q", m.ta.Value())
			}
			if m.pending != "" {
				t.Error("pending should be cleared once handed back")
			}
			if !strings.Contains(c.all(), "not sent") {
				t.Errorf("the operator was not told:\n%s", c.all())
			}
		})
	}
}

// Nothing typed is dropped: a second Enter appends to what is pending.
func TestSecondQueuedMessageAppends(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, enter())
	m = press(m, runeMsg("one"))
	m = press(m, enter())
	m = press(m, runeMsg("two"))
	m = press(m, enter())
	if m.pending != "one\ntwo" {
		t.Errorf("pending = %q, want both lines kept", m.pending)
	}
}

// Ctrl+C while running is the interrupt, unconditionally — an escape
// hatch that depends on the input box being empty is not an escape hatch.
func TestCtrlCDuringATurnInterruptsEvenWithText(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, enter())
	m = press(m, runeMsg("half-typed"))

	canceled := false
	m.cancelTurn = func() { canceled = true }
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !canceled {
		t.Fatal("Ctrl+C did not interrupt the turn")
	}
	if m.ta.Value() != "half-typed" {
		t.Errorf("Ctrl+C cleared the draft: %q", m.ta.Value())
	}
}

// While the approval dialog is open, keys answer the dialog. A keystroke
// captured as input there could become an approval nobody meant to give.
func TestApprovalDialogStillOwnsTheKeysWhileOpen(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("first")
	m = press(m, enter())
	resp := make(chan byte, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "write_file", Detail: "x", Resp: resp})
	m = next.(Model)
	m = dialogSeen(m)

	m = press(m, runeMsg("y"))
	if got := <-resp; got != 'y' {
		t.Fatalf("approval answer = %q", got)
	}
	if m.pending != "" || m.ta.Value() != "" {
		t.Errorf("dialog keys leaked into the input box: pending=%q value=%q", m.pending, m.ta.Value())
	}
}

// "never ask again" edits a file on disk, so it is a separate answer
// from "a" (this session only) and it must report what it wrote.
func TestApprovalPersistAnswerWritesPolicyAndAllows(t *testing.T) {
	c := &capture{}
	var got SettingChange
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) {},
		Slash:     slashStub,
		Printer:   c.printer,
		Settings:  &SettingsData{ProjectDir: "/p"},
		ApplySetting: func(ch SettingChange) (SettingsData, string) {
			got = ch
			return SettingsData{ProjectDir: "/p"}, "saved: mcp__x__y will not ask again (policy.toml)"
		},
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	resp := make(chan byte, 1)
	next, _ := m.Update(ApprovalRequest{Tool: "mcp__x__y", Detail: "d", Resp: resp})
	m = next.(Model)
	m = dialogSeen(m)

	m = press(m, runeMsg("p"))
	if got.Tool != "mcp__x__y" || got.Value != "never" || got.Scope != ScopeGlobal {
		t.Fatalf("change = %+v", got)
	}
	// The call still has to be allowed, or the operator answered a
	// question the tool never got.
	select {
	case answer := <-resp:
		if answer != 'y' {
			t.Errorf("gate answered %c, want y", answer)
		}
	case <-time.After(time.Second):
		t.Fatal("the gate was never answered")
	}
	out := c.all()
	if !strings.Contains(out, "will not ask again") {
		t.Errorf("what was written was not reported:\n%s", out)
	}
}

// Without a policy store (one-shot, tests), the persist answer must not
// silently do nothing: it degrades to a plain allow.
func TestApprovalPersistDegradesWithoutAPolicyStore(t *testing.T) {
	c := &capture{}
	m, resp := openApproval(t, newTestModel(c), "")
	m = press(m, runeMsg("p"))
	select {
	case answer := <-resp:
		if answer != 'y' {
			t.Errorf("gate answered %c, want y", answer)
		}
	case <-time.After(time.Second):
		t.Fatal("the gate was never answered")
	}
}

// settingsRows builds a panel shaped like the real one: a few short
// sections, then one long list (every MCP tool).
func settingsRows(n int) []SettingRow {
	rows := []SettingRow{}
	for _, s := range []string{"backend", "backend", "backend", "backend", "backend",
		"safety", "limits", "limits", "limits", "session", "session", "session"} {
		rows = append(rows, SettingRow{Section: s, Label: "setting", Value: "value", Source: "default"})
	}
	for i := 0; i < n; i++ {
		rows = append(rows, SettingRow{Section: "approval policy",
			Label: "mcp__some-lookup-server__a_tool_name", Value: "default", Source: "default",
			Values: []string{"default", "always", "never"}})
	}
	return rows
}

func settingsModel(t *testing.T, c *capture, rows []SettingRow) Model {
	t.Helper()
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) {},
		Slash:     slashStub,
		Printer:   c.printer,
		Settings:  &SettingsData{Rows: rows, ProjectDir: "~/work/p"},
		ApplySetting: func(SettingChange) (SettingsData, string) {
			return SettingsData{Rows: rows, ProjectDir: "~/work/p"}, ""
		},
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	next, _ := m.openSettings()
	return next.(Model)
}

// A managed view taller than the terminal scrolls it, and the inline
// renderer's line accounting then drifts by exactly the overflow — which
// is how closing the panel left the input line one row higher than it
// started. The bottom-pinning math reserves one line, so the view must
// fit in height-1.
func TestSettingsViewNeverExceedsTheTerminal(t *testing.T) {
	c := &capture{}
	for _, tools := range []int{0, 3, 60} {
		rows := settingsRows(tools)
		m := settingsModel(t, c, rows)
		for _, height := range []int{6, 10, 24, 40, 60, 120} {
			m.height, m.width = height, 120
			for _, cursor := range []int{0, 1, 6, 11, 12, len(rows) / 2, len(rows) - 1} {
				if cursor >= len(rows) {
					continue
				}
				m.settingsCursor = cursor
				got := strings.Count(m.View(), "\n") + 1
				if got > height-1 {
					t.Errorf("tools=%d height=%d cursor=%d: view is %d lines, want at most %d",
						tools, height, cursor, got, height-1)
				}
			}
		}
	}
}

// Whatever the window, the highlighted row has to be in it — a cursor
// you cannot see is worse than no cursor.
func TestSettingsWindowAlwaysContainsTheCursor(t *testing.T) {
	c := &capture{}
	rows := settingsRows(60)
	m := settingsModel(t, c, rows)
	for _, height := range []int{6, 10, 24, 40} {
		m.height = height
		for cursor := 0; cursor < len(rows); cursor++ {
			m.settingsCursor = cursor
			start, end := m.settingsWindow(height - 7)
			if cursor < start || cursor >= end {
				t.Fatalf("height=%d cursor=%d not inside window [%d,%d)", height, cursor, start, end)
			}
		}
	}
}

// Closing the panel must leave the model exactly where it was, so the
// input block returns to the same row it occupied before.
func TestClosingSettingsRestoresTheInputPhase(t *testing.T) {
	c := &capture{}
	m := settingsModel(t, c, settingsRows(20))
	m.height, m.width = 40, 120
	before := m.printed

	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.phase != phaseInput {
		t.Fatalf("phase = %v", m.phase)
	}
	if m.settings != nil {
		t.Error("panel state survived the close")
	}
	// Opening and closing prints nothing, so the pinning counter — which
	// decides where the input block sits — must be untouched.
	if m.printed != before {
		t.Errorf("printed = %d, was %d: opening the panel moved the pinning counter", m.printed, before)
	}
}

// ADR-0010: /skill expands into a turn — the operator's line is echoed,
// the expanded body is what runs, and the synchronous slash handler
// never sees it.
func TestExpandInputRoutesSkillInvocationsToATurn(t *testing.T) {
	c := &capture{}
	m := New(Options{
		StartTurn: func(ctx context.Context, input string) { c.turns = append(c.turns, input) },
		Slash:     slashStub,
		Printer:   c.printer,
		ExpandInput: func(in string) (string, bool, string) {
			switch {
			case in == "/skill x do it":
				return "EXPANDED BODY + do it", true, ""
			case strings.HasPrefix(in, "/skill"):
				return "", true, "unknown skill"
			}
			return "", false, ""
		},
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})

	m.ta.SetValue("/skill x do it")
	m = press(m, enter())
	if len(c.turns) != 1 || c.turns[0] != "EXPANDED BODY + do it" {
		t.Fatalf("turns = %v", c.turns)
	}
	if m.phase != phaseRunning {
		t.Errorf("phase = %v", m.phase)
	}
	if !strings.Contains(c.all(), "> /skill x do it") {
		t.Errorf("the operator's own line was not echoed:\n%s", c.all())
	}
	if strings.Contains(c.all(), "slash:") {
		t.Error("the slash handler saw a skill invocation")
	}

	// An error from expansion is a message, not a turn.
	next, _ := m.Update(TurnDone{})
	m = next.(Model)
	m.ta.SetValue("/skill nope")
	m = press(m, enter())
	if len(c.turns) != 1 {
		t.Fatalf("an unknown skill started a turn: %v", c.turns)
	}
	if !strings.Contains(c.all(), "unknown skill") {
		t.Errorf("the error was silent:\n%s", c.all())
	}
	// And unrelated slash commands still reach the handler.
	m.ta.SetValue("/help")
	m = press(m, enter())
	if !strings.Contains(c.all(), "slash:/help") {
		t.Errorf("/help no longer reaches the slash handler:\n%s", c.all())
	}
}

package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The argv first message (ADR-0064) is armed by the first size report
// — after the banner, via tea.Sequence — cleared there so a resize can
// never resubmit it, and runs through the exact typed path.
func TestInitialInputSubmitsOnceThroughTypedPath(t *testing.T) {
	c := &capture{}
	m := New(Options{
		InitialInput: "hello first turn",
		StartTurn: func(ctx context.Context, input string) {
			c.turns = append(c.turns, input)
		},
		Slash:   slashStub,
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	if m.initialInput != "" {
		t.Fatal("initial input not cleared by the first size report")
	}
	if m.initialCmd() != nil {
		t.Error("a resize could re-arm the initial submission")
	}

	// Deliver the message the sequence queues.
	next, _ = m.Update(initialSubmit("hello first turn"))
	m = next.(Model)
	if len(c.turns) != 1 || c.turns[0] != "hello first turn" {
		t.Fatalf("StartTurn calls = %v, want the initial message once", c.turns)
	}
	if m.phase != phaseRunning {
		t.Error("phase not running after the initial submission")
	}
	if !strings.Contains(c.all(), "hello first turn") {
		t.Errorf("initial message not echoed like a typed line:\n%s", c.all())
	}
	// It behaves as typed input in every side channel too: history
	// recall sees it.
	if len(m.history) != 1 || m.history[0] != "hello first turn" {
		t.Errorf("initial message missing from input history: %v", m.history)
	}
}

// initialCmd is the queueing seam: nil when nothing is pending, and
// the pending text as an initialSubmit message otherwise.
func TestInitialCmdCarriesTheMessage(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	if m.initialCmd() != nil {
		t.Error("no initial input must mean no command")
	}
	m.initialInput = "x"
	cmd := m.initialCmd()
	if cmd == nil {
		t.Fatal("nil cmd for pending initial input")
	}
	if got, ok := cmd().(initialSubmit); !ok || string(got) != "x" {
		t.Fatalf("cmd yielded %#v, want initialSubmit(%q)", cmd(), "x")
	}
}

// The queueing wiring itself: the first frame's command list carries
// the initial message LAST, after every banner line — the load-bearing
// append the once-only test cannot see (independent review of
// ADR-0064, finding 2).
func TestFirstFrameCmdsCarryInitialMessageLast(t *testing.T) {
	c := &capture{}
	m := New(Options{
		InitialInput: "go",
		Banner:       []string{"b1", "b2"},
		StartTurn:    func(ctx context.Context, input string) {},
		Slash:        slashStub,
		Printer:      c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
	})
	cmds := m.firstFrameCmds()
	if len(cmds) != 4 { // clear + 2 banner lines + initial message
		t.Fatalf("len(cmds) = %d, want 4", len(cmds))
	}
	last := cmds[len(cmds)-1]
	if got, ok := last().(initialSubmit); !ok || string(got) != "go" {
		t.Fatalf("last cmd yielded %#v, want initialSubmit(%q)", last(), "go")
	}
	m.initialInput = ""
	if n := len(m.firstFrameCmds()); n != 3 {
		t.Errorf("without initial input len = %d, want 3 (clear + banner)", n)
	}
}

// A type-ahead turn can already be running when the initial message
// arrives — the input reader subscribes before the first size report.
// Then it queues like an Enter during a running turn (ADR-0007), and a
// command is refused visibly instead of merging (ADR-0021 §7).
func TestInitialSubmitDuringRunningTurnQueues(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("type-ahead turn")
	m = press(m, enter())
	if m.phase != phaseRunning || len(c.turns) != 1 {
		t.Fatal("setup: type-ahead turn not running")
	}
	next, _ = m.Update(initialSubmit("argv message"))
	m = next.(Model)
	if len(c.turns) != 1 {
		t.Fatalf("initial message started a concurrent turn: %v", c.turns)
	}
	if m.pending != "argv message" {
		t.Fatalf("initial message not queued: %q", m.pending)
	}
	next, _ = m.Update(initialSubmit("/help"))
	m = next.(Model)
	if strings.Contains(m.pending, "/help") {
		t.Error("a command merged into the queue")
	}
}

// A draft typed ahead (no Enter yet) survives the initial submission:
// the argv message runs, the draft goes back into the box.
func TestInitialSubmitPreservesTypedDraft(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("half-typed draft")
	next, _ = m.Update(initialSubmit("argv message"))
	m = next.(Model)
	if len(c.turns) != 1 || c.turns[0] != "argv message" {
		t.Fatalf("turns = %v, want the argv message once", c.turns)
	}
	if m.ta.Value() != "half-typed draft" {
		t.Errorf("draft lost: %q", m.ta.Value())
	}
}

// A slash first message routes to the slash handler, not to a model
// turn — the typed path decides, exactly as with the keyboard.
func TestInitialInputSlashRoutesAsSlash(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(initialSubmit("/nope"))
	m = next.(Model)
	if len(c.turns) != 0 {
		t.Fatalf("slash first message started a model turn: %v", c.turns)
	}
	if m.phase == phaseRunning {
		t.Error("slash first message left the TUI in a running phase")
	}
	if !strings.Contains(c.all(), "unknown command") {
		t.Errorf("slash output missing:\n%s", c.all())
	}
}

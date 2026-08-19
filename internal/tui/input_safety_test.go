package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ADR-0021: a keystroke already in flight for the input box when the
// approval dialog appears must not answer it — within the grace window
// keys are dropped; after it, they work.
func TestApprovalTypeAheadGrace(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	req, resp := approvalReq("")
	next, _ := m.Update(req)
	m = next.(Model) // approvalAt = now: inside the grace window

	m = press(m, enter())      // the Enter that was aimed at the textarea
	m = press(m, runeMsg("a")) // a letter from a word like "and"
	select {
	case got := <-resp:
		t.Fatalf("typed-ahead key answered the dialog with %q", got)
	default:
	}
	if m.phase != phaseApproval {
		t.Fatal("dialog dismissed by a typed-ahead key")
	}

	m = dialogSeen(m) // grace elapsed: the operator has read the dialog
	m = press(m, enter())
	if got := <-resp; got != 'y' {
		t.Errorf("deliberate Enter after the grace = %q, want 'y'", got)
	}
}

// ADR-0021 §7: ! and / cannot be queued mid-run — the merged pending
// would be prefix-routed whole (queued prose executed as shell, or
// dropped after a slash command). The text stays in the box.
func TestCommandsCannotBeQueuedMidRun(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter()) // now running

	for _, cmd := range []string{"!make test", "/clear"} {
		m.ta.SetValue(cmd)
		m = press(m, enter())
		if m.pending != "" {
			t.Fatalf("%q was queued — the merged input would be prefix-routed", cmd)
		}
		if m.ta.Value() != cmd {
			t.Errorf("%q vanished from the input box: %q", cmd, m.ta.Value())
		}
	}
	if !strings.Contains(c.all(), "Ctrl+C") {
		t.Errorf("the refusal must teach the escape hatch: %q", c.all())
	}

	// Plain prose still queues.
	m.ta.SetValue("explain the result")
	m = press(m, enter())
	if m.pending != "explain the result" {
		t.Errorf("prose failed to queue: %q", m.pending)
	}
}

// ADR-0021: a half-typed draft survives the queued message being sent —
// and on a failed turn both come back, in writing order.
func TestDraftSurvivesQueuedSend(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())

	m.ta.SetValue("queued message")
	m = press(m, enter())
	m.ta.SetValue("half-typed draft") // written after, never entered

	next, _ := m.Update(TurnDone{})
	m = next.(Model)
	if len(c.turns) != 2 || c.turns[1] != "queued message" {
		t.Fatalf("turns = %v — the queued message must be sent alone", c.turns)
	}
	if m.ta.Value() != "half-typed draft" {
		t.Errorf("draft clobbered: %q", m.ta.Value())
	}
}

func TestDraftAndQueueHandedBackOnError(t *testing.T) {
	c := &capture{}
	m := newTestModel(c)
	m.ta.SetValue("go")
	m = press(m, enter())
	m.ta.SetValue("queued")
	m = press(m, enter())
	m.ta.SetValue("draft")

	next, _ := m.Update(TurnDone{Err: errors.New("boom")})
	m = next.(Model)
	if len(c.turns) != 1 {
		t.Fatalf("a failed turn sent the queued message: %v", c.turns)
	}
	if m.ta.Value() != "queued\ndraft" {
		t.Errorf("hand-back = %q, want both in writing order", m.ta.Value())
	}
}

// ADR-0021: an interrupted ! command hands a queued message back
// instead of auto-sending it against a world that no longer exists;
// a cleanly completed one still auto-sends.
func TestShellInterruptHandsQueuedBack(t *testing.T) {
	c := &capture{}
	m := New(Options{
		Printer: c.printer,
		RenderFactory: func(width int) func(string) string {
			return func(s string) string { return s }
		},
		Slash:     slashStub,
		Shell:     func(ctx context.Context, command string) {},
		StartTurn: func(ctx context.Context, input string) { c.turns = append(c.turns, input) },
	})
	m.ta.SetValue("!sleep 100")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("shell submit did not enter the running phase")
	}
	m.ta.SetValue("queued after shell")
	m = press(m, enter())

	next, _ := m.Update(ShellDone{Output: "error: context canceled", Interrupted: true})
	m = next.(Model)
	if len(c.turns) != 0 {
		t.Fatalf("interrupted shell auto-sent the queued message: %v", c.turns)
	}
	if m.ta.Value() != "queued after shell" {
		t.Errorf("queued message not handed back: %q", m.ta.Value())
	}

	m.ta.SetValue("!ls")
	m = press(m, enter())
	m.ta.SetValue("queued two")
	m = press(m, enter())
	next, _ = m.Update(ShellDone{Output: "ok"})
	m = next.(Model)
	if len(c.turns) != 1 || c.turns[0] != "queued two" {
		t.Errorf("clean shell did not auto-send the queued message: %v", c.turns)
	}
}

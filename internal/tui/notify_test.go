package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The bytes each [tui].notify value writes (ADR-0104 §2). OSC 9 is ended
// by ST so that the BEL after it is a bell of its own, not the OSC's
// terminator: a terminal that drops OSC 9 still rings.
func TestNoticeBytes(t *testing.T) {
	cases := map[string]string{
		"bell": "\a",
		"osc9": "\x1b]9;gem-agent needs your answer\x1b\\\a",
		"off":  "",
		"":     "",
		"BELL": "",
	}
	for mode, want := range cases {
		if got := Notice(mode); got != want {
			t.Errorf("Notice(%q) = %q, want %q", mode, got, want)
		}
	}
}

// noticeRig is a model whose notifier counts and whose clock the test
// moves.
type noticeRig struct {
	t     *testing.T
	m     Model
	at    time.Time
	calls int
}

func newNoticeRig(t *testing.T) *noticeRig {
	r := &noticeRig{t: t, at: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)}
	r.m = New(Options{
		Slash:         slashStub,
		Printer:       (&capture{}).printer,
		RenderFactory: func(int) func(string) string { return func(s string) string { return s } },
		Notify:        func() { r.calls++ },
	})
	r.m.now = func() time.Time { return r.at }
	return r
}

func (r *noticeRig) update(msg tea.Msg) tea.Cmd {
	next, cmd := r.m.Update(msg)
	r.m = next.(Model)
	return cmd
}

// after moves the clock to `sec` seconds after the rig's start.
func (r *noticeRig) after(sec float64) {
	r.at = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).Add(time.Duration(sec * float64(time.Second)))
}

// check delivers the scheduled check for the current dialog and runs
// whatever it asks for, except a further tick (which only sleeps). It
// reports whether the check asked to look again.
func (r *noticeRig) check(gen int) (again bool) {
	r.t.Helper()
	cmd := r.update(noticeCheck{gen: gen})
	if cmd == nil {
		return false
	}
	if !r.m.noticeDone {
		return true // a tea.Tick: running it would only wait six seconds
	}
	cmd()
	return false
}

func (r *noticeRig) openApproval() (int, chan ApprovalAnswer) {
	r.t.Helper()
	req, resp := approvalReq("")
	if cmd := r.update(req); cmd == nil {
		r.t.Fatal("opening a dialog with a notifier must schedule the first check")
	}
	r.m = dialogSeen(r.m)
	return r.m.noticeGen, resp
}

// A dialog nobody touches is notified at the first check, once.
func TestNoticeAtFirstQuietCheckAndOnlyOnce(t *testing.T) {
	r := newNoticeRig(t)
	gen, _ := r.openApproval()
	r.after(6)
	if r.check(gen) {
		t.Fatal("no keystroke: the first check must notify, not look again")
	}
	if r.calls != 1 {
		t.Fatalf("notified %d times, want 1", r.calls)
	}
	r.after(12)
	r.check(gen)
	if r.calls != 1 {
		t.Fatalf("a dialog left open must get one notice, got %d", r.calls)
	}
}

// The measured Claude Code case (ADR-0104 "Measured"): keys at 4.3 s and
// 8.3 s, notice at the 18 s check — a keystroke makes the next check
// pass; it does not restart a six-second timer.
func TestKeystrokeMakesTheNextCheckPass(t *testing.T) {
	r := newNoticeRig(t)
	gen, _ := r.openApproval()
	r.after(4.3)
	r.update(tea.KeyMsg{Type: tea.KeyRight})
	r.after(6)
	if !r.check(gen) || r.calls != 0 {
		t.Fatalf("a key 1.7 s before the check must defer it (again, calls=%d)", r.calls)
	}
	r.after(8.3)
	r.update(tea.KeyMsg{Type: tea.KeyLeft})
	r.after(12)
	if !r.check(gen) || r.calls != 0 {
		t.Fatalf("a key 3.7 s before the check must defer it (calls=%d)", r.calls)
	}
	r.after(18)
	if r.check(gen) || r.calls != 1 {
		t.Fatalf("9.7 s of quiet: the 18 s check must notify (calls=%d)", r.calls)
	}
}

// An answered dialog is never notified, and a check scheduled for one
// dialog does nothing for the next.
func TestAnsweredOrReplacedDialogIsNotNotified(t *testing.T) {
	r := newNoticeRig(t)
	first, resp := r.openApproval()
	r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if got := <-resp; got.Key != 'y' {
		t.Fatalf("answer = %q, want y", got.Key)
	}
	r.after(6)
	r.check(first)
	if r.calls != 0 {
		t.Fatal("an answered dialog must not be notified")
	}

	ask := make(chan int, 1)
	r.update(AskRequest{Question: "which?", Options: []string{"A", "B"}, Resp: ask})
	second := r.m.noticeGen
	if second == first {
		t.Fatal("each dialog opening needs its own generation")
	}
	r.after(12)
	r.check(first)
	if r.calls != 0 {
		t.Fatal("the first dialog's check must not notify for the second")
	}
	r.check(second)
	if r.calls != 1 {
		t.Fatalf("the question dialog left alone must be notified, got %d", r.calls)
	}
}

// With [tui].notify = "off" the caller passes no notifier, and nothing
// is scheduled at all (ADR-0104 Consequences).
func TestNoNotifierSchedulesNothing(t *testing.T) {
	m := New(Options{
		Slash:         slashStub,
		Printer:       (&capture{}).printer,
		RenderFactory: func(int) func(string) string { return func(s string) string { return s } },
	})
	req, _ := approvalReq("")
	if _, cmd := m.Update(req); cmd != nil {
		t.Fatal("no notifier: opening a dialog must schedule nothing")
	}
}

// After one dialog was notified, the next one is notified too: the
// once-per-dialog mark belongs to the dialog, not the session.
func TestEachDialogGetsItsOwnNotice(t *testing.T) {
	r := newNoticeRig(t)
	first, resp := r.openApproval()
	r.after(6)
	r.check(first)
	r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	<-resp
	second, _ := r.openApproval()
	r.after(12)
	r.check(second)
	if r.calls != 2 {
		t.Fatalf("two dialogs each left alone: notified %d times, want 2", r.calls)
	}
}

// Every key the operator presses in a dialog counts — one the type-ahead
// grace window drops, one typed into the deny-with-reason field, a paste.
func TestEveryKeyInADialogDefersTheNotice(t *testing.T) {
	cases := map[string]func(r *noticeRig){
		"grace window": func(r *noticeRig) {
			r.m.approvalAt = r.at // the key lands inside the grace window
			r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		},
		"reason field": func(r *noticeRig) {
			r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
			if !r.m.reasonMode {
				r.t.Fatal("N should open the reason field")
			}
			r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
		},
		"paste": func(r *noticeRig) {
			r.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("pasted"), Paste: true})
		},
	}
	for name, press := range cases {
		t.Run(name, func(t *testing.T) {
			r := newNoticeRig(t)
			gen, _ := r.openApproval()
			r.after(4)
			press(r)
			r.after(6)
			if !r.check(gen) || r.calls != 0 {
				t.Fatalf("a key 2 s before the check must defer it (calls=%d)", r.calls)
			}
		})
	}
}

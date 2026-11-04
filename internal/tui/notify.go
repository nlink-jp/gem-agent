package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// NoticeQuiet is how long a dialog must go without a keystroke before
// the operator is told it is waiting (ADR-0104 §1). It is also the
// interval between checks: Claude Code's permission_prompt was measured
// to check every six seconds from the moment its prompt opened and to
// notify at the first check with no keystroke in the six seconds
// before it.
const NoticeQuiet = 6 * time.Second

// noticeBody is the OSC 9 text. Fixed, never text from the turn: the
// notice is the runtime's own bytes, which is why ADR-0093 neither
// covers it nor has anything to neutralise (ADR-0104 §2).
const noticeBody = "gem-agent needs your answer"

// Notice returns the bytes [tui].notify writes to the terminal, or ""
// when it writes nothing. OSC 9 is ended by ST, not BEL, so the BEL
// after it is a bell of its own: a terminal that drops OSC 9 (Terminal.app,
// and tmux, which does not pass it on — measured) still rings.
func Notice(mode string) string {
	switch mode {
	case "bell":
		return "\a"
	case "osc9":
		return "\x1b]9;" + noticeBody + "\x1b\\" + "\a"
	}
	return ""
}

// noticeCheck is one scheduled look at whether dialog gen has been left
// alone long enough to notify.
type noticeCheck struct{ gen int }

// dialogOpen reports whether a dialog is waiting on the operator.
func (m *Model) dialogOpen() bool {
	return m.phase == phaseApproval || m.phase == phaseAsk
}

// armNotice starts the checks for a dialog that just opened. Each
// opening gets its own generation, so a check scheduled for a dialog
// that was answered — or replaced by the next one — does nothing.
func (m *Model) armNotice() tea.Cmd {
	m.noticeGen++
	m.noticeKeyAt = time.Time{}
	m.noticeDone = false
	if m.notify == nil {
		return nil
	}
	return noticeTick(m.noticeGen)
}

func noticeTick(gen int) tea.Cmd {
	return tea.Tick(NoticeQuiet, func(time.Time) tea.Msg { return noticeCheck{gen: gen} })
}

// noticeKey records a keystroke while a dialog is open: it makes the
// next check pass rather than restarting a timer.
func (m *Model) noticeKey() {
	if m.dialogOpen() {
		m.noticeKeyAt = m.clock()
	}
}

// checkNotice decides one check: stale, too soon (look again in six
// seconds), or notify — once per dialog.
func (m *Model) checkNotice(c noticeCheck) tea.Cmd {
	if c.gen != m.noticeGen || !m.dialogOpen() || m.noticeDone || m.notify == nil {
		return nil
	}
	if !m.noticeKeyAt.IsZero() && m.clock().Sub(m.noticeKeyAt) < NoticeQuiet {
		return noticeTick(c.gen)
	}
	m.noticeDone = true
	notify := m.notify
	// Off the update loop: the write waits for the renderer's flush to
	// finish, and Update must not wait on the terminal.
	return func() tea.Msg {
		notify()
		return nil
	}
}

// clock is the model's time: its own when a test set one.
func (m *Model) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

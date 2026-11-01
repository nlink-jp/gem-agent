package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// ADR-0033 regression tests: the running status distinguishes a
// thinking model, a stalled stream, and a backoff retry — the three
// states that used to render identically as "thinking…".

func runningModel(t *testing.T) (Model, *capture) {
	t.Helper()
	c := &capture{}
	m := newTestModel(c)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = next.(Model)
	m.ta.SetValue("go")
	m = press(m, enter())
	if m.phase != phaseRunning {
		t.Fatal("expected phaseRunning")
	}
	return m, c
}

func TestHeartbeatShowsElapsedAndChunks(t *testing.T) {
	m, _ := runningModel(t)
	for range 3 {
		next, _ := m.Update(StreamUpdate{Kind: "chunk"})
		m = next.(Model)
	}
	v := m.View()
	if !strings.Contains(v, "3 chunks") {
		t.Errorf("heartbeat missing chunk count:\n%s", v)
	}
	if !strings.Contains(v, "last 0s") {
		t.Errorf("heartbeat missing last-chunk age:\n%s", v)
	}
}

// Silence is never an accusation (ADR-0100): the wire is silent while
// the model composes a function call or the server reads a long
// context, measured up to 205s on live connections, and a dead one is
// named by the transport's health check as a turn error. The heartbeat
// keeps counting the age, neutral, in both languages and at any length.
func TestSilenceIsNeverAnAccusation(t *testing.T) {
	for _, lang := range []uitext.Lang{uitext.EN, uitext.JA} {
		m, _ := runningModel(t)
		m.msgs = uitext.For(lang)
		next, _ := m.Update(StreamUpdate{Kind: "chunk"})
		m = next.(Model)
		for _, age := range []int{40, 95, 210, 600} {
			m.lastChunk = time.Now().Add(-time.Duration(age) * time.Second)
			v := m.View()
			for _, word := range []string{"stalled", "stall", "失速", "データなし"} {
				if strings.Contains(v, word) {
					t.Errorf("%s, silent %ds: the heartbeat accuses the connection (%q):\n%s", lang, age, word, v)
				}
			}
			want := map[uitext.Lang]string{uitext.EN: "last " + strconv.Itoa(age) + "s", uitext.JA: "最終 " + strconv.Itoa(age) + " 秒前"}[lang]
			if !strings.Contains(v, want) {
				t.Errorf("%s, silent %ds: the heartbeat dropped the age (want %q):\n%s", lang, age, want, v)
			}
		}
	}
}

// The status line must leave the Ctrl+C hint on screen at 80 columns —
// it is the way out of a slow turn, and truncation cuts it first.
func TestStatusLineKeepsTheCtrlCHintAt80Columns(t *testing.T) {
	for _, lang := range []uitext.Lang{uitext.EN, uitext.JA} {
		c := &capture{}
		m := newTestModel(c)
		m.msgs = uitext.For(lang)
		next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		m = next.(Model)
		m.ta.SetValue("go")
		m = press(m, enter())
		next, _ = m.Update(StreamUpdate{Kind: "chunk"})
		m = next.(Model)
		for _, age := range []time.Duration{40 * time.Second, 600 * time.Second} {
			m.lastChunk = time.Now().Add(-age)
			for _, line := range strings.Split(m.View(), "\n") {
				if !strings.Contains(line, "chunks") && !strings.Contains(line, "チャンク") {
					continue
				}
				// The WHOLE hint, not a prefix of it: truncation cuts
				// mid-word and "(Ctrl+C interrupt" would pass a
				// substring check while reading as breakage.
				if hint := strings.TrimSpace(m.msgs.CtrlCHint); !strings.Contains(line, hint) {
					t.Errorf("%s, silent %s: the status line lost %q:\n%q", lang, age, hint, line)
				}
			}
		}
	}
}

func TestRetryIsVisible(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "retry", Attempt: 2, Max: 3, Cause: "429", DelayMS: 4000})
	m = next.(Model)
	v := m.View()
	if !strings.Contains(v, "retry 2/3 (429)") || !strings.Contains(v, "4s") {
		t.Errorf("retry not visible:\n%s", v)
	}
	// Data flowing again clears the retry line.
	next, _ = m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	if strings.Contains(m.View(), "retry 2/3") {
		t.Error("retry line survived fresh data")
	}
}

func TestThoughtTailDisplaysAndYieldsToAnswer(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "thought", Thought: "Reading the config loader first"})
	m = next.(Model)
	if !strings.Contains(m.View(), "Reading the config loader first") {
		t.Errorf("thought not displayed:\n%s", m.View())
	}
	// The visible answer supersedes the thought tail.
	next, _ = m.Update(TextDelta("The answer is"))
	m = next.(Model)
	if strings.Contains(m.View(), "Reading the config loader") {
		t.Error("thought tail survived the start of the answer")
	}
	// A tool call also ends the round's thoughts.
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "checking files"})
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "read_file", Detail: "x"})
	m = next.(Model)
	if m.thoughtTail != "" {
		t.Error("thought tail survived a tool call")
	}
	// TurnDone clears everything.
	next, _ = m.Update(StreamUpdate{Kind: "thought", Thought: "wrapping up"})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	m = next.(Model)
	if m.thoughtTail != "" || !m.turnStart.IsZero() {
		t.Error("observability state survived TurnDone")
	}
}

// Thoughts are ephemeral display (ADR-0033 §3): nothing reaches the
// scrollback printer.
func TestThoughtsNeverReachScrollback(t *testing.T) {
	m, c := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "thought", Thought: "secret internal reasoning"})
	m = next.(Model)
	next, _ = m.Update(TurnDone{})
	_ = next.(Model)
	for _, line := range c.printed {
		if strings.Contains(line, "secret internal reasoning") {
			t.Fatalf("thought text reached the scrollback: %q", line)
		}
	}
}

// ADR-0034 §3: the last-resort exit. A wedged tool that ignores
// cancellation must not trap the operator: second Ctrl+C warns, third
// quits — and a completed turn resets the ladder.
func TestTripleCtrlCEscapesAWedgedTool(t *testing.T) {
	m, c := runningModel(t)
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC}) // 1st: interrupt
	if !m.interruptSent {
		t.Fatal("interrupt not sent")
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC}) // 2nd: warn
	found := false
	for _, line := range c.printed {
		if strings.Contains(line, "Ctrl+C") && strings.Contains(line, "quits") {
			found = true
		}
	}
	if !found {
		t.Errorf("second Ctrl+C must warn about the exit: %q", c.printed)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}) // 3rd: quit
	m = next.(Model)
	if cmd == nil {
		t.Fatal("third Ctrl+C returned no command")
	}
	// The ladder resets when a turn actually completes.
	m2, _ := runningModel(t)
	m2 = press(m2, tea.KeyMsg{Type: tea.KeyCtrlC})
	next, _ = m2.Update(TurnDone{})
	m2 = next.(Model)
	if m2.interruptSent || m2.interruptPresses != 0 {
		t.Error("interrupt ladder must reset on TurnDone")
	}
}

// During a tool the only streams are side-calls (risk/progress
// review): their chunks must not end the tool state, so their thoughts
// stay out of the main tail. ToolDone ends it (review round 3).
func TestToolStateEndsOnToolDoneNotOnSideCallChunks(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "shell_exec", Detail: "make build"})
	m = next.(Model)
	next, _ = m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	if !m.toolRunning {
		t.Error("a side-call chunk ended the tool state")
	}
	next, _ = m.Update(ToolDone{Name: "shell_exec"})
	m = next.(Model)
	if m.toolRunning {
		t.Error("ToolDone did not end the tool state")
	}
}

// ADR-0036: the ask_user dialog — digits pick in one press, Esc
// declines, interrupted turns auto-decline, releaseTurn never strands
// the blocked tool goroutine.
func TestAskDialog(t *testing.T) {
	m, _ := runningModel(t)
	resp := make(chan int, 1)
	next, _ := m.Update(AskRequest{Question: "どっち？", Options: []string{"A 案", "B 案", "C 案"}, Resp: resp})
	m = next.(Model)
	if m.phase != phaseAsk {
		t.Fatal("ask request should switch phase")
	}
	v := m.View()
	for _, want := range []string{"どっち？", "1) A 案", "2) B 案", "Esc"} {
		if !strings.Contains(v, want) {
			t.Errorf("ask view missing %q:\n%s", want, v)
		}
	}
	m.askAt = time.Time{} // skip the typed-ahead grace
	m = press(m, runeMsg("2"))
	if got := <-resp; got != 1 {
		t.Errorf("digit pick = %d, want 1", got)
	}
	if m.phase != phaseRunning {
		t.Error("phase should return to running")
	}

	// Esc declines.
	resp2 := make(chan int, 1)
	next, _ = m.Update(AskRequest{Question: "q", Options: []string{"a", "b"}, Resp: resp2})
	m = next.(Model)
	m.askAt = time.Time{}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := <-resp2; got != -1 {
		t.Errorf("Esc = %d, want -1", got)
	}

	// After Ctrl+C, an arriving ask is auto-declined.
	m = press(m, tea.KeyMsg{Type: tea.KeyCtrlC})
	resp3 := make(chan int, 1)
	next, _ = m.Update(AskRequest{Question: "q", Options: []string{"a", "b"}, Resp: resp3})
	m = next.(Model)
	if m.phase == phaseAsk {
		t.Error("ask dialog opened for an interrupted turn")
	}
	if got := <-resp3; got != -1 {
		t.Errorf("interrupted ask = %d, want -1", got)
	}

	// A pending dialog is drained on TurnDone — the tool goroutine
	// must never be stranded.
	m2, _ := runningModel(t)
	resp4 := make(chan int, 1)
	next, _ = m2.Update(AskRequest{Question: "q", Options: []string{"a", "b"}, Resp: resp4})
	m2 = next.(Model)
	next, _ = m2.Update(TurnDone{})
	_ = next.(Model)
	if got := <-resp4; got != -1 {
		t.Errorf("drained ask = %d, want -1", got)
	}
}

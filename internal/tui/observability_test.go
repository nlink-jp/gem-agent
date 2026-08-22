package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

func TestStallWarningAfterSilence(t *testing.T) {
	m, _ := runningModel(t)
	// No chunk for stallSeconds: rewind the clock instead of sleeping.
	m.turnStart = time.Now().Add(-25 * time.Second)
	v := m.View()
	if !strings.Contains(v, "stalled") {
		t.Errorf("no stall warning after %ds silence:\n%s", stallSeconds, v)
	}
	// A chunk arriving clears the stall reading.
	next, _ := m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	if strings.Contains(m.View(), "stalled") {
		t.Error("stall warning survived fresh data")
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

// A long tool execution receives no stream chunks BY DESIGN — the
// stall warning must not cry wolf there (operator question follow-up:
// "is the silent tool the same cause?" — during the tool, silence is
// normal; the warning is for the model stream).
func TestNoStallWarningWhileAToolRuns(t *testing.T) {
	m, _ := runningModel(t)
	next, _ := m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	next, _ = m.Update(ToolCall{Name: "shell_exec", Detail: "make build"})
	m = next.(Model)
	m.lastChunk = time.Now().Add(-25 * time.Second) // long tool, no stream
	if strings.Contains(m.View(), "stalled") {
		t.Errorf("false stall warning during tool execution:\n%s", m.View())
	}
	// A chunk during the tool is a SIDE-CALL stream (risk/progress
	// review) and must not re-arm the detector (review round 3).
	next, _ = m.Update(StreamUpdate{Kind: "chunk"})
	m = next.(Model)
	m.lastChunk = time.Now().Add(-25 * time.Second)
	if strings.Contains(m.View(), "stalled") {
		t.Errorf("side-call chunk re-armed the stall detector:\n%s", m.View())
	}
	// The tool returning (ToolDone) re-arms it: a real stall on the
	// next model round is warned.
	next, _ = m.Update(ToolDone{Name: "shell_exec"})
	m = next.(Model)
	m.lastChunk = time.Now().Add(-25 * time.Second)
	if !strings.Contains(m.View(), "stalled") {
		t.Errorf("real stall after the tool returned not warned:\n%s", m.View())
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

package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// autoResponder answers every ApprovalRequest with a fixed byte.
type autoResponder struct {
	answer byte
	asked  int
}

func (a *autoResponder) Send(msg tea.Msg) {
	if req, ok := msg.(ApprovalRequest); ok {
		a.asked++
		req.Resp <- a.answer
	}
}

func TestGateFailsClosedWithoutProgram(t *testing.T) {
	g := NewGate()
	if g.Approve("shell_exec", "x", "") {
		t.Fatal("gate without a program must deny")
	}
}

func TestGateYesNo(t *testing.T) {
	g := NewGate()
	yes := &autoResponder{answer: 'y'}
	g.SetProgram(yes)
	if !g.Approve("shell_exec", "x", "") {
		t.Error("y should approve")
	}
	no := &autoResponder{answer: 'n'}
	g.SetProgram(no)
	if g.Approve("shell_exec", "x", "") {
		t.Error("n should deny")
	}
}

func TestGateAlwaysSkipsUI(t *testing.T) {
	g := NewGate()
	r := &autoResponder{answer: 'a'}
	g.SetProgram(r)
	if !g.Approve("write_file", "x", "") {
		t.Fatal("a should approve")
	}
	if !g.Approve("write_file", "y", "") {
		t.Fatal("allowlisted tool should approve")
	}
	if r.asked != 1 {
		t.Errorf("UI asked %d times, want 1 (allowlist lives in the gate)", r.asked)
	}
	// Different tool still asks.
	if !g.Approve("edit_file", "z", "") {
		t.Fatal("other tool should ask and approve")
	}
	if r.asked != 2 {
		t.Errorf("asked = %d, want 2", r.asked)
	}
}

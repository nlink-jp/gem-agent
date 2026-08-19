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
	if g.Approve("shell_exec", "x", "", false) {
		t.Fatal("gate without a program must deny")
	}
}

func TestGateYesNo(t *testing.T) {
	g := NewGate()
	yes := &autoResponder{answer: 'y'}
	g.SetProgram(yes)
	if !g.Approve("shell_exec", "x", "", false) {
		t.Error("y should approve")
	}
	no := &autoResponder{answer: 'n'}
	g.SetProgram(no)
	if g.Approve("shell_exec", "x", "", false) {
		t.Error("n should deny")
	}
}

func TestGateAlwaysSkipsUI(t *testing.T) {
	g := NewGate()
	r := &autoResponder{answer: 'a'}
	g.SetProgram(r)
	if !g.Approve("write_file", "x", "", false) {
		t.Fatal("a should approve")
	}
	if !g.Approve("write_file", "y", "", false) {
		t.Fatal("allowlisted tool should approve")
	}
	if r.asked != 1 {
		t.Errorf("UI asked %d times, want 1 (allowlist lives in the gate)", r.asked)
	}
	// Different tool still asks.
	if !g.Approve("edit_file", "z", "", false) {
		t.Fatal("other tool should ask and approve")
	}
	if r.asked != 2 {
		t.Errorf("asked = %d, want 2", r.asked)
	}
}

// ADR-0021 §5: mustPrompt (Block-tier / always-policy) skips the
// allowlist — an earlier 'a' may not answer it — while later ordinary
// calls still benefit from the registration.
func TestGateMustPromptSkipsAllowlist(t *testing.T) {
	g := NewGate()
	r := &autoResponder{answer: 'a'}
	g.SetProgram(r)
	if !g.Approve("shell_exec", "mkdir build", "", false) {
		t.Fatal("first call should approve via 'a'")
	}
	deny := &autoResponder{answer: 'n'}
	g.SetProgram(deny)
	if g.Approve("shell_exec", "sudo whoami", "privilege escalation", true) {
		t.Error("mustPrompt call was answered by the allowlist, not the operator")
	}
	if deny.asked != 1 {
		t.Errorf("mustPrompt call reached the UI %d times, want 1", deny.asked)
	}
	// The registration still covers ordinary calls.
	if !g.Approve("shell_exec", "mkdir dist", "", false) {
		t.Error("ordinary call after 'a' should not ask again")
	}
	if deny.asked != 1 {
		t.Errorf("ordinary allowlisted call reached the UI (asked=%d)", deny.asked)
	}
}

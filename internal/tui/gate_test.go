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
		req.Resp <- ApprovalAnswer{Key: a.answer}
	}
}

func TestGateFailsClosedWithoutProgram(t *testing.T) {
	g := NewGate()
	if allowed(g.Approve("shell_exec", "x", "", "", false)) {
		t.Fatal("gate without a program must deny")
	}
}

func TestGateYesNo(t *testing.T) {
	g := NewGate()
	yes := &autoResponder{answer: 'y'}
	g.SetProgram(yes)
	if !allowed(g.Approve("shell_exec", "x", "", "", false)) {
		t.Error("y should approve")
	}
	no := &autoResponder{answer: 'n'}
	g.SetProgram(no)
	if allowed(g.Approve("shell_exec", "x", "", "", false)) {
		t.Error("n should deny")
	}
}

func TestGateAlwaysSkipsUI(t *testing.T) {
	g := NewGate()
	r := &autoResponder{answer: 'a'}
	g.SetProgram(r)
	if !allowed(g.Approve("write_file", "x", "", "", false)) {
		t.Fatal("a should approve")
	}
	if !allowed(g.Approve("write_file", "y", "", "", false)) {
		t.Fatal("allowlisted tool should approve")
	}
	if r.asked != 1 {
		t.Errorf("UI asked %d times, want 1 (allowlist lives in the gate)", r.asked)
	}
	// Different tool still asks.
	if !allowed(g.Approve("edit_file", "z", "", "", false)) {
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
	if !allowed(g.Approve("shell_exec", "mkdir build", "", "", false)) {
		t.Fatal("first call should approve via 'a'")
	}
	deny := &autoResponder{answer: 'n'}
	g.SetProgram(deny)
	if allowed(g.Approve("shell_exec", "sudo whoami", "", "privilege escalation", true)) {
		t.Error("mustPrompt call was answered by the allowlist, not the operator")
	}
	if deny.asked != 1 {
		t.Errorf("mustPrompt call reached the UI %d times, want 1", deny.asked)
	}
	// The registration still covers ordinary calls.
	if !allowed(g.Approve("shell_exec", "mkdir dist", "", "", false)) {
		t.Error("ordinary call after 'a' should not ask again")
	}
	if deny.asked != 1 {
		t.Errorf("ordinary allowlisted call reached the UI (asked=%d)", deny.asked)
	}
}

// allowed drops the allowlist flag and deny reason for assertions that
// only care about the verdict (ADR-0048 §1).
func allowed(approved, _ bool, _ string) bool { return approved }

// The gate reports an allowlist answer as such: the learner counts one
// keystroke once, however many calls it covers.
func TestGateReportsAllowlistAnswers(t *testing.T) {
	g := NewGate()
	g.SetProgram(&autoResponder{answer: 'a'})
	if approved, fromAllowlist, _ := g.Approve("shell_exec", "x", "", "", false); !approved || fromAllowlist {
		t.Errorf("the 'a' keystroke = (%v, %v), want (true, false)", approved, fromAllowlist)
	}
	if approved, fromAllowlist, _ := g.Approve("shell_exec", "y", "", "", false); !approved || !fromAllowlist {
		t.Errorf("the allowlist answer = (%v, %v), want (true, true)", approved, fromAllowlist)
	}
}

// reasonResponder answers every request with a reasoned denial.
type reasonResponder struct{ reason string }

func (r *reasonResponder) Send(msg tea.Msg) {
	if req, ok := msg.(ApprovalRequest); ok {
		req.Resp <- ApprovalAnswer{Key: 'n', Reason: r.reason}
	}
}

// ADR-0060: the operator's typed reason rides back through the gate
// verbatim, and only on denials.
func TestGateRelaysDenyReason(t *testing.T) {
	g := NewGate()
	g.SetProgram(&reasonResponder{reason: "use the work dir instead"})
	approved, _, reason := g.Approve("write_file", "x", "", "", false)
	if approved {
		t.Fatal("reasoned denial approved")
	}
	if reason != "use the work dir instead" {
		t.Errorf("deny reason = %q", reason)
	}
}

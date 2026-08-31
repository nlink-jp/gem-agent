package approve

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestApproveYes(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("y\n"), &out)
	if !allowed(g.Approve("shell_exec", "rm -rf build", "", "", false)) {
		t.Error("y should approve")
	}
	if !strings.Contains(out.String(), "shell_exec") || !strings.Contains(out.String(), "rm -rf build") {
		t.Error("prompt should show tool name and detail")
	}
}

func TestEscalationReasonShown(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("n\n"), &out)
	allowed(g.Approve("shell_exec", "rm -rf build", "",
		"auto-approve blocked by rule (always asks): recursive force delete", false))
	if !strings.Contains(out.String(), "⚠") || !strings.Contains(out.String(), "recursive force delete") {
		t.Errorf("prompt should show why auto-approve escalated: %q", out.String())
	}
}

func TestDenyNo(t *testing.T) {
	g := New(strings.NewReader("n\n"), &bytes.Buffer{})
	if allowed(g.Approve("write_file", "x.txt", "", "", false)) {
		t.Error("n should deny")
	}
}

func TestEmptyLineDenies(t *testing.T) {
	g := New(strings.NewReader("\n"), &bytes.Buffer{})
	if allowed(g.Approve("write_file", "x.txt", "", "", false)) {
		t.Error("bare Enter should deny (fail closed)")
	}
}

func TestEOFDenies(t *testing.T) {
	g := New(strings.NewReader(""), &bytes.Buffer{})
	if allowed(g.Approve("shell_exec", "anything", "", "", false)) {
		t.Error("EOF should deny (fail closed)")
	}
}

// ADR-0060 §1: 'N' denies and reads one reason line; the case is
// load-bearing, so a lowercase 'n' must never reach the reason prompt.
func TestDenyWithReason(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("N\n書き込み先が違う — notes.md に\n"), &out)
	approved, fromAllowlist, reason := g.Approve("write_file", "x.txt", "", "", false)
	if approved || fromAllowlist {
		t.Errorf("N = (%v, %v), want a denial", approved, fromAllowlist)
	}
	if reason != "書き込み先が違う — notes.md に" {
		t.Errorf("reason = %q", reason)
	}
	if !strings.Contains(out.String(), "deny reason") {
		t.Errorf("the reason prompt was never shown: %q", out.String())
	}
}

func TestDenyWithReasonEmptyLineIsPlainDeny(t *testing.T) {
	g := New(strings.NewReader("N\n\n"), &bytes.Buffer{})
	approved, _, reason := g.Approve("write_file", "x.txt", "", "", false)
	if approved || reason != "" {
		t.Errorf("N + empty reason = (%v, %q), want plain deny", approved, reason)
	}
}

func TestDenyWithReasonEOFMidQuestionDenies(t *testing.T) {
	g := New(strings.NewReader("N\n"), &bytes.Buffer{})
	approved, _, reason := g.Approve("write_file", "x.txt", "", "", false)
	if approved || reason != "" {
		t.Errorf("EOF at the reason prompt = (%v, %q), want plain deny", approved, reason)
	}
}

func TestAlwaysSkipsSubsequentPrompts(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("a\n"), &out)
	if !allowed(g.Approve("shell_exec", "make build", "", "", false)) {
		t.Fatal("a should approve")
	}
	// Second call: input is exhausted, so only the allowlist can approve.
	if !allowed(g.Approve("shell_exec", "make test", "", "", false)) {
		t.Error("always should skip the prompt for the same tool")
	}
	// Different tool still prompts — and with no input left, it denies.
	if allowed(g.Approve("write_file", "y.txt", "", "", false)) {
		t.Error("allowlist must be per tool name")
	}
}

func TestInvalidInputReprompts(t *testing.T) {
	g := New(strings.NewReader("what\ny\n"), &bytes.Buffer{})
	if !allowed(g.Approve("edit_file", "main.go", "", "", false)) {
		t.Error("invalid input then y should approve")
	}
}

// allowed drops the allowlist flag and deny reason for the assertions
// that only care about the verdict. Tests that care about them read
// all three values.
func allowed(approved, _ bool, _ string) bool { return approved }

// ADR-0048 §1: the gate reports whether the session allowlist answered,
// so the learner can tell one keystroke from many typed decisions.
func TestApproveReportsAllowlistAnswers(t *testing.T) {
	g := New(strings.NewReader("a\ny\n"), io.Discard)

	// The keystroke that registers the allowlist is a decision made here.
	approved, fromAllowlist, _ := g.Approve("shell_exec", "make build", "", "", false)
	if !approved || fromAllowlist {
		t.Errorf("the 'a' keystroke = (%v, %v), want (true, false)", approved, fromAllowlist)
	}
	// The next call is answered by the allowlist, with nobody looking.
	approved, fromAllowlist, _ = g.Approve("shell_exec", "make test", "", "", false)
	if !approved || !fromAllowlist {
		t.Errorf("the allowlist answer = (%v, %v), want (true, true)", approved, fromAllowlist)
	}
	// A typed 'y' is the operator's own answer.
	approved, fromAllowlist, _ = g.Approve("write_file", "x.txt", "", "", false)
	if !approved || fromAllowlist {
		t.Errorf("the typed 'y' = (%v, %v), want (true, false)", approved, fromAllowlist)
	}
}

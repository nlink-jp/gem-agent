package approve

import (
	"bytes"
	"strings"
	"testing"
)

func TestApproveYes(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("y\n"), &out)
	if !g.Approve("shell_exec", "rm -rf build", "") {
		t.Error("y should approve")
	}
	if !strings.Contains(out.String(), "shell_exec") || !strings.Contains(out.String(), "rm -rf build") {
		t.Error("prompt should show tool name and detail")
	}
}

func TestEscalationReasonShown(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("n\n"), &out)
	g.Approve("shell_exec", "rm -rf build",
		"auto-approve blocked by rule (always asks): recursive force delete")
	if !strings.Contains(out.String(), "⚠") || !strings.Contains(out.String(), "recursive force delete") {
		t.Errorf("prompt should show why auto-approve escalated: %q", out.String())
	}
}

func TestDenyNo(t *testing.T) {
	g := New(strings.NewReader("n\n"), &bytes.Buffer{})
	if g.Approve("write_file", "x.txt", "") {
		t.Error("n should deny")
	}
}

func TestEmptyLineDenies(t *testing.T) {
	g := New(strings.NewReader("\n"), &bytes.Buffer{})
	if g.Approve("write_file", "x.txt", "") {
		t.Error("bare Enter should deny (fail closed)")
	}
}

func TestEOFDenies(t *testing.T) {
	g := New(strings.NewReader(""), &bytes.Buffer{})
	if g.Approve("shell_exec", "anything", "") {
		t.Error("EOF should deny (fail closed)")
	}
}

func TestAlwaysSkipsSubsequentPrompts(t *testing.T) {
	var out bytes.Buffer
	g := New(strings.NewReader("a\n"), &out)
	if !g.Approve("shell_exec", "make build", "") {
		t.Fatal("a should approve")
	}
	// Second call: input is exhausted, so only the allowlist can approve.
	if !g.Approve("shell_exec", "make test", "") {
		t.Error("always should skip the prompt for the same tool")
	}
	// Different tool still prompts — and with no input left, it denies.
	if g.Approve("write_file", "y.txt", "") {
		t.Error("allowlist must be per tool name")
	}
}

func TestInvalidInputReprompts(t *testing.T) {
	g := New(strings.NewReader("what\ny\n"), &bytes.Buffer{})
	if !g.Approve("edit_file", "main.go", "") {
		t.Error("invalid input then y should approve")
	}
}

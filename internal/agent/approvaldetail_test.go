package agent

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// ADR-0112: what the operator is asked about is the whole call.
func TestFullCallDetailKeepsEveryArgument(t *testing.T) {
	long := strings.Repeat("x", 1000) + "SHELL-TAIL"
	got := FullCallDetail(llm.ToolCall{Name: "shell_exec", Args: map[string]any{"command": long}})
	if got != long {
		t.Errorf("the command was changed: %d bytes", len(got))
	}
	edit := FullCallDetail(llm.ToolCall{Name: "edit_file", Args: map[string]any{
		"new_string": "first\nsecond", "old_string": "OLD", "path": "a/b.go",
	}})
	want := "path=a/b.go\nnew_string:\nfirst\nsecond\nold_string=OLD"
	if edit != want {
		t.Errorf("edit detail = %q, want %q", edit, want)
	}
	if got := FullCallDetail(llm.ToolCall{Name: "x"}); got != "(no arguments)" {
		t.Errorf("no arguments = %q", got)
	}
}

func TestFullCallDetailSaysWhatItLeftOut(t *testing.T) {
	body := strings.Repeat("あ", approvalDetailCap)
	got := FullCallDetail(llm.ToolCall{Name: "write_file", Args: map[string]any{"content": body}})
	if !strings.Contains(got, "more bytes not shown") {
		t.Errorf("the cut is not disclosed: %q", got[len(got)-60:])
	}
	if !utf8.ValidString(got) {
		t.Error("cut inside a character")
	}
}

// The gate gets the whole call; the one-line summary stays one line's worth.
func TestGateReceivesTheFullCall(t *testing.T) {
	long := "make build " + strings.Repeat("y", 1000) + " GATE-TAIL"
	b := &autoBackend{responses: []*llm.Response{{ToolCalls: []llm.ToolCall{shellCall(long)}}, {Content: "done"}}, verdict: okVerdict}
	g := &recordingGate{}
	a, _, _ := newAutoAgent(t, b, g)
	a.SetAutoApprove(false)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(g.asked) != 1 || !strings.Contains(g.asked[0], "GATE-TAIL") {
		t.Errorf("the gate did not get the end of the command: %q", g.asked)
	}
	if d, _ := a.Describe(shellCall(long)); strings.Contains(d, "GATE-TAIL") {
		t.Errorf("the event summary is no longer clipped: %d bytes", len(d))
	}
}

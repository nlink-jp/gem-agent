package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// mockBackend replays scripted responses and archives every request.
type mockBackend struct {
	responses []*llm.Response
	calls     [][]llm.Message
	systems   []string
	toolDefs  [][]llm.ToolDef
}

func (m *mockBackend) ChatStream(ctx context.Context, system string, messages []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	m.calls = append(m.calls, append([]llm.Message(nil), messages...))
	m.systems = append(m.systems, system)
	m.toolDefs = append(m.toolDefs, defs)
	if len(m.responses) == 0 {
		return &llm.Response{Content: "(script exhausted)"}, nil
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	if onText != nil && resp.Content != "" {
		onText(resp.Content)
	}
	return resp, nil
}

type approveAll struct{ asked []string }

func (a *approveAll) Approve(name, detail string) bool {
	a.asked = append(a.asked, name+": "+detail)
	return true
}

type denyAll struct{ asked []string }

func (d *denyAll) Approve(name, detail string) bool {
	d.asked = append(d.asked, name+": "+detail)
	return false
}

func newAgent(t *testing.T, backend llm.Backend, gate Approver, maxTurns int) (*Agent, *tools.Registry) {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/bash", "-c", command)
		}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Backend: backend, Registry: reg, Gate: gate,
		System: "test system", MaxTurns: maxTurns,
	}), reg
}

func TestPlainAnswer(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "こんにちは"}}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)

	var streamed bytes.Buffer
	out, err := a.Run(context.Background(), "hi", func(s string) { streamed.WriteString(s) })
	if err != nil {
		t.Fatal(err)
	}
	if out != "こんにちは" || streamed.String() != "こんにちは" {
		t.Errorf("out=%q streamed=%q", out, streamed.String())
	}
	if a.HistoryLen() != 2 {
		t.Errorf("history = %d, want user+assistant", a.HistoryLen())
	}
	if mb.systems[0] != "test system" {
		t.Error("system prompt not passed through")
	}
	if len(mb.toolDefs[0]) != 5 {
		t.Errorf("tool defs = %d, want 5 built-ins", len(mb.toolDefs[0]))
	}
}

func TestToolCallLoopPreservesSignatures(t *testing.T) {
	sig := []byte("sig-fc")
	mb := &mockBackend{responses: []*llm.Response{
		{
			ToolCalls: []llm.ToolCall{{
				ID: "c1", Name: "write_file",
				Args:             map[string]any{"path": "x.txt", "content": "data"},
				ThoughtSignature: sig,
			}},
			ThoughtPartSigs: [][]byte{[]byte("sig-thought")},
		},
		{Content: "wrote it"},
	}}
	gate := &approveAll{}
	a, reg := newAgent(t, mb, gate, 5)

	out, err := a.Run(context.Background(), "write x.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "wrote it" {
		t.Errorf("out = %q", out)
	}

	// The tool really ran (approval + execution wired through).
	if data, err := os.ReadFile(filepath.Join(reg.ProjectDir(), "x.txt")); err != nil || string(data) != "data" {
		t.Errorf("tool did not run: %v %q", err, data)
	}
	if len(gate.asked) != 1 || !strings.Contains(gate.asked[0], "write_file") {
		t.Errorf("gate.asked = %v", gate.asked)
	}

	// Round 2 must see: user, assistant(with signatures), tool result.
	second := mb.calls[1]
	if len(second) != 3 {
		t.Fatalf("round-2 history = %d messages", len(second))
	}
	asst := second[1]
	if len(asst.ToolCalls) != 1 || !bytes.Equal(asst.ToolCalls[0].ThoughtSignature, sig) {
		t.Error("function-call thought signature lost between rounds")
	}
	if len(asst.ThoughtPartSigs) != 1 {
		t.Error("thought-part signature lost between rounds")
	}
	toolMsg := second[2]
	if toolMsg.Role != llm.RoleTool || toolMsg.ToolName != "write_file" || toolMsg.ToolCallID != "c1" {
		t.Errorf("tool message = %+v", toolMsg)
	}
	if !strings.Contains(toolMsg.Content, "wrote") {
		t.Errorf("tool result = %q", toolMsg.Content)
	}
}

func TestDeniedMutatingCall(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file",
			Args: map[string]any{"path": "x.txt", "content": "data"}}}},
		{Content: "understood"},
	}}
	gate := &denyAll{}
	a, reg := newAgent(t, mb, gate, 5)

	if _, err := a.Run(context.Background(), "write x.txt", nil); err != nil {
		t.Fatal(err)
	}
	// Denied → file must not exist, and the model must see the denial.
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "x.txt")); !os.IsNotExist(err) {
		t.Error("denied tool call still executed")
	}
	toolMsg := mb.calls[1][2]
	if !strings.Contains(toolMsg.Content, "denied") {
		t.Errorf("denial not surfaced to the model: %q", toolMsg.Content)
	}
}

func TestReadOnlyToolSkipsApproval(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "list_files", Args: map[string]any{}}}},
		{Content: "done"},
	}}
	gate := &denyAll{} // would deny if asked
	a, _ := newAgent(t, mb, gate, 5)

	if _, err := a.Run(context.Background(), "list", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("read-only tool should not prompt: %v", gate.asked)
	}
	if strings.Contains(mb.calls[1][2].Content, "denied") {
		t.Error("read-only tool was gated")
	}
}

func TestUnknownToolSurfacesError(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "no_such_tool", Args: map[string]any{}}}},
		{Content: "ok"},
	}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)
	if _, err := a.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mb.calls[1][2].Content, "unknown tool") {
		t.Errorf("unknown tool not surfaced: %q", mb.calls[1][2].Content)
	}
}

func TestMaxTurnsCap(t *testing.T) {
	loop := &llm.Response{ToolCalls: []llm.ToolCall{{ID: "c", Name: "list_files", Args: map[string]any{}}}}
	mb := &mockBackend{responses: []*llm.Response{loop, loop, loop, loop, loop}}
	a, _ := newAgent(t, mb, &approveAll{}, 3)
	_, err := a.Run(context.Background(), "loop forever", nil)
	if err == nil || !strings.Contains(err.Error(), "max turns") {
		t.Fatalf("expected max-turns error, got %v", err)
	}
	if len(mb.calls) != 3 {
		t.Errorf("backend called %d times, want 3", len(mb.calls))
	}
}

func TestResetClearsHistory(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "a"}, {Content: "b"}}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)
	if _, err := a.Run(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	a.Reset()
	if _, err := a.Run(context.Background(), "two", nil); err != nil {
		t.Fatal(err)
	}
	if len(mb.calls[1]) != 1 {
		t.Errorf("after Reset, second run should start fresh: %d messages", len(mb.calls[1]))
	}
}

func TestCallDetail(t *testing.T) {
	shell := llm.ToolCall{Name: "shell_exec", Args: map[string]any{"command": "make build"}}
	if got := CallDetail(shell); got != "make build" {
		t.Errorf("shell detail = %q", got)
	}
	write := llm.ToolCall{Name: "write_file", Args: map[string]any{"path": "a.txt", "content": strings.Repeat("x", 500)}}
	got := CallDetail(write)
	if !strings.Contains(got, "path=a.txt") || len(got) > 400 {
		t.Errorf("write detail = %q", got)
	}
}

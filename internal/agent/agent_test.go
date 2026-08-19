package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/mention"
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

func (a *approveAll) Approve(name, detail, reason string) bool {
	a.asked = append(a.asked, name+": "+detail)
	return true
}

type denyAll struct{ asked []string }

func (d *denyAll) Approve(name, detail, reason string) bool {
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
	if len(mb.toolDefs[0]) != 6 {
		t.Errorf("tool defs = %d, want 6 built-ins", len(mb.toolDefs[0]))
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

// TestEmptyResponseDoesNotPoisonHistory is the field-reported bug: a
// model turn with neither text nor tool calls was stored, and every
// later request then carried an empty part — a 400 that repeated until
// the session was cleared. The turn must fail loudly and leave nothing
// behind, so the next message still works.
func TestEmptyResponseDoesNotPoisonHistory(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{}, // empty: no content, no tool calls
		{Content: "recovered"},
	}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)

	_, err := a.Run(context.Background(), "first", nil)
	if err == nil {
		t.Fatal("an empty response should be reported as an error")
	}
	if !strings.Contains(err.Error(), "empty response") {
		t.Errorf("error should name the cause: %v", err)
	}
	for i, m := range a.history {
		if m.Role == llm.RoleAssistant && m.Content == "" && len(m.ToolCalls) == 0 {
			t.Fatalf("history[%d] stored an empty assistant turn", i)
		}
	}

	// The session stays usable.
	out, err := a.Run(context.Background(), "second", nil)
	if err != nil || out != "recovered" {
		t.Fatalf("next turn failed after an empty response: %q %v", out, err)
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

// TestMentionAttachmentsAreNonceWrapped: the operator chose the file,
// but not what is inside it — attached content must reach the model as
// isolated data, exactly like tool output.
func TestMentionAttachmentsAreNonceWrapped(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "ok"}}}
	a, reg := newAgent(t, mb, &approveAll{}, 5)

	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "notes.md"), []byte("SECRET-MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}
	var attached []string
	var problems []string
	a.onAttach = func(atts []mention.Attachment, probs []mention.Problem) {
		for _, at := range atts {
			attached = append(attached, at.Ref)
		}
		for _, p := range probs {
			problems = append(problems, p.Ref+": "+p.Reason)
		}
	}

	if _, err := a.Run(context.Background(), "@notes.md と @missing.txt を見て", nil); err != nil {
		t.Fatal(err)
	}
	if len(attached) != 1 || attached[0] != "notes.md" {
		t.Errorf("attached = %v", attached)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "not found") {
		t.Errorf("problems = %v", problems)
	}

	sent := mb.calls[0][0].Content
	if !strings.Contains(sent, "SECRET-MARKER") {
		t.Fatal("attachment content never reached the model")
	}
	re := regexp.MustCompile(`<(tool_output_[0-9a-f]{32})>[^<]*SECRET-MARKER`)
	if !re.MatchString(sent) {
		t.Errorf("attachment not nonce-wrapped: %q", sent)
	}
	if !strings.Contains(sent, "@notes.md と @missing.txt を見て") {
		t.Error("the operator's own text must be sent unmodified")
	}
	// Stored history keeps the raw attachment (the tag is per-call).
	if a.history[0].Attachments[0].Content != "SECRET-MARKER" {
		t.Error("history should keep the raw attachment")
	}
}

func TestToolResultsNonceWrapped(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "list_files", Args: map[string]any{}}}},
		{Content: "done"},
	}}
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/bash", "-c", command)
		}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{
		Backend: mb, Registry: reg, Gate: &approveAll{},
		System: "sys with tag <{{DATA_TAG}}>", MaxTurns: 5,
	})
	if _, err := a.Run(context.Background(), "list", nil); err != nil {
		t.Fatal(err)
	}

	// Round 2's tool message must be wrapped in this round's nonce tag,
	// and the same tag must appear expanded in the system prompt.
	re := regexp.MustCompile(`^<(tool_output_[0-9a-f]{32})>(?s).*</(tool_output_[0-9a-f]{32})>$`)
	toolMsg := mb.calls[1][2]
	m := re.FindStringSubmatch(toolMsg.Content)
	if m == nil {
		t.Fatalf("tool result not nonce-wrapped: %q", toolMsg.Content)
	}
	if m[1] != m[2] {
		t.Fatalf("open/close tags differ: %s vs %s", m[1], m[2])
	}
	if !strings.Contains(mb.systems[1], "<"+m[1]+">") {
		t.Error("system prompt does not reference the same turn tag")
	}
	if strings.Contains(mb.systems[1], "{{DATA_TAG}}") {
		t.Error("placeholder not expanded")
	}
	// Fresh tag per LLM call: round 1 and round 2 system prompts must
	// carry different tag names.
	if mb.systems[0] == mb.systems[1] {
		t.Error("tag must be regenerated for every LLM call")
	}
}

// TestAddContextFeedsNextTurn: a !-shell note lands in history so the
// next backend call sees it before the new user input.
func TestAddContextFeedsNextTurn(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "ok"}}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)

	a.AddContext("I ran this shell command myself:\n$ git status\n\nOutput:\nclean")
	if _, err := a.Run(context.Background(), "何か問題ある？", nil); err != nil {
		t.Fatal(err)
	}
	first := mb.calls[0]
	if len(first) != 2 {
		t.Fatalf("history = %d messages, want context note + user input", len(first))
	}
	if first[0].Role != llm.RoleUser || !strings.Contains(first[0].Content, "git status") {
		t.Errorf("context note missing or malformed: %+v", first[0])
	}
	if first[1].Content != "何か問題ある？" {
		t.Errorf("user input must follow the note: %+v", first[1])
	}
}

func TestOnUsageReportsEachRound(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{
			ToolCalls:    []llm.ToolCall{{ID: "c1", Name: "list_files", Args: map[string]any{}}},
			PromptTokens: 100, OutputTokens: 20,
		},
		{Content: "done", PromptTokens: 150, OutputTokens: 30},
	}}
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/bash", "-c", command)
		}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got [][2]int
	a := New(Options{
		Backend: mb, Registry: reg, Gate: &approveAll{},
		System: "s", MaxTurns: 5,
		OnUsage: func(p, o int) { got = append(got, [2]int{p, o}) },
	})
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	want := [][2]int{{100, 20}, {150, 30}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("usage reports = %v, want %v", got, want)
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

// ADR-0010: a skill body is an instruction file the operator installed —
// wrapping it as data while the system prompt forbids following data
// would leave every skill half-inert. Everything else stays wrapped.
func TestInstructionToolResultsAreNotWrapped(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{
			{ID: "c1", Name: "load_skill", Args: map[string]any{"name": "s"}},
			{ID: "c2", Name: "read_file", Args: map[string]any{"path": "x.txt"}},
		}},
		{Content: "done"},
	}}
	_, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "x.txt"), []byte("FILE DATA"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&tools.Tool{
		Name: "load_skill", Description: "d", Parameters: map[string]any{},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return "SKILL INSTRUCTIONS", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "s",
		MaxTurns: 5, InstructionTools: []string{"load_skill"}})

	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	// The second request carries both tool results.
	last := mb.calls[len(mb.calls)-1]
	var skillMsg, fileMsg string
	for _, m := range last {
		switch m.ToolName {
		case "load_skill":
			skillMsg = m.Content
		case "read_file":
			fileMsg = m.Content
		}
	}
	if skillMsg != "SKILL INSTRUCTIONS" {
		t.Errorf("skill result was wrapped or altered: %q", skillMsg)
	}
	if !strings.Contains(fileMsg, "FILE DATA") || fileMsg == "FILE DATA" {
		t.Errorf("read_file result must stay nonce-wrapped: %q", fileMsg)
	}
	// The stored history keeps the raw content either way (wrapping is
	// send-time), so resume fidelity is unaffected.
}

package agent

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// newAutoAgentWithTool builds the auto agent with one extra tool
// registered before New — the Agent caches tool declarations at
// construction (ADR-0030), so registration order matters.
func newAutoAgentWithTool(t *testing.T, b *autoBackend, extra *tools.Tool) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(extra); err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Backend: b, Registry: reg, Gate: &recordingGate{}, System: "sys", MaxTurns: 5,
		AutoApprove: true,
	})
}

func mcpLookupTool(desc string) *tools.Tool {
	return &tools.Tool{
		Name:        "mcp__srv__lookup",
		Description: desc,
		Mutating:    true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return "ok", nil
		},
	}
}

func mcpCall() llm.ToolCall {
	return llm.ToolCall{ID: "m", Name: "mcp__srv__lookup", Args: map[string]any{"q": "203.0.113.7"}}
}

func mcpScript(verdict string) *autoBackend {
	return &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{mcpCall()}},
			{Content: "done"},
		},
		verdict: verdict,
	}
}

// ADR-0046 §1–2: an MCP call's evaluation carries the tool's
// self-description inside the nonce wrap, labelled for what it is, and
// the prompt gains the claim-not-fact addendum.
func TestRiskEvalCarriesMCPDescription(t *testing.T) {
	b := mcpScript(okVerdict)
	a := newAutoAgentWithTool(t, b, mcpLookupTool("Reads a locally cached list, fully offline."))
	if _, err := a.Run(context.Background(), "IP を調べて", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(b.evals))
	}
	payload := b.evals[0]
	want := "tool self-description (published by the MCP server): Reads a locally cached list, fully offline."
	if !strings.Contains(payload, want) {
		t.Errorf("description missing from payload: %q", payload)
	}
	// Inside the wrap: the server's text must reach the reviewer as
	// data, never as directives.
	open := strings.Index(payload, "<proposed_call_")
	desc := strings.Index(payload, "tool self-description")
	if open < 0 || desc < open {
		t.Errorf("description not inside the nonce wrap: %q", payload)
	}
	if !strings.Contains(b.evalSystems[0], "tool self-description") {
		t.Errorf("prompt lacks the description addendum: %q", b.evalSystems[0])
	}
}

// ADR-0046 §1: built-in tools are excluded — shell_exec has a registry
// description too, so this pins the mcp__ prefix scoping, and the
// non-MCP prompt stays free of the addendum.
func TestRiskEvalDescriptionOnlyForMCP(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if _, err := a.Run(context.Background(), "ビルドして", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 1 {
		t.Fatalf("evals = %d, want 1", len(b.evals))
	}
	if strings.Contains(b.evals[0], "tool self-description") {
		t.Errorf("built-in call carries a self-description section: %q", b.evals[0])
	}
	if strings.Contains(b.evalSystems[0], "tool self-description") {
		t.Error("non-MCP prompt is not the byte-identical base prompt")
	}
}

// An oversized description is clipped with the truncation named — the
// side call stays cheap and the clip must not masquerade as the whole
// text.
func TestRiskEvalClipsDescription(t *testing.T) {
	long := strings.Repeat("あ", riskDescriptionCap+200)
	b := mcpScript(okVerdict)
	a := newAutoAgentWithTool(t, b, mcpLookupTool(long))
	if _, err := a.Run(context.Background(), "調べて", nil); err != nil {
		t.Fatal(err)
	}
	payload := b.evals[0]
	if strings.Contains(payload, long) {
		t.Error("description not clipped")
	}
	if !strings.Contains(payload, "[clipped]") {
		t.Error("clip not disclosed")
	}
}

// A server that publishes no description gets the conventional
// evaluation: no section, no addendum.
func TestRiskEvalEmptyDescriptionOmitsSection(t *testing.T) {
	b := mcpScript(okVerdict)
	a := newAutoAgentWithTool(t, b, mcpLookupTool("   "))
	if _, err := a.Run(context.Background(), "調べて", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.evals[0], "tool self-description") {
		t.Errorf("blank description still produced a section: %q", b.evals[0])
	}
	if strings.Contains(b.evalSystems[0], "tool self-description") {
		t.Error("blank description still produced the addendum")
	}
}

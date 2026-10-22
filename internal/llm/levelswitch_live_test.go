//go:build live

package llm

import (
	"context"
	"os"
	"testing"
)

// Measures the premise of ADR-0098: a conversation whose earlier rounds
// were produced at one thinking level, replayed with their thought
// signatures, is accepted at another level — across a tool round (the
// function-call signature) and across a text-only turn. Each request is
// one backend at one level, which is all a live switch changes.
// Run: GEM_TEST_PROJECT=... GEM_TEST_MODEL=... go test -tags live -run ThinkingLevelSwitch ./internal/llm/
func TestThinkingLevelSwitchMidConversation(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	model := os.Getenv("GEM_TEST_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}
	at := func(level string) *Vertex {
		v, err := NewVertex(ctx, project, "global", model, "off", level, false)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	tools := []ToolDef{{
		Name:        "lookup_price",
		Description: "Returns the unit price in yen of a named item.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"item": map[string]any{"type": "string"}},
			"required":   []string{"item"},
		},
	}}
	hist := []Message{{Role: RoleUser, Content: "りんご 3 個の値段を lookup_price で調べて合計を答えて。"}}

	// Round 1 at high: expect a function call carrying a signature.
	r1, err := at("high").ChatStream(ctx, "", hist, tools, nil)
	if err != nil {
		t.Fatalf("round 1 (high): %v", err)
	}
	if len(r1.ToolCalls) == 0 {
		t.Skipf("round 1 made no tool call (%q) — nothing to replay across the switch", r1.Content)
	}
	t.Logf("round 1 (high): call=%s sig=%d bytes thoughts=%d", r1.ToolCalls[0].Name, len(r1.ToolCalls[0].ThoughtSignature), r1.ThoughtTokens)
	hist = append(hist,
		Message{Role: RoleAssistant, Content: r1.Content, ToolCalls: r1.ToolCalls, ThoughtPartSigs: r1.ThoughtPartSigs, TextPartSig: r1.TextPartSig},
		Message{Role: RoleTool, ToolName: r1.ToolCalls[0].Name, ToolCallID: r1.ToolCalls[0].ID, Content: `{"item":"りんご","yen":120}`})

	// Round 2 at low: the high-level signature replayed at another level.
	r2, err := at("low").ChatStream(ctx, "", hist, tools, nil)
	if err != nil {
		t.Fatalf("round 2 (low, replaying high's signature) REJECTED: %v", err)
	}
	t.Logf("round 2 (low): %q thoughts=%d (accepted)", r2.Content, r2.ThoughtTokens)
	hist = append(hist,
		Message{Role: RoleAssistant, Content: r2.Content, ToolCalls: r2.ToolCalls, ThoughtPartSigs: r2.ThoughtPartSigs, TextPartSig: r2.TextPartSig},
		Message{Role: RoleUser, Content: "では 5 個なら? 数字だけ。"})

	// Turn 2 back at high, then at the model's default: both directions.
	r3, err := at("high").ChatStream(ctx, "", hist, tools, nil)
	if err != nil {
		t.Fatalf("turn 2 (high, replaying low's turn) REJECTED: %v", err)
	}
	t.Logf("turn 2 (high): %q thoughts=%d (accepted)", r3.Content, r3.ThoughtTokens)
	r4, err := at("").ChatStream(ctx, "", hist, tools, nil)
	if err != nil {
		t.Fatalf("turn 2 (default) REJECTED: %v", err)
	}
	t.Logf("turn 2 (default): %q thoughts=%d (accepted)", r4.Content, r4.ThoughtTokens)

	// minimal: model-dependent support (ADR-0025 §4) — logged, not failed.
	if r5, err := at("minimal").ChatStream(ctx, "", hist, tools, nil); err != nil {
		t.Logf("turn 2 (minimal): rejected — %v", err)
	} else {
		t.Logf("turn 2 (minimal): %q thoughts=%d (accepted)", r5.Content, r5.ThoughtTokens)
	}
}

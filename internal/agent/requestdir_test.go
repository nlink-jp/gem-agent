package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// requestDirTool stands in for cmd's request_directory: what matters
// here is that the verdict, not the tool, keeps it the operator's.
func requestDirTool(ran *int) *tools.Tool {
	return &tools.Tool{
		Name: "request_directory", Description: "d", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			*ran++
			return "added", nil
		},
	}
}

func requestDirCall(id string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "request_directory", Args: map[string]any{"path": "/Users/op/notes", "access": "write", "reason": "r"}}
}

// ADR-0099 §4: the model's request is the operator's alone in every
// mode — the session allowlist never answers it, a "never" policy (and
// the one-shot --allow grant it stands for) does not lift it, and
// auto-approval never sends it to the model tier.
func TestRequestDirectoryStaysOperatorOnly(t *testing.T) {
	ran := 0
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{requestDirCall("c1")}},
		{ToolCalls: []llm.ToolCall{requestDirCall("c2")}},
		{Content: "done"},
	}}
	gate := &allowlistGate{}
	a, reg := newAgent(t, mb, gate, 6)
	if err := reg.Register(requestDirTool(&ran)); err != nil {
		t.Fatal(err)
	}
	pol, _, err := policy.Build(map[string]string{"request_directory": "never"}, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	a.SetPolicy(pol)
	if _, err := a.Run(context.Background(), "need notes", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.allowlisted) != 0 {
		t.Errorf("the allowlist answered a request: %v", gate.allowlisted)
	}
	if len(gate.prompted) != 2 || !strings.Contains(gate.prompted[0], "request_directory") {
		t.Errorf("each request must prompt under a never policy: %v", gate.prompted)
	}
	d := a.decide(requestDirCall("x"))
	if !d.Verdict.OperatorOnly || !d.Floor() {
		t.Errorf("decision = %+v, want the operator-only floor", d)
	}

	b := &autoBackend{verdict: `{"approve": true, "confidence": 0.99, "reason": "ok"}`}
	auto := newAutoAgentWithTool(t, b, requestDirTool(&ran))
	ad := auto.decideAuto(context.Background(), requestDirCall("y"))
	if ad.Approved || ad.ModelConsulted || len(b.evals) != 0 {
		t.Errorf("auto mode decided a request itself: %+v (evals %d)", ad, len(b.evals))
	}
}

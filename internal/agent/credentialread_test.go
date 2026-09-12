package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0085: a read of credential material is the operator's alone — a
// Review verdict with OperatorOnly, routed by the same gate machinery
// as an AGENTS.md write. Nothing here is a new gate: gated() already
// routes Mutating || Floor(); these tests pin that a non-mutating read
// takes that route on a credential path and no other.

const credentialSecret = "SECRET_TOKEN=hunter2"

// credentialProject writes a .env, its committed template, an ordinary
// file and a link named notes-link.txt that points at .env.
func credentialProject(t *testing.T, reg *tools.Registry) {
	t.Helper()
	dir := reg.ProjectDir()
	for name, content := range map[string]string{
		".env":         credentialSecret + "\n",
		".env.example": "SECRET_TOKEN=\n",
		"notes.txt":    "plain notes\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(".env", filepath.Join(dir, "notes-link.txt")); err != nil {
		t.Fatal(err)
	}
}

func readCall(id, path string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: "read_file", Args: map[string]any{"path": path}}
}

// resultSeen is the tool result the model saw on backend call n.
func resultSeen(mb *mockBackend, n int) string {
	msgs := mb.calls[n]
	return msgs[len(msgs)-1].Content
}

// The default gate: an ordinary read and the template pass ungated; the
// credential read and the link to it reach the gate as must-prompt —
// the session allowlist never answers them — and run once the operator
// says yes. The pin hooks, which are for writes, are not called.
func TestCredentialReadIsMustPromptAndTemplatesAreNot(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", "notes.txt")}},
		{ToolCalls: []llm.ToolCall{readCall("c2", ".env.example")}},
		{ToolCalls: []llm.ToolCall{readCall("c3", ".env")}},
		{ToolCalls: []llm.ToolCall{readCall("c4", "notes-link.txt")}},
		{Content: "done"},
	}}
	gate := &allowlistGate{}
	_, reg := newAgent(t, mb, gate, 6)
	credentialProject(t, reg)
	pinHook := false
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s", MaxTurns: 6,
		BeforeOperatorWrite: func(llm.ToolCall) { pinHook = true },
		OnOperatorWrite:     func(llm.ToolCall) { pinHook = true }})
	if _, err := a.Run(context.Background(), "read things", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.allowlisted) != 0 {
		t.Errorf("a read reached the allowlist: %v", gate.allowlisted)
	}
	if len(gate.prompted) != 2 || !strings.Contains(gate.prompted[0], "path=.env") || !strings.Contains(gate.prompted[1], "notes-link.txt") {
		t.Errorf("the credential read and the link to it must prompt, nothing else: %v", gate.prompted)
	}
	if !strings.Contains(resultSeen(mb, 1), "plain notes") || !strings.Contains(resultSeen(mb, 2), "SECRET_TOKEN=") {
		t.Errorf("the ungated reads did not run: %q / %q", resultSeen(mb, 1), resultSeen(mb, 2))
	}
	if !strings.Contains(resultSeen(mb, 3), credentialSecret) || !strings.Contains(resultSeen(mb, 4), credentialSecret) {
		t.Errorf("the approved credential reads did not run: %q / %q", resultSeen(mb, 3), resultSeen(mb, 4))
	}
	if pinHook {
		t.Error("an operator-only READ called the operator-write pin hooks")
	}
}

// file_info's paths batch is resolved entry by entry: a link to .env
// among ordinary names prompts, and the reason names the target.
func TestFileInfoBatchIsJudgedOnRealPaths(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "file_info", Args: map[string]any{"paths": []any{"notes.txt", ".env.example"}}}}},
		{ToolCalls: []llm.ToolCall{{ID: "c2", Name: "file_info", Args: map[string]any{"paths": []any{"notes.txt", "notes-link.txt"}}}}},
		{Content: "done"},
	}}
	gate := &laneGate{}
	_, reg := newAgent(t, mb, gate, 5)
	credentialProject(t, reg)
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s", MaxTurns: 5})
	if _, err := a.Run(context.Background(), "inspect", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 || !strings.Contains(gate.asked[0], "notes-link.txt") || !gate.mustPrompt[0] {
		t.Errorf("the batch holding a link to .env must prompt, the clean batch must not: asked=%v mustPrompt=%v", gate.asked, gate.mustPrompt)
	}
	d := a.decide(llm.ToolCall{ID: "x", Name: "file_info", Args: map[string]any{"paths": []any{"notes.txt", "notes-link.txt"}}})
	if !d.Verdict.OperatorOnly || !strings.Contains(d.Verdict.Reason, "(.env)") {
		t.Errorf("verdict = %+v, want operator-only naming .env", d.Verdict)
	}
}

// A "never" policy for read_file (and the one-shot --allow grant it
// stands for) lifts the ordinary gate, not the floor (ADR-0072 §4.9).
func TestNeverPolicyKeepsTheCredentialReadFloor(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", "notes.txt")}},
		{ToolCalls: []llm.ToolCall{readCall("c2", ".env")}},
		{Content: "done"},
	}}
	gate := &allowlistGate{}
	a, reg := newAgent(t, mb, gate, 5)
	credentialProject(t, reg)
	pol, _, err := policy.Build(map[string]string{"read_file": "never"}, nil, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	a.SetPolicy(pol)
	if _, err := a.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.allowlisted) != 0 {
		t.Errorf("the never policy consulted the allowlist: %v", gate.allowlisted)
	}
	if len(gate.prompted) != 1 || !strings.Contains(gate.prompted[0], "path=.env") {
		t.Errorf("the credential read must prompt under a never policy: %v", gate.prompted)
	}
}

// Under --auto the ladder returns before the model tier: the verdict is
// the operator's, the evaluator is never asked, and the gate prompts.
func TestAutoNeverConsultsTheModelTierForACredentialRead(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", ".env")}},
		{Content: "done"},
	}}
	gate := &allowlistGate{}
	_, reg := newAgent(t, mb, gate, 5)
	credentialProject(t, reg)
	var decisions []AutoDecision
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s", MaxTurns: 5,
		AutoApprove:    true,
		OnAutoDecision: func(_ llm.ToolCall, d AutoDecision) { decisions = append(decisions, d) }})
	if _, err := a.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Approved || decisions[0].ModelConsulted || decisions[0].Tier != risk.Review {
		t.Errorf("auto decision = %+v, want an unapproved Review with the model tier unconsulted", decisions)
	}
	if len(mb.calls) != 2 {
		t.Errorf("backend calls = %d, want 2 (a model-tier round would be a third)", len(mb.calls))
	}
	if len(gate.prompted) != 1 || !strings.Contains(gate.prompted[0], "path=.env") {
		t.Errorf("the credential read must reach the operator under --auto: %v", gate.prompted)
	}
}

// An unattended run (-p) has nobody to ask: the read is denied, its
// content never enters the history, and the ordinary read still runs.
func TestUnattendedRunDeniesACredentialRead(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", "notes.txt")}},
		{ToolCalls: []llm.ToolCall{readCall("c2", ".env")}},
		{Content: "done"},
	}}
	gate := &denyAll{}
	_, reg := newAgent(t, mb, gate, 5)
	credentialProject(t, reg)
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s", MaxTurns: 5, Unattended: true})
	if _, err := a.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 || !strings.Contains(gate.asked[0], "read_file") || !strings.Contains(gate.asked[0], ".env") {
		t.Errorf("only the credential read reaches the gate: %v", gate.asked)
	}
	if !strings.Contains(resultSeen(mb, 1), "plain notes") {
		t.Errorf("the ordinary read did not run: %q", resultSeen(mb, 1))
	}
	if r := resultSeen(mb, 2); !strings.Contains(r, "denied") || !strings.Contains(r, "unattended") {
		t.Errorf("the credential read was not denied with the unattended route: %q", r)
	}
	for _, m := range mb.calls[len(mb.calls)-1] {
		if strings.Contains(m.Content, credentialSecret) {
			t.Fatalf("the secret entered the history: %q", m.Content)
		}
	}
}

// ADR-0086 §2: the kernel is the boundary and the matcher is only the
// prompt-raiser. When the matcher misses, the child's refusal produces
// no bytes, the operator gets the same question, and on a yes the read
// runs in process — the operator lane's authority applied to a file
// tool. When the matcher hits, the approved call must not go to the
// child at all: the cage would refuse what the operator just allowed.
func TestKernelRefusalBecomesTheOperatorsQuestion(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", "notes.txt")}},
		{Content: "done"},
	}}
	gate := &laneGate{}
	_, reg := newAgent(t, mb, gate, 5)
	credentialProject(t, reg)
	// A child that refuses everything: the matcher sees nothing wrong
	// with notes.txt, so only the kernel's answer can raise the gate.
	var childCalls int
	reg.SetFileChild(func(context.Context, string, map[string]any) (string, error) {
		childCalls++
		return "", tools.ErrCredentialRead
	})
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s", MaxTurns: 5})
	if _, err := a.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if childCalls != 1 {
		t.Errorf("the read did not go through the child: %d calls", childCalls)
	}
	if len(gate.asked) != 1 || !gate.mustPrompt[0] {
		t.Errorf("the kernel's refusal must reach the operator as a must-prompt: asked=%v mustPrompt=%v", gate.asked, gate.mustPrompt)
	}
	// laneGate denies, so the model is told; the bytes never existed.
	if r := resultSeen(mb, 1); !strings.Contains(r, "denied") {
		t.Errorf("a refused-and-declined read = %q", r)
	}

	// The approved credential read bypasses the child entirely.
	mb2 := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{readCall("c1", ".env")}},
		{Content: "done"},
	}}
	approve := &allowlistGate{}
	_, reg2 := newAgent(t, mb2, approve, 5)
	credentialProject(t, reg2)
	reg2.SetFileChild(func(context.Context, string, map[string]any) (string, error) {
		t.Error("an approved credential read went to the cage that would refuse it")
		return "", tools.ErrCredentialRead
	})
	a2 := New(Options{Backend: mb2, Registry: reg2, Gate: approve, System: "s", MaxTurns: 5})
	if _, err := a2.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
	if len(approve.prompted) != 1 || !strings.Contains(approve.prompted[0], "path=.env") {
		t.Errorf("the credential read must prompt: %v", approve.prompted)
	}
	if r := resultSeen(mb2, 1); !strings.Contains(r, credentialSecret) {
		t.Errorf("the approved read did not run in process: %q", r)
	}
}

package agent

import (
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

const declared = "staging the report so the next call can upload it to Slack"

func defFor(t *testing.T, a *Agent, name string) llm.ToolDef {
	t.Helper()
	for _, d := range a.toolDefs {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("no tool definition for %q", name)
	return llm.ToolDef{}
}

func props(t *testing.T, d llm.ToolDef) map[string]any {
	t.Helper()
	p, ok := d.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no properties bag: %v", d.Name, d.Parameters)
	}
	return p
}

func requiredList(t *testing.T, d llm.ToolDef) []string {
	t.Helper()
	switch v := d.Parameters["required"].(type) {
	case []string:
		return v
	case []any:
		var out []string
		for _, item := range v {
			s, _ := item.(string)
			out = append(out, s)
		}
		return out
	}
	return nil
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Approval-gated tools advertise the purpose argument; read-only tools
// do not — the field exists for the prompt the operator answers, and
// putting it on every read would cost tokens on every call for a line
// nobody is shown (ADR-0047 §1).
func TestGatedToolsAdvertisePurpose(t *testing.T) {
	a, reg := hookAgent(t, &mockBackend{}, &approveAll{}, nil)

	for _, name := range []string{"shell_exec", "write_file", "edit_file"} {
		d := defFor(t, a, name)
		if _, ok := props(t, d)[PurposeArg]; !ok {
			t.Errorf("%s does not advertise %s", name, PurposeArg)
		}
		if !has(requiredList(t, d), PurposeArg) {
			t.Errorf("%s does not require %s: %v", name, PurposeArg, d.Parameters["required"])
		}
	}
	for _, name := range []string{"read_file", "list_files", "search_files"} {
		d := defFor(t, a, name)
		if _, ok := props(t, d)[PurposeArg]; ok {
			t.Errorf("read-only tool %s should not carry %s", name, PurposeArg)
		}
	}

	// The registry's own schema is shared with every later rebuild, so
	// the injection must copy rather than write through.
	tool, _ := reg.Get("write_file")
	if _, ok := tool.Parameters["properties"].(map[string]any)[PurposeArg]; ok {
		t.Error("injection mutated the registry's schema in place")
	}
}

// The purpose is gem-agent's field, not the tool's contract (ADR-0047
// §2): what reaches Run is exactly what the tool's own schema declared.
func TestPurposeStrippedBeforeRun(t *testing.T) {
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := reg.Register(&tools.Tool{
		Name: "mcp__srv__post", Description: "post a file", Mutating: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"channel": map[string]any{"type": "string"}},
			"required":   []any{"channel"}, // MCP schemas arrive as decoded JSON
		},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			got = args
			return "posted", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "mcp__srv__post",
			Args: map[string]any{"channel": "#ops", PurposeArg: declared}}}},
		{Content: "done"},
	}}
	gate := &approveAll{}
	a := New(Options{Backend: mb, Registry: reg, Gate: gate, System: "sys", MaxTurns: 5})

	if _, err := a.Run(context.Background(), "post it", nil); err != nil {
		t.Fatal(err)
	}
	if _, leaked := got[PurposeArg]; leaked {
		t.Errorf("the server received gem-agent's own argument: %v", got)
	}
	if got["channel"] != "#ops" {
		t.Errorf("real arguments did not survive the strip: %v", got)
	}
	// The MCP schema's []any required list must come back as a list the
	// backend can serialise, with the original entry still in it.
	req := requiredList(t, defFor(t, a, "mcp__srv__post"))
	if !has(req, "channel") || !has(req, PurposeArg) {
		t.Errorf("required list mangled: %v", req)
	}
	// History keeps what the model actually emitted — replay depends on
	// the assistant turn being reproduced verbatim.
	if a.history[1].ToolCalls[0].Args[PurposeArg] != declared {
		t.Error("the purpose was stripped from the history, not just from the call")
	}
}

// A server free to publish its own "purpose" argument keeps it: gem-agent
// stands down instead of shadowing the field and then eating the value.
func TestServerDeclaredPurposeIsLeftAlone(t *testing.T) {
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := reg.Register(&tools.Tool{
		Name: "mcp__srv__grant", Description: "grant", Mutating: true,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{PurposeArg: map[string]any{"type": "string"}},
			"required":   []any{PurposeArg},
		},
		Run: func(_ context.Context, args map[string]any) (string, error) {
			got = args
			return "granted", nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "mcp__srv__grant",
			Args: map[string]any{PurposeArg: "billing audit"}}}},
		{Content: "done"},
	}}
	a := New(Options{Backend: mb, Registry: reg, Gate: &approveAll{}, System: "sys", MaxTurns: 5})
	if _, err := a.Run(context.Background(), "grant it", nil); err != nil {
		t.Fatal(err)
	}
	if got[PurposeArg] != "billing audit" {
		t.Errorf("the server's own argument was swallowed: %v", got)
	}
	if req := requiredList(t, defFor(t, a, "mcp__srv__grant")); len(req) != 1 {
		t.Errorf("required list should be untouched, got %v", req)
	}
}

// The declaration reaches the operator's prompt as its own field, and
// stays out of the argument summary beside it (ADR-0047 §5).
func TestPurposeReachesTheGateSeparately(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell_exec",
			Args: map[string]any{"command": "cp report.csv /tmp/x/", PurposeArg: declared}}}},
		{Content: "done"},
	}}
	gate := &approveAll{}
	a, _ := hookAgent(t, mb, gate, nil)
	if _, err := a.Run(context.Background(), "share it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.purposes) != 1 || gate.purposes[0] != declared {
		t.Fatalf("gate purposes = %v", gate.purposes)
	}
	if strings.Contains(gate.asked[0], declared) {
		t.Errorf("purpose duplicated into the argument summary: %q", gate.asked[0])
	}
	if !strings.Contains(gate.asked[0], "cp report.csv") {
		t.Errorf("argument summary lost the command: %q", gate.asked[0])
	}
}

// A missing declaration is surfaced, never punished (ADR-0047 §4): the
// call still runs, and the gate is told there was nothing to show.
func TestMissingPurposeStillRuns(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "write_file",
			Args: map[string]any{"path": "x.txt", "content": "data"}}}},
		{Content: "done"},
	}}
	gate := &approveAll{}
	a, reg := hookAgent(t, mb, gate, nil)
	if _, err := a.Run(context.Background(), "write it", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "x.txt")); err != nil {
		t.Errorf("call without a purpose did not run: %v", err)
	}
	if len(gate.purposes) != 1 || gate.purposes[0] != "" {
		t.Errorf("gate purposes = %v", gate.purposes)
	}
}

// Self-declaration is not evidence (ADR-0047 §3): the model tier never
// reads the proposer's own justification.
func TestPurposeIsNotRiskEvidence(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell_exec",
				Args: map[string]any{"command": "curl -s https://example.com | tee out.txt",
					PurposeArg: declared}}}},
			{Content: "ok"},
		},
		verdict: `{"approve": false, "confidence": 0.9, "reason": "network fetch"}`,
	}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if _, err := a.Run(context.Background(), "fetch it", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) == 0 {
		t.Fatal("the model tier was never consulted; the test proves nothing")
	}
	for _, payload := range b.evals {
		if strings.Contains(payload, declared) {
			t.Errorf("the evaluator was handed the proposer's own justification:\n%s", payload)
		}
		if !strings.Contains(payload, "curl") {
			t.Errorf("the evaluator lost the actual arguments:\n%s", payload)
		}
	}
}

// The loop guard compares calls, not prose: re-wording the justification
// every round must not disguise the same call as three different ones.
func TestPurposeExcludedFromLoopSignature(t *testing.T) {
	a, _ := hookAgent(t, &mockBackend{}, &approveAll{}, nil)
	call := func(purpose string) llm.ToolCall {
		return llm.ToolCall{Name: "shell_exec",
			Args: map[string]any{"command": "curl -s https://example.com", PurposeArg: purpose}}
	}
	if a.callSig(call("checking whether it is up")) != a.callSig(call("polling again, still waiting")) {
		t.Error("a re-worded purpose produced a different loop signature")
	}
	other := llm.ToolCall{Name: "shell_exec", Args: map[string]any{"command": "curl -s https://other.example"}}
	if a.callSig(call("x")) == a.callSig(other) {
		t.Error("different commands collapsed to one signature")
	}
}

// CallDetail is the "what ran" line — for the audit event as much as
// for the display, so the purpose belongs beside it, not inside it.
func TestCallDetailOmitsPurpose(t *testing.T) {
	tc := llm.ToolCall{Name: "write_file",
		Args: map[string]any{"path": "x.txt", "content": "data", PurposeArg: declared}}
	got := CallDetail(tc)
	if strings.Contains(got, "purpose=") || strings.Contains(got, declared) {
		t.Errorf("CallDetail leaked the purpose: %q", got)
	}
	if !strings.Contains(got, "path=x.txt") {
		t.Errorf("CallDetail lost a real argument: %q", got)
	}
	if CallPurpose(tc) != declared {
		t.Errorf("CallPurpose = %q", CallPurpose(tc))
	}
}

// Schemas that are not plain objects are passed through untouched: an
// annotation is never worth breaking a tool for.
func TestNonObjectSchemaUntouched(t *testing.T) {
	in := map[string]any{"type": "string"}
	if out := withPurposeParam(in); out["properties"] != nil {
		t.Errorf("a non-object schema was rewritten: %v", out)
	}
}

package agent

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// findRecord returns the payload of the first record of that kind, as
// the map the agent logged it as.
func findRecord(l *capturingLog, kind string) map[string]any {
	for i, k := range l.kinds {
		if k != kind {
			continue
		}
		m, _ := l.data[i].(map[string]any)
		return m
	}
	return nil
}

// commandAgent builds an agent whose shell tool is a no-op, with the
// given tool and command policies in force.
func commandAgent(t *testing.T, mb *mockBackend, gate Approver, toolPol, cmdPol map[string]string, log SessionLog) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/echo", "ran")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := policy.Build(toolPol, nil, cmdPol, false)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Backend: mb, Registry: reg, Gate: gate, System: "s",
		MaxTurns: 5, Policy: p, Log: log})
}

func shellRun(command string) *mockBackend {
	return &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: tools.ShellExecName,
			Args: map[string]any{"command": command, PurposeArg: "running the tests"}}}},
		{Content: "done"},
	}}
}

// ADR-0045 §4: a learned "never" takes one settled command off the gate.
func TestLearnedNeverSkipsTheGate(t *testing.T) {
	gate := &denyAll{}
	a := commandAgent(t, shellRun("go test ./..."), gate, nil,
		map[string]string{"go test": "never"}, nil)
	if _, err := a.Run(context.Background(), "test it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Fatalf("the gate was consulted despite a learned never: %v", gate.asked)
	}
}

// The rule applies to the key, not the whole tool: a different command
// still asks.
func TestLearnedNeverIsScopedToItsKey(t *testing.T) {
	gate := &denyAll{}
	a := commandAgent(t, shellRun("make deploy"), gate, nil,
		map[string]string{"go test": "never"}, nil)
	if _, err := a.Run(context.Background(), "deploy", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatalf("an unrelated command did not ask: %v", gate.asked)
	}
}

// A learned "never" does not lift the rule tier's Block floor — the
// ADR-0008 §2 promise, inherited by the new vocabulary.
func TestLearnedNeverDoesNotLiftTheBlockFloor(t *testing.T) {
	gate := &denyAll{}
	// Same key as the learned rule, but this call is Block-tier.
	a := commandAgent(t, shellRun("git push --force origin main"), gate, nil,
		map[string]string{"git push": "never"}, nil)
	if _, err := a.Run(context.Background(), "push", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatalf("a Block-tier call ran unattended: %v", gate.asked)
	}
}

// A command whose key cannot be derived matches no rule: the learned
// entry for `go test` must not cover a compound line that starts with it.
func TestLearnedNeverDoesNotMatchACompoundCommand(t *testing.T) {
	gate := &denyAll{}
	a := commandAgent(t, shellRun("go test ./... && rm -rf /tmp/x"), gate, nil,
		map[string]string{"go test": "never"}, nil)
	if _, err := a.Run(context.Background(), "test it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatalf("a compound command matched a learned rule: %v", gate.asked)
	}
}

// A learned "always" tightens: it asks even where the tool policy said
// never, and the session allowlist may not answer for it.
func TestLearnedAlwaysTightensAndMustPrompt(t *testing.T) {
	gate := &recordingApprover{}
	a := commandAgent(t, shellRun("git push origin main"), gate,
		map[string]string{"shell_exec": "never"},
		map[string]string{"git push": "always"}, nil)
	if _, err := a.Run(context.Background(), "push", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.calls) != 1 {
		t.Fatalf("a learned always did not ask: %v", gate.calls)
	}
	if !gate.calls[0].mustPrompt {
		t.Error("a learned always did not set mustPrompt — the session allowlist could answer it")
	}
}

// ADR-0045 §7: every gate answer leaves a record carrying the key the
// learner aggregates by, so a decision never has to be paired back to
// its call.
func TestGateDecisionIsRecordedWithItsKey(t *testing.T) {
	log := &capturingLog{}
	a := commandAgent(t, shellRun("make build"), &approveAll{}, nil, nil, log)
	if _, err := a.Run(context.Background(), "build", nil); err != nil {
		t.Fatal(err)
	}
	rec := findRecord(log, "gate_decision")
	if rec == nil {
		t.Fatal("no gate_decision record")
	}
	if rec["decision"] != "approved" || rec["key"] != "make build" {
		t.Errorf("record = %v", rec)
	}
	if rec["name"] != tools.ShellExecName {
		t.Errorf("record does not name the tool: %v", rec)
	}
	// The detail is the evidence /learn shows the operator; the purpose
	// is gem-agent's field and is not part of what ran (ADR-0047 §2).
	detail, _ := rec["detail"].(string)
	if detail != "make build" {
		t.Errorf("detail = %q, want the command line alone", detail)
	}
}

func TestGateDenialIsRecorded(t *testing.T) {
	log := &capturingLog{}
	a := commandAgent(t, shellRun("make deploy"), &denyAll{}, nil, nil, log)
	if _, err := a.Run(context.Background(), "deploy", nil); err != nil {
		t.Fatal(err)
	}
	rec := findRecord(log, "gate_decision")
	if rec == nil || rec["decision"] != "denied" || rec["key"] != "make deploy" {
		t.Errorf("record = %v", rec)
	}
}

// A command too complex to key records an empty key: a call no rule can
// ever match must not produce a rule either.
func TestUnkeyableCommandRecordsNoKey(t *testing.T) {
	log := &capturingLog{}
	a := commandAgent(t, shellRun("cat x | sh"), &approveAll{}, nil, nil, log)
	if _, err := a.Run(context.Background(), "run", nil); err != nil {
		t.Fatal(err)
	}
	rec := findRecord(log, "gate_decision")
	if rec == nil {
		t.Fatal("no gate_decision record")
	}
	if rec["key"] != "" {
		t.Errorf("key = %q, want empty for an unkeyable command", rec["key"])
	}
}

// A non-shell call keys by tool name — the ADR-0008 vocabulary.
func TestNonShellCallKeysByToolName(t *testing.T) {
	log := &capturingLog{}
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "write_file",
			Args: map[string]any{"path": "n.txt", "content": "x", PurposeArg: "saving"}}}},
		{Content: "done"},
	}}
	a := commandAgent(t, mb, &approveAll{}, nil, nil, log)
	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	rec := findRecord(log, "gate_decision")
	if rec == nil || rec["key"] != "write_file" {
		t.Errorf("record = %v", rec)
	}
}

// recordingApprover captures the arguments the gate was called with.
type recordingApprover struct {
	fromAllowlist bool
	calls []approverCall
}

type approverCall struct {
	tool       string
	mustPrompt bool
}

func (g *recordingApprover) Approve(tool, detail, purpose, reason string, mustPrompt bool) (bool, bool) {
	g.calls = append(g.calls, approverCall{tool: tool, mustPrompt: mustPrompt})
	return true, g.fromAllowlist
}

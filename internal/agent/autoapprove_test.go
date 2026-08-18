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
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// autoBackend answers tool rounds from a script and risk-eval rounds
// (identified by the absence of tool definitions) from a fixed verdict.
type autoBackend struct {
	responses  []*llm.Response
	verdict    string
	verdictErr error
	evals      []string // the prompts the risk evaluator saw
}

func (b *autoBackend) ChatStream(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	if len(defs) == 0 && strings.Contains(system, "security reviewer") {
		if b.verdictErr != nil {
			return nil, b.verdictErr
		}
		if len(msgs) > 0 {
			b.evals = append(b.evals, msgs[0].Content)
		}
		return &llm.Response{Content: b.verdict}, nil
	}
	if len(b.responses) == 0 {
		return &llm.Response{Content: "(exhausted)"}, nil
	}
	r := b.responses[0]
	b.responses = b.responses[1:]
	return r, nil
}

type recordingGate struct{ asked []string }

func (g *recordingGate) Approve(name, detail, reason string) bool {
	g.asked = append(g.asked, name+"|"+detail+"|"+reason)
	return false // deny: tests assert on whether the gate was reached
}

func newAutoAgent(t *testing.T, b *autoBackend, gate Approver) (*Agent, *tools.Registry, *[]AutoDecision) {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var decisions []AutoDecision
	a := New(Options{
		Backend: b, Registry: reg, Gate: gate, System: "sys", MaxTurns: 5,
		AutoApprove:    true,
		OnAutoDecision: func(tc llm.ToolCall, d AutoDecision) { decisions = append(decisions, d) },
	})
	return a, reg, &decisions
}

func writeCall(path string) llm.ToolCall {
	return llm.ToolCall{ID: "c1", Name: "write_file",
		Args: map[string]any{"path": path, "content": "x"}}
}

func shellCall(command string) llm.ToolCall {
	return llm.ToolCall{ID: "c1", Name: "shell_exec", Args: map[string]any{"command": command}}
}

func TestAutoSafeRunsWithoutModelOrGate(t *testing.T) {
	b := &autoBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{writeCall("src/main.go")}},
		{Content: "done"},
	}}
	gate := &recordingGate{}
	a, reg, decisions := newAutoAgent(t, b, gate)

	if _, err := a.Run(context.Background(), "write it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("safe call should not reach the gate: %v", gate.asked)
	}
	if len(b.evals) != 0 {
		t.Error("safe call must not spend a model round")
	}
	if d := (*decisions)[0]; !d.Approved || d.Tier != risk.Safe || d.ModelConsulted {
		t.Errorf("decision = %+v", d)
	}
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "src/main.go")); err != nil {
		t.Errorf("tool did not run: %v", err)
	}
}

// TestAutoBlockNeverConsultsModel is the ADR-0004 floor: a Block verdict
// reaches the human even if the model would have approved.
func TestAutoBlockNeverConsultsModel(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("rm -rf /")}},
			{Content: "ok"},
		},
		verdict: `{"approve": true, "confidence": 1.0, "reason": "looks fine"}`,
	}
	gate := &recordingGate{}
	a, _, decisions := newAutoAgent(t, b, gate)

	if _, err := a.Run(context.Background(), "clean up", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.evals) != 0 {
		t.Error("block tier must not consult the model at all")
	}
	if len(gate.asked) != 1 {
		t.Fatalf("block tier must reach the human gate: %v", gate.asked)
	}
	// The escalation must name the tier that objected as well as why:
	// "blocked by rule" reads differently from a model judgment call.
	if !strings.Contains(gate.asked[0], "delete") || !strings.Contains(gate.asked[0], "blocked by rule") {
		t.Errorf("escalation should carry tier and reason: %q", gate.asked[0])
	}
	if d := (*decisions)[0]; d.Approved || d.Tier != risk.Block || d.ModelConsulted {
		t.Errorf("decision = %+v", d)
	}
}

func TestAutoReviewApprovedByModel(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "built"},
		},
		verdict: "```json\n{\"approve\": true, \"confidence\": 0.95, \"reason\": \"local build\"}\n```",
	}
	gate := &recordingGate{}
	a, _, decisions := newAutoAgent(t, b, gate)

	if _, err := a.Run(context.Background(), "build", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("model-approved call should not reach the gate: %v", gate.asked)
	}
	if len(b.evals) != 1 {
		t.Fatalf("model tier should have run once, got %d", len(b.evals))
	}
	// The proposed call must arrive nonce-wrapped as data.
	if !strings.Contains(b.evals[0], "<proposed_call_") {
		t.Errorf("risk eval input not nonce-wrapped: %q", b.evals[0])
	}
	if d := (*decisions)[0]; !d.Approved || d.Tier != risk.Review || !d.ModelConsulted {
		t.Errorf("decision = %+v", d)
	}
}

func TestAutoReviewLowConfidenceEscalates(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("npm install")}},
			{Content: "ok"},
		},
		verdict: `{"approve": true, "confidence": 0.4, "reason": "probably fine"}`,
	}
	gate := &recordingGate{}
	a, _, _ := newAutoAgent(t, b, gate)

	if _, err := a.Run(context.Background(), "install", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatal("low confidence must escalate")
	}
	if !strings.Contains(gate.asked[0], "confidence") || !strings.Contains(gate.asked[0], "risk review") {
		t.Errorf("escalation should name the risk review and the confidence shortfall: %q", gate.asked[0])
	}
}

// TestOrdinaryPromptCarriesNoReason: with auto mode off there is no
// escalation to explain, so the prompt must not invent one.
func TestOrdinaryPromptCarriesNoReason(t *testing.T) {
	b := &autoBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{writeCall("src/main.go")}},
		{Content: "ok"},
	}}
	gate := &recordingGate{}
	a, _, _ := newAutoAgent(t, b, gate)
	a.SetAutoApprove(false)

	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatal("auto off must still ask")
	}
	if !strings.HasSuffix(gate.asked[0], "|") {
		t.Errorf("ordinary prompt should carry an empty reason: %q", gate.asked[0])
	}
}

func TestAutoModelErrorFailsClosed(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("make build")}},
			{Content: "ok"},
		},
		verdictErr: context.DeadlineExceeded,
	}
	gate := &recordingGate{}
	a, _, _ := newAutoAgent(t, b, gate)

	if _, err := a.Run(context.Background(), "build", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Fatal("model error must escalate (fail closed)")
	}
}

func TestAutoMalformedVerdictFailsClosed(t *testing.T) {
	for _, bad := range []string{"", "yes, approve it", `{"approve": true, "confidence": 7}`} {
		b := &autoBackend{
			responses: []*llm.Response{
				{ToolCalls: []llm.ToolCall{shellCall("make build")}},
				{Content: "ok"},
			},
			verdict: bad,
		}
		gate := &recordingGate{}
		a, _, _ := newAutoAgent(t, b, gate)
		if _, err := a.Run(context.Background(), "build", nil); err != nil {
			t.Fatal(err)
		}
		if len(gate.asked) != 1 {
			t.Errorf("malformed verdict %q must escalate", bad)
		}
	}
}

func TestAutoOffKeepsEveryMutatingCallGated(t *testing.T) {
	b := &autoBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{writeCall("src/main.go")}},
		{Content: "ok"},
	}}
	gate := &recordingGate{}
	a, _, _ := newAutoAgent(t, b, gate)
	a.SetAutoApprove(false)

	if _, err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 1 {
		t.Error("with auto mode off, even a safe mutating call must ask")
	}
	if len(b.evals) != 0 {
		t.Error("auto mode off must not spend model rounds")
	}
}

func TestAutoToggle(t *testing.T) {
	b := &autoBackend{}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	if !a.AutoApprove() {
		t.Fatal("constructed with AutoApprove: true")
	}
	a.SetAutoApprove(false)
	if a.AutoApprove() {
		t.Fatal("toggle off failed")
	}
}

// TestEmptyResponseErrorNamesTheCause: "the model returned nothing" is
// not actionable — a blocked prompt, an exhausted output budget, and a
// safety stop look identical without the reason the API reported.
func TestEmptyResponseErrorNamesTheCause(t *testing.T) {
	cases := []struct {
		resp llm.Response
		want string
	}{
		{llm.Response{BlockReason: "PROHIBITED_CONTENT"}, "PROHIBITED_CONTENT"},
		{llm.Response{FinishReason: "MAX_TOKENS", ThoughtTokens: 4096}, "output limit"},
		{llm.Response{FinishReason: "SAFETY"}, "SAFETY"},
		{llm.Response{}, "empty response"},
	}
	for _, c := range cases {
		resp := c.resp
		if err := emptyResponseError(&resp); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v -> %v, want mention of %q", c.resp, err, c.want)
		}
	}
}

// TestContentFilterBlockRetriesOnce: the filter fires intermittently on
// the same request (measured: five identical runs, one blocked), so one
// automatic retry turns a dead turn into a completed one — but only one,
// so a request the provider genuinely refuses still surfaces.
func TestContentFilterBlockRetriesOnce(t *testing.T) {
	b := &autoBackend{responses: []*llm.Response{
		{BlockReason: "PROHIBITED_CONTENT"},
		{Content: "went through on the retry"},
	}}
	var notices []string
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/true") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	a := New(Options{
		Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s", MaxTurns: 5,
		OnNotice: func(m string) { notices = append(notices, m) },
	})

	out, err := a.Run(context.Background(), "write the runbook", nil)
	if err != nil {
		t.Fatalf("a retryable filter block should not fail the turn: %v", err)
	}
	if out != "went through on the retry" {
		t.Errorf("out = %q", out)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "PROHIBITED_CONTENT") {
		t.Errorf("the retry must be visible: %v", notices)
	}

	// Blocked twice: report it, with advice that matches what was measured.
	b2 := &autoBackend{responses: []*llm.Response{
		{BlockReason: "PROHIBITED_CONTENT"},
		{BlockReason: "PROHIBITED_CONTENT"},
	}}
	a2 := New(Options{Backend: b2, Registry: reg, Gate: &recordingGate{}, System: "s", MaxTurns: 5})
	_, err = a2.Run(context.Background(), "write the runbook", nil)
	if err == nil || !strings.Contains(err.Error(), "sending it again often works") {
		t.Errorf("second block should report with retry advice: %v", err)
	}
}

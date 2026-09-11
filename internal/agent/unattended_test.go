package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// ADR-0084 §2: a denial in an unattended run names what still runs
// instead of a user who is not there; interactive text is unchanged.
func TestUnattendedDenialNamesTheRoute(t *testing.T) {
	run := func(unattended bool) string {
		mb := &mockBackend{responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{shellCall("go test ./...")}},
			{Content: "done"},
		}}
		_, reg := newAgent(t, mb, &denyAll{}, 5)
		a := New(Options{Backend: mb, Registry: reg, Gate: &denyAll{}, System: "s", MaxTurns: 5, Unattended: unattended})
		if _, err := a.Run(context.Background(), "test it", nil); err != nil {
			t.Fatal(err)
		}
		msgs := mb.calls[1]
		return msgs[len(msgs)-1].Content
	}
	interactive := run(false)
	if !strings.Contains(interactive, "ask the user") {
		t.Errorf("interactive denial changed: %q", interactive)
	}
	unattended := run(true)
	if strings.Contains(unattended, "ask the user") {
		t.Errorf("unattended denial still points at a user: %q", unattended)
	}
	for _, want := range []string{"unattended", "read lane", "file tools", "state what remains undone"} {
		if !strings.Contains(unattended, want) {
			t.Errorf("unattended denial lacks %q: %q", want, unattended)
		}
	}
	// The reasoned denial (ADR-0060) keeps the reason and follows the
	// same rule for its closing line.
	a := &Agent{unattended: true}
	if got := a.deniedWithReason("use notes.md"); !strings.Contains(got, "use notes.md") || strings.Contains(got, "ask the user") || !strings.Contains(got, "read lane") {
		t.Errorf("reasoned unattended denial = %q", got)
	}
	if got := (&Agent{}).deniedWithReason("use notes.md"); !strings.Contains(got, "ask the user") {
		t.Errorf("reasoned interactive denial changed: %q", got)
	}
	// Without --auto or --allow the write tools are exactly what a
	// one-shot run denies, so the route must not name them.
	for _, text := range []string{unattended, a.deniedWithReason("r")} {
		if strings.Contains(text, "file tools inside the project") || strings.Contains(text, "nothing that needs approval") {
			t.Errorf("unattended route names a tool the run denies, or overclaims: %q", text)
		}
	}
	// The read-only ceiling's refusal (ADR-0080) follows the same rule.
	if got := (&Agent{}).ceilingRefused("capped at the read lane"); !strings.Contains(got, "/readonly off") {
		t.Errorf("interactive ceiling refusal changed: %q", got)
	}
	if got := a.ceilingRefused("capped at the read lane"); strings.Contains(got, "/readonly off") || !strings.Contains(got, "capped at the read lane") || !strings.Contains(got, "read lane —") {
		t.Errorf("unattended ceiling refusal = %q", got)
	}
}

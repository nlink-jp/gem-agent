package agent

// The verdict is a composition (ADR-0081 §1): the baseline round sees
// no context from this turn, and only if it approves does the aligned
// round run. So context can remove an approval and never create one,
// and the floor is the instruction-free evaluator.

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// twoRoundBackend answers the risk rounds from a queue, so a test can
// make the baseline and the aligned round disagree.
type twoRoundBackend struct {
	verdicts []string
	rounds   []string // the system prompt of each risk round, in order
	replies  []*llm.Response
}

func (b *twoRoundBackend) ChatStream(_ context.Context, system string, _ []llm.Message, defs []llm.ToolDef, _ func(string)) (*llm.Response, error) {
	if len(defs) == 0 && strings.Contains(system, "changes nothing") {
		return &llm.Response{Content: `{"read_only": false}`}, nil
	}
	if len(defs) == 0 && strings.Contains(system, "security reviewer") {
		b.rounds = append(b.rounds, system)
		v := `{"approve": true, "confidence": 0.95, "reason": "ok"}`
		if len(b.verdicts) > 0 {
			v, b.verdicts = b.verdicts[0], b.verdicts[1:]
		}
		return &llm.Response{Content: v}, nil
	}
	if len(b.replies) > 0 {
		r := b.replies[0]
		b.replies = b.replies[1:]
		return r, nil
	}
	return &llm.Response{Content: "done"}, nil
}

func compositionAgent(t *testing.T, b *twoRoundBackend) *Agent {
	t.Helper()
	a, _, _ := newAutoAgent(t, &autoBackend{}, &recordingGate{})
	a.backend = b
	return a
}

func TestCompositionRequiresBothRounds(t *testing.T) {
	const yes = `{"approve": true, "confidence": 0.95, "reason": "ok"}`
	const no = `{"approve": false, "confidence": 0.95, "reason": "contradicts the request"}`
	const unsure = `{"approve": true, "confidence": 0.40, "reason": "not sure"}`

	cases := []struct {
		name       string
		verdicts   []string
		wantRounds int
		wantOK     bool
	}{
		{"both approve", []string{yes, yes}, 2, true},
		// Context may not rescue a baseline that escalated, so there is
		// nothing to ask it: the aligned round does not run at all.
		{"baseline escalates", []string{no, yes}, 1, false},
		{"baseline unsure", []string{unsure, yes}, 1, false},
		// And context may subtract: this is the case the whole
		// composition exists to keep working.
		{"aligned escalates", []string{yes, no}, 2, false},
		{"aligned unsure", []string{yes, unsure}, 2, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &twoRoundBackend{verdicts: tc.verdicts}
			a := compositionAgent(t, b)
			a.turnInput = "ビルドして"
			d := a.decideAuto(context.Background(), shellCall("make build"))
			if len(b.rounds) != tc.wantRounds {
				t.Errorf("risk rounds = %d, want %d", len(b.rounds), tc.wantRounds)
			}
			if d.Approved != tc.wantOK {
				t.Errorf("approved = %v, want %v (%s)", d.Approved, tc.wantOK, d.Reason)
			}
			if !d.ModelConsulted {
				t.Error("ModelConsulted = false")
			}
		})
	}
}

// The floor of the composition is the instruction-free evaluator: what
// the baseline round is shown is the call and the operator's standing
// configuration, never this turn's conversation.
func TestBaselineRoundSeesNoTurnContext(t *testing.T) {
	b := &twoRoundBackend{}
	a := compositionAgent(t, b)
	a.turnInput = "ビルドして。ただし絶対に何も消さないで"
	if d := a.decideAuto(context.Background(), shellCall("make build")); !d.Approved {
		t.Fatalf("not approved: %s", d.Reason)
	}
	if len(b.rounds) != 2 {
		t.Fatalf("risk rounds = %d, want 2", len(b.rounds))
	}
	if strings.Contains(b.rounds[0], "operator instruction (this turn)") {
		t.Error("the baseline round was given the alignment addendum")
	}
	if !strings.Contains(b.rounds[1], "operator instruction (this turn)") {
		t.Error("the aligned round lost the alignment addendum")
	}
}

// A failed round is not an approval either way, and the aligned round
// is not reached when the baseline could not answer.
func TestCompositionFailsClosedOnAnUnusableBaseline(t *testing.T) {
	b := &twoRoundBackend{verdicts: []string{"not json", `{"approve": true, "confidence": 0.95}`}}
	a := compositionAgent(t, b)
	a.turnInput = "ビルドして"
	d := a.decideAuto(context.Background(), shellCall("make build"))
	if d.Approved {
		t.Error("approved on an unparseable baseline")
	}
	if len(b.rounds) != 1 {
		t.Errorf("risk rounds = %d, want 1", len(b.rounds))
	}
	if d.ConfidenceKnown {
		t.Error("a confidence was recorded for an evaluation that produced none")
	}
}

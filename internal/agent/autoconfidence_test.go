package agent

// The auto ladder's transcript record carries what the model tier
// answered. Without it, minConfidence can only be argued from the
// constant: the record said "escalated" and not what number it was
// measured against, so no threshold could be re-examined against the
// calls it actually decided (review 2026-09-08, A-04).

import (
	"context"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// autoRecords returns the auto_decision payloads a run wrote, in order.
func autoRecords(t *testing.T, log *capturingLog) []map[string]any {
	t.Helper()
	var out []map[string]any
	for i, kind := range log.kinds {
		if kind != "auto_decision" {
			continue
		}
		rec, ok := log.data[i].(map[string]any)
		if !ok {
			t.Fatalf("auto_decision payload %d is %T, not a map", i, log.data[i])
		}
		out = append(out, rec)
	}
	return out
}

func TestAutoDecisionRecordsTheModelTiersNumber(t *testing.T) {
	cases := []struct {
		name       string
		verdict    string
		wantConf   float64
		wantApprov bool
	}{
		// Approved above the bar, and refused below it: both are the
		// model tier answering, and both numbers are worth having.
		{"approved", `{"approve": true, "confidence": 0.95, "reason": "ok"}`, 0.95, true},
		{"below the bar", `{"approve": true, "confidence": 0.42, "reason": "unsure"}`, 0.42, false},
		{"refused", `{"approve": false, "confidence": 0.9, "reason": "no"}`, 0.9, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &autoBackend{
				responses: []*llm.Response{
					{ToolCalls: []llm.ToolCall{shellCall("make build")}},
					{Content: "done"},
				},
				verdict: tc.verdict,
			}
			log := &capturingLog{}
			a, _, _ := newAutoAgent(t, b, &recordingGate{})
			a.log, a.model = log, "gemini-test-1.0"
			if _, err := a.Run(context.Background(), "ビルドして", nil); err != nil {
				t.Fatal(err)
			}
			recs := autoRecords(t, log)
			if len(recs) != 1 {
				t.Fatalf("auto_decision records = %d, want 1", len(recs))
			}
			r := recs[0]
			if r["approved"] != tc.wantApprov {
				t.Errorf("approved = %v, want %v", r["approved"], tc.wantApprov)
			}
			if got, ok := r["confidence"].(float64); !ok || got != tc.wantConf {
				t.Errorf("confidence = %v (%T), want %v", r["confidence"], r["confidence"], tc.wantConf)
			}
			// The bar travels with the record: it is a constant that can
			// change between versions, and a record that omits it cannot
			// be re-read against a different one.
			if got, ok := r["min_confidence"].(float64); !ok || got != minConfidence {
				t.Errorf("min_confidence = %v, want %v", r["min_confidence"], minConfidence)
			}
			if r["evaluator_model"] != "gemini-test-1.0" {
				t.Errorf("evaluator_model = %v", r["evaluator_model"])
			}
		})
	}
}

// A call the rule tier settled never asked the model tier, so there is
// no number to report — and an absent key must not be read back as 0.
func TestAutoDecisionOmitsConfidenceWhenTheModelTierDidNotRun(t *testing.T) {
	b := &autoBackend{
		responses: []*llm.Response{
			{ToolCalls: []llm.ToolCall{writeCall("notes.md")}}, // rule-tier Safe
			{Content: "done"},
		},
		verdict: okVerdict,
	}
	log := &capturingLog{}
	a, _, _ := newAutoAgent(t, b, &recordingGate{})
	a.log, a.model = log, "gemini-test-1.0"
	if _, err := a.Run(context.Background(), "メモを書いて", nil); err != nil {
		t.Fatal(err)
	}
	recs := autoRecords(t, log)
	if len(recs) != 1 {
		t.Fatalf("auto_decision records = %d, want 1", len(recs))
	}
	for _, key := range []string{"confidence", "min_confidence", "evaluator_model"} {
		if _, present := recs[0][key]; present {
			t.Errorf("%q recorded for a call the model tier never saw: %v", key, recs[0])
		}
	}
	if recs[0]["model"] != false {
		t.Errorf("model = %v, want false", recs[0]["model"])
	}
}

// The tier can run and still produce no usable number. The record then
// says the tier ran and names the model, but reports no confidence —
// recording 0 would be indistinguishable from a verdict of 0.
func TestAutoDecisionOmitsConfidenceWhenTheEvaluationFailed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		verdict string
	}{
		{"unparseable", "not json at all"},
		{"out of range", `{"approve": true, "confidence": 4.2, "reason": "ok"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &autoBackend{
				responses: []*llm.Response{
					{ToolCalls: []llm.ToolCall{shellCall("make build")}},
					{Content: "done"},
				},
				verdict: tc.verdict,
			}
			log := &capturingLog{}
			a, _, _ := newAutoAgent(t, b, &recordingGate{})
			a.log, a.model = log, "gemini-test-1.0"
			if _, err := a.Run(context.Background(), "ビルドして", nil); err != nil {
				t.Fatal(err)
			}
			recs := autoRecords(t, log)
			if len(recs) != 1 {
				t.Fatalf("auto_decision records = %d, want 1", len(recs))
			}
			r := recs[0]
			if r["model"] != true {
				t.Errorf("model = %v, want true — the tier did run", r["model"])
			}
			if r["evaluator_model"] != "gemini-test-1.0" {
				t.Errorf("evaluator_model = %v", r["evaluator_model"])
			}
			if _, present := r["confidence"]; present {
				t.Errorf("confidence recorded for a failed evaluation: %v", r)
			}
			if r["approved"] != false {
				t.Errorf("approved = %v, want false", r["approved"])
			}
		})
	}
}

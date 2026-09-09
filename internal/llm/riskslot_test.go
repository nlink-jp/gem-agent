package llm

import (
	"testing"

	"google.golang.org/genai"
)

// ADR-0082 §3: the model tier's slot is WithModel plus WithThinking on
// the main backend — a name and a level on the same client, and the
// main backend keeps the operator's dial.
func TestWithThinkingComposesWithWithModel(t *testing.T) {
	main := &Vertex{model: "main", thinking: genai.ThinkingLevelHigh, safety: SafetySettings("relaxed")}
	slot := main.WithModel("judge").WithThinking("low")
	if slot.model != "judge" {
		t.Errorf("slot model = %q", slot.model)
	}
	if slot.thinking != genai.ThinkingLevelLow {
		t.Errorf("slot thinking = %q, want low", slot.thinking)
	}
	if len(slot.safety) != len(main.safety) {
		t.Errorf("slot lost the safety policy: %d vs %d", len(slot.safety), len(main.safety))
	}
	if main.thinking != genai.ThinkingLevelHigh || main.model != "main" {
		t.Errorf("deriving the slot changed the main backend: %+v", main)
	}
}

// The 400 hint names the key the operator has to edit: the slot's, not
// the main model's (ADR-0082 §5 — the failure is loud and names the
// setting).
func TestWithThinkingNamesItsOwnKeyInTheHint(t *testing.T) {
	main := &Vertex{model: "main"}
	if got := main.thinkingKeyName(); got != "[model].thinking" {
		t.Errorf("main key = %q", got)
	}
	if got := main.WithModel("judge").WithThinking("minimal").thinkingKeyName(); got != "[model].risk_thinking" {
		t.Errorf("slot key = %q", got)
	}
}

// An empty level means the slot model's own default, not the main
// level (ADR-0025 §2, unchanged).
func TestWithThinkingEmptyIsModelDefault(t *testing.T) {
	main := &Vertex{model: "main", thinking: genai.ThinkingLevelHigh}
	if got := main.WithModel("judge").WithThinking("").thinking; got != "" {
		t.Errorf("empty level became %q", got)
	}
	// And WithThinking alone (the level-only slot) keeps the main name.
	if got := main.WithThinking("low"); got.model != "main" || got.thinking != genai.ThinkingLevelLow {
		t.Errorf("level-only slot = %+v", got)
	}
}

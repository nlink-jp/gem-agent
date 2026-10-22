package llm

import (
	"testing"
)

// testVertex is a client-less backend at a configured level, the shape
// NewVertex builds.
func testVertex(model, level string) *Vertex {
	return &Vertex{model: model, thinking: newThinking(level, mainThinkingKey)}
}

// ADR-0082 §3: the model tier's slot is WithModel plus WithThinking on
// the main backend — a name and a level on the same client, and the
// main backend keeps the operator's dial.
func TestWithThinkingComposesWithWithModel(t *testing.T) {
	main := testVertex("main", "high")
	main.safety = SafetySettings("relaxed")
	slot := main.WithModel("judge").WithThinking("low", "[model].risk_thinking")
	if slot.model != "judge" {
		t.Errorf("slot model = %q", slot.model)
	}
	if slot.Thinking() != "low" {
		t.Errorf("slot thinking = %q, want low", slot.Thinking())
	}
	if len(slot.safety) != len(main.safety) {
		t.Errorf("slot lost the safety policy: %d vs %d", len(slot.safety), len(main.safety))
	}
	if main.Thinking() != "high" || main.model != "main" {
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
	if got := main.WithModel("judge").WithThinking("minimal", "[model].risk_thinking").thinkingKeyName(); got != "[model].risk_thinking" {
		t.Errorf("slot key = %q", got)
	}
	// The librarian's slot names its own key, not the risk slot's.
	if got := main.WithModel("lib").WithThinking("minimal", "[model].librarian_thinking").thinkingKeyName(); got != "[model].librarian_thinking" {
		t.Errorf("librarian key = %q", got)
	}
}

// An empty level means the slot model's own default, not the main
// level (ADR-0025 §2, unchanged).
func TestWithThinkingEmptyIsModelDefault(t *testing.T) {
	main := testVertex("main", "high")
	if got := main.WithModel("judge").WithThinking("", "[model].risk_thinking").Thinking(); got != "" {
		t.Errorf("empty level became %q", got)
	}
	// And WithThinking alone (the level-only slot) keeps the main name.
	if got := main.WithThinking("low", "[model].risk_thinking"); got.model != "main" || got.Thinking() != "low" {
		t.Errorf("level-only slot = %+v", got)
	}
}

package llm

import (
	"testing"

	"google.golang.org/genai"
)

func TestThinkingLevelMapping(t *testing.T) {
	cases := map[string]genai.ThinkingLevel{
		"":        "",
		"minimal": genai.ThinkingLevelMinimal,
		"low":     genai.ThinkingLevelLow,
		"medium":  genai.ThinkingLevelMedium,
		"high":    genai.ThinkingLevelHigh,
	}
	for in, want := range cases {
		if got := ThinkingLevel(in); got != want {
			t.Errorf("ThinkingLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

// ADR-0025 §2: the summary model must not inherit the main model's
// thinking level.
func TestWithModelDoesNotInheritThinking(t *testing.T) {
	v := &Vertex{model: "main", thinking: genai.ThinkingLevelHigh}
	sub := v.WithModel("light")
	if sub.thinking != "" {
		t.Errorf("WithModel inherited thinking level %q", sub.thinking)
	}
}

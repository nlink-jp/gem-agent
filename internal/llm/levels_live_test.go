//go:build live

package llm

import (
	"context"
	"os"
	"strings"
	"testing"
)

// Measures the premise of ADR-0118: which thinking levels a model
// accepts, and what each one spends on the same small question. A level
// the model does not support is expected to fail with a 400 — the test
// logs both outcomes and fails on neither, because the answer is the
// measurement.
//
// Run: GEM_TEST_PROJECT=... GEM_TEST_MODEL=... go test -tags live -v -run ThinkingLevelsAccepted ./internal/llm/
// GEM_TEST_LEVELS narrows the levels (comma-separated; "default" is the
// model's own). An accepted level is one small call.
func TestThinkingLevelsAccepted(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	model := os.Getenv("GEM_TEST_MODEL")
	if model == "" {
		model = "gemini-3.8-flash"
	}
	levels := []string{"default", "minimal", "low", "medium", "high", "xhigh"}
	if s := os.Getenv("GEM_TEST_LEVELS"); s != "" {
		levels = strings.Split(s, ",")
	}
	hist := []Message{{Role: RoleUser, Content: "17 * 23 + 4 * 19 を計算して、答えの数だけを書いて。"}}
	for _, word := range levels {
		level := word
		if level == "default" {
			level = ""
		} else if ThinkingLevel(level) == "" {
			// A word with no wire value would be sent as the model's own
			// default and logged as accepted.
			t.Fatalf("GEM_TEST_LEVELS: %q is not a level word", word)
		}
		v, err := NewVertex(ctx, project, "global", model, "off", level, false)
		if err != nil {
			t.Fatal(err)
		}
		r, err := v.ChatStream(ctx, "", hist, nil, nil)
		if err != nil {
			t.Logf("%s %-8s REJECTED: %v", model, word, err)
			continue
		}
		t.Logf("%s %-8s accepted: answer=%q thoughts=%d output=%d prompt=%d", model, word, strings.TrimSpace(r.Content), r.ThoughtTokens, r.OutputTokens, r.PromptTokens)
	}
}

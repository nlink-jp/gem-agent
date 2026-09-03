//go:build live

package llm

import (
	"context"
	"os"
	"testing"

	"google.golang.org/genai"
)

// Measures whether IncludeThoughts returns thought-summary TEXT parts
// (ADR-0033 §3) on this model, and what shape they arrive in.
// Temporary measurement harness — not part of the suite.
func TestThoughtSummariesArrive(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project: project, Location: "global", Backend: genai.BackendVertexAI,
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &genai.GenerateContentConfig{
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true, ThinkingLevel: genai.ThinkingLevelHigh},
	}
	contents := []*genai.Content{genai.NewContentFromText(
		"1 から 50 の素数のうち桁の和が偶数のものを挙げて。よく考えて。", genai.RoleUser)}
	thoughtParts, thoughtWithText, textParts := 0, 0, 0
	for chunk, err := range client.Models.GenerateContentStream(ctx, "gemini-3.8-flash", contents, cfg) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
		if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
			continue
		}
		for _, p := range chunk.Candidates[0].Content.Parts {
			if p == nil {
				continue
			}
			if p.Thought {
				thoughtParts++
				if p.Text != "" {
					thoughtWithText++
					if thoughtWithText <= 2 {
						t.Logf("thought text sample: %.80q", p.Text)
					}
				}
			} else if p.Text != "" {
				textParts++
			}
		}
	}
	t.Logf("thought parts: %d (with text: %d), text parts: %d", thoughtParts, thoughtWithText, textParts)
	if thoughtWithText == 0 {
		t.Log("NO thought summary text arrived — IncludeThoughts may not be honored on this endpoint/model")
	}
}

// Same measurement through OUR ChatStream path: observer events.
func TestThoughtEventsThroughChatStream(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	v, err := NewVertex(ctx, project, "global", "gemini-3.8-flash", "off", "high", true)
	if err != nil {
		t.Fatal(err)
	}
	var chunks, thoughts int
	v.SetObserver(func(ev StreamEvent) {
		switch ev.Kind {
		case "chunk":
			chunks++
		case "thought":
			thoughts++
			if thoughts <= 2 {
				t.Logf("thought event: %.60q", ev.Thought)
			}
		}
	})
	msgs := []Message{{Role: RoleUser, Content: "1 から 50 の素数のうち桁の和が偶数のものを挙げて。よく考えて。"}}
	resp, err := v.ChatStream(ctx, "テスト用アシスタント", msgs, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("chunks=%d thoughts=%d contentLen=%d sigs=%d", chunks, thoughts, len(resp.Content), len(resp.ThoughtPartSigs))
	if thoughts == 0 {
		t.Error("no thought events through ChatStream — pipeline drops them")
	}
}

// The agent's real requests carry tool declarations — measure whether
// summaries still arrive with tools armed.
func TestThoughtEventsWithToolsArmed(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	v, err := NewVertex(ctx, project, "global", "gemini-3.8-flash", "off", "high", true)
	if err != nil {
		t.Fatal(err)
	}
	var thoughts int
	v.SetObserver(func(ev StreamEvent) {
		if ev.Kind == "thought" {
			thoughts++
		}
	})
	tools := []ToolDef{{Name: "read_file", Description: "read a file",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{
			"path": map[string]any{"type": "string"}}}}}
	msgs := []Message{{Role: RoleUser, Content: "1 から 50 の素数のうち桁の和が偶数のものを挙げて。ツールは不要。よく考えて。"}}
	if _, err := v.ChatStream(ctx, "テスト用アシスタント", msgs, tools, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("thoughts with tools armed: %d", thoughts)
}

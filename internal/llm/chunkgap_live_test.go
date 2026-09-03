//go:build live

package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

// Measurement for the reported false stall warning (ADR-0033 §1): the
// operator sees "no data for Ns" while the model is demonstrably alive,
// around edit_file / write_file. Hypothesis: Gemini emits a function
// call as ONE whole part, so generating a large `content` argument is a
// long silence on the wire with no chunk to prove liveness.
//
//	GEM_TEST_PROJECT=<gcp project> go test -tags live -run ChunkGap -v ./internal/llm/
func TestChunkGapDuringLargeToolCallLive(t *testing.T) {
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	backend, err := NewVertex(ctx, project, "global", "gemini-3.8-flash", "off", "", true)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	last := start
	maxGap := time.Duration(0)
	chunks := 0
	backend.SetObserver(func(ev StreamEvent) {
		now := time.Now()
		switch ev.Kind {
		case "chunk":
			chunks++
			gap := now.Sub(last)
			if gap > maxGap {
				maxGap = gap
			}
			t.Logf("t=%6.1fs chunk #%d (gap %.1fs)", now.Sub(start).Seconds(), chunks, gap.Seconds())
			last = now
		case "thought":
			// thoughts ride on chunks; only note their presence
		}
	})

	tools := []ToolDef{{
		Name:        "write_file",
		Description: "Write a file. Provide the COMPLETE file content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string"},
				"content": map[string]any{"type": "string"},
			},
			"required": []any{"path", "content"},
		},
	}}

	msgs := []Message{{Role: RoleUser, Content: "Write /tmp/gemagent-gap-demo.go: a single-file Go program of at least 400 lines implementing a small in-memory key/value store with an HTTP API, TTL expiry, LRU eviction, and thorough doc comments. Call write_file once with the complete content. Do not abbreviate."}}

	resp, err := backend.ChatStream(ctx, "You are a coding agent. Use the tools.", msgs, tools, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	total := time.Since(start)
	gapBeforeEnd := time.Since(last)
	argLen := 0
	if len(resp.ToolCalls) > 0 {
		if c, ok := resp.ToolCalls[0].Args["content"].(string); ok {
			argLen = len(c)
		}
	}
	t.Logf("RESULT: total=%.1fs chunks=%d maxGap=%.1fs tailGap=%.1fs calls=%d contentBytes=%d output=%d thoughts=%d",
		total.Seconds(), chunks, maxGap.Seconds(), gapBeforeEnd.Seconds(),
		len(resp.ToolCalls), argLen, resp.OutputTokens, resp.ThoughtTokens)
	if maxGap >= 20*time.Second {
		t.Logf("REPRODUCED: a %.1fs silence exceeds stallSeconds=20 — the TUI would warn while the model works", maxGap.Seconds())
	}
}

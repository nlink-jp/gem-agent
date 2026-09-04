//go:build live

package llm

import (
	"context"
	"os"
	"testing"
)

// Measures whether a TEXT-ONLY assistant turn replayed with its captured
// thought signatures (review round 4) is accepted: turn 1 produces a
// text answer under thinking, turn 2 replays it as parts (thought
// signatures + signed text) and asks a follow-up. Run with
// GEM_TEST_PROJECT set: go test -tags live -run TextOnlySignatureReplay ./internal/llm/
func TestTextOnlySignatureReplayIsAccepted(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT unset")
	}
	backend, err := NewVertex(ctx, project, "global", "gemini-3.8-flash", "off", "high", true)
	if err != nil {
		t.Fatal(err)
	}
	hist := []Message{{Role: RoleUser, Content: "1 から 50 の素数のうち桁の和が偶数のものを挙げて。よく考えて。最後に個数だけ書いて。"}}
	first, err := backend.ChatStream(ctx, "", hist, nil, nil)
	if err != nil {
		t.Fatalf("turn 1: %v", err)
	}
	t.Logf("turn 1: %q thoughtSigs=%d textSig=%d bytes", first.Content, len(first.ThoughtPartSigs), len(first.TextPartSig))
	if len(first.ThoughtPartSigs) == 0 && len(first.TextPartSig) == 0 {
		t.Skip("no signatures arrived on a text-only turn — nothing to replay")
	}
	hist = append(hist,
		Message{Role: RoleAssistant, Content: first.Content, ThoughtPartSigs: first.ThoughtPartSigs, TextPartSig: first.TextPartSig},
		Message{Role: RoleUser, Content: "それに 2 を足すと? 数字だけ答えて。"})
	second, err := backend.ChatStream(ctx, "", hist, nil, nil)
	if err != nil {
		t.Fatalf("turn 2 with replayed signatures REJECTED: %v", err)
	}
	t.Logf("turn 2: %q (accepted)", second.Content)
}

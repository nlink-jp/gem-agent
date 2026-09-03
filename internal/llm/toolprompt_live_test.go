//go:build live

package llm

import (
	"context"
	"os"
	"testing"
	"time"
)

// ADR-0066 §5: the evidence for the fourth bucket. One URL-context fetch
// against a page with real content; the SDK defines totalTokenCount as
// prompt + candidates + tool_use_prompt + thoughts, and this asserts
// that the four-term checksum holds WITH a non-zero fourth term — the
// case ADR-0057's main-loop probe never covered. If a future SDK moves
// the bucket, this is what notices.
//
//	GEM_TEST_PROJECT=<gcp project> go test -tags live -run ToolPromptBucket -v ./internal/llm/
func TestToolPromptBucketLive(t *testing.T) {
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	backend, err := NewVertex(ctx, project, "global", "gemini-3.5-flash-lite", "off", "", false)
	if err != nil {
		t.Fatal(err)
	}
	digest, status, u, err := backend.FetchURL(ctx,
		"Summarize https://www.rfc-editor.org/rfc/rfc2119.txt in two sentences.")
	if err != nil {
		t.Fatalf("fetch: %v (retrieval: %s)", err, status)
	}
	t.Logf("retrieval=%s digest=%.80q", status, digest)
	t.Logf("usage: prompt=%d output=%d thoughts=%d cached=%d tool_prompt=%d total=%d",
		u.Prompt, u.Output, u.Thoughts, u.Cached, u.ToolPrompt, u.Total)
	if u.ToolPrompt == 0 {
		t.Fatal("the URL-context result added nothing to the count — the probe is not representative")
	}
	if u.Prompt+u.Output+u.Thoughts == u.Total {
		t.Fatal("three-term sum balances: the fixture does not exercise the bucket")
	}
	if u.Prompt+u.Output+u.Thoughts+u.ToolPrompt != u.Total {
		t.Errorf("four-term checksum broken: %d + %d + %d + %d != %d",
			u.Prompt, u.Output, u.Thoughts, u.ToolPrompt, u.Total)
	}
}

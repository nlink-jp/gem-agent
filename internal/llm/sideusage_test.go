package llm

import (
	"testing"

	"google.golang.org/genai"
)

// ADR-0066: the side calls are the only ones that enable a built-in
// tool, so they are the only ones where toolUsePromptTokenCount is
// non-zero — and the bucket used to be dropped here, which left every
// web_search / web_fetch record with a total larger than the sum of
// its parts (issue #1). The shape below is the reporter's: the record
// fails ADR-0057's three-term check and passes the four-term one.
func TestSideUsageCarriesTheToolPromptBucket(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        1200,
			CandidatesTokenCount:    900,
			ThoughtsTokenCount:      40,
			CachedContentTokenCount: 0,
			ToolUsePromptTokenCount: 7000,
			TotalTokenCount:         9140,
		},
	}
	u := sideUsage(resp)
	want := Usage{Prompt: 1200, Output: 900, Thoughts: 40, ToolPrompt: 7000, Total: 9140}
	if u != want {
		t.Fatalf("sideUsage = %+v, want %+v", u, want)
	}
	if u.Prompt+u.Output+u.Thoughts == u.Total {
		t.Fatal("fixture does not exercise the bucket: the three-term sum already balances")
	}
	if u.Prompt+u.Output+u.Thoughts+u.ToolPrompt != u.Total {
		t.Errorf("checksum broken: %d + %d + %d + %d != %d", u.Prompt, u.Output, u.Thoughts, u.ToolPrompt, u.Total)
	}
}

// A response without usage metadata (or no response at all) is the
// zero spend — nothing to account for, and nothing to crash on.
func TestSideUsageOfNothingIsEmpty(t *testing.T) {
	if u := sideUsage(nil); !u.Empty() {
		t.Errorf("sideUsage(nil) = %+v", u)
	}
	if u := sideUsage(&genai.GenerateContentResponse{}); !u.Empty() {
		t.Errorf("sideUsage(no metadata) = %+v", u)
	}
}

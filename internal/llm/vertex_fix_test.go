package llm

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// ADR-0021: a metadata-only chunk (usage, feedback — no parts) must not
// count as consumed output, or a transient error after it needlessly
// refuses the retry.
func TestMetadataOnlyChunkIsNotConsumption(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	meta := &genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10},
	}
	if accumulateChunk(meta, resp, &text, nil, nil) {
		t.Error("usage-only chunk reported as consumed")
	}
	content := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "hi"}}}}},
	}
	if !accumulateChunk(content, resp, &text, nil, nil) {
		t.Error("text chunk not reported as consumed")
	}
	sigOnly := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, ThoughtSignature: []byte("sig")},
		}}}},
	}
	if accumulateChunk(sigOnly, resp, &text, nil, nil) {
		t.Error("signature-only chunk reported as consumed — a retry rebuilds it safely")
	}
	if len(resp.ThoughtPartSigs) != 1 {
		t.Error("signature not captured")
	}
}

// A null element in URL retrieval metadata must not panic the turn.
func TestRetrievalStatusNilElement(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			URLContextMetadata: &genai.URLContextMetadata{
				URLMetadata: []*genai.URLMetadata{nil, {RetrievedURL: "https://example.com", URLRetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"}},
			},
		}},
	}
	got := retrievalStatus(resp)
	if !strings.Contains(got, "example.com") {
		t.Errorf("retrievalStatus = %q", got)
	}
	allNil := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			URLContextMetadata: &genai.URLContextMetadata{URLMetadata: []*genai.URLMetadata{nil}},
		}},
	}
	if got := retrievalStatus(allNil); got != "no retrieval metadata" {
		t.Errorf("all-nil metadata = %q", got)
	}
}

// ADR-0033 §3: thought text is display-only — routed to onThought,
// never into resp.Content (the stored/replayed shape stays
// signatures-only, exactly what replay was measured with).
func TestThoughtTextRoutedToSinkNotStored(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	var thoughts []string
	chunk := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "planning the approach", ThoughtSignature: []byte("sig")},
		}}}},
	}
	accumulateChunk(chunk, resp, &text, nil, func(s string) { thoughts = append(thoughts, s) })
	if len(thoughts) != 1 || thoughts[0] != "planning the approach" {
		t.Errorf("thought not routed to sink: %v", thoughts)
	}
	if text.Len() != 0 || resp.Content != "" {
		t.Errorf("thought text leaked into stored content: %q %q", text.String(), resp.Content)
	}
	if len(resp.ThoughtPartSigs) != 1 {
		t.Error("signature capture regressed")
	}
	// A nil sink (plain REPL, side-calls) is safe.
	accumulateChunk(chunk, resp, &text, nil, nil)
}

func TestRetryCauseTokens(t *testing.T) {
	cases := map[string]string{
		"googleapi: Error 429: quota": "429",
		"Error 503: unavailable":      "503",
		"connection reset":            "error",
	}
	for msg, want := range cases {
		if got := retryCause(fmt.Errorf("%s", msg)); got != want {
			t.Errorf("retryCause(%q) = %q, want %q", msg, got, want)
		}
	}
	if retryCause(nil) != "error" {
		t.Error("nil error")
	}
}

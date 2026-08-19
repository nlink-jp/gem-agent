package llm

import (
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
	if accumulateChunk(meta, resp, &text, nil) {
		t.Error("usage-only chunk reported as consumed")
	}
	content := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "hi"}}}}},
	}
	if !accumulateChunk(content, resp, &text, nil) {
		t.Error("text chunk not reported as consumed")
	}
	sigOnly := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, ThoughtSignature: []byte("sig")},
		}}}},
	}
	if accumulateChunk(sigOnly, resp, &text, nil) {
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

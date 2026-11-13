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

// The cause is the status the endpoint answered with, read from the
// error value. Text is not evidence: a transport error quotes ports and
// project ids, and a 503 can mention a 429 in its message.
func TestRetryCauseIsTheStatusNotTheText(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{genai.APIError{Code: 429, Message: "quota"}, "429"},
		{genai.APIError{Code: 503, Message: "unavailable"}, "503"},
		{fmt.Errorf("stream: %w", genai.APIError{Code: 502}), "502"},
		{genai.APIError{Code: 503, Message: "upstream answered 429"}, "503"},
		{genai.APIError{Code: 400, Message: "see error 429"}, "error"},
		{fmt.Errorf("googleapi: Error 429: quota"), "error"},
		{fmt.Errorf(`Post "https://example.invalid/v1/projects/p-425003": read tcp 10.0.0.5:50234->10.0.0.9:443: connection reset`), "error"},
		{nil, "error"},
	} {
		if got := retryCause(c.err); got != c.want {
			t.Errorf("retryCause(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

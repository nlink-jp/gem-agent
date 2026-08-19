package llm

import (
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestGroundingSourcesExtractionAndDedup(t *testing.T) {
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		GroundingMetadata: &genai.GroundingMetadata{GroundingChunks: []*genai.GroundingChunk{
			{Web: &genai.GroundingChunkWeb{Title: "A", Domain: "a.example", URI: "https://a.example/1"}},
			{Web: &genai.GroundingChunkWeb{Title: "A again", URI: "https://a.example/1"}}, // dup URI
			{Web: &genai.GroundingChunkWeb{Title: "B", URI: "https://b.example/2"}},
			{Web: &genai.GroundingChunkWeb{Title: "no uri"}}, // dropped
			nil, // tolerated
		}},
	}}}
	got := groundingSources(resp)
	if len(got) != 2 || got[0].URI != "https://a.example/1" || got[1].Title != "B" {
		t.Errorf("sources = %+v", got)
	}
	if groundingSources(nil) != nil || groundingSources(&genai.GenerateContentResponse{}) != nil {
		t.Error("empty responses must yield no sources")
	}
}

func TestRetrievalStatusRendering(t *testing.T) {
	resp := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		URLContextMetadata: &genai.URLContextMetadata{URLMetadata: []*genai.URLMetadata{
			{RetrievedURL: "https://x.example", URLRetrievalStatus: genai.URLRetrievalStatusSuccess},
			{RetrievedURL: "https://paywalled.example", URLRetrievalStatus: genai.URLRetrievalStatusPaywall},
		}},
	}}}
	got := retrievalStatus(resp)
	if !strings.Contains(got, "https://x.example → URL_RETRIEVAL_STATUS_SUCCESS") ||
		!strings.Contains(got, "PAYWALL") {
		t.Errorf("status = %q", got)
	}
	if retrievalStatus(nil) != "no retrieval metadata" {
		t.Error("nil response status")
	}
}

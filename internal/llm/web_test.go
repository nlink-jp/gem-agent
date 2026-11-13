package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// The one-shot loop decides to retry from the status, as the stream
// does. It used to look for the digits in the error text, so a refusal
// that merely mentioned "502" — a port, a project id — was sent again.
func TestOneShotRetryReadsTheStatusNotTheText(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"bad request from 10.0.0.5:50234 (was 429 earlier)","status":"INVALID_ARGUMENT"}}`)
	}))
	defer srv.Close()
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := v.generateWithRetry(ctx, []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, &genai.GenerateContentConfig{})
	if err == nil {
		t.Fatal("a 400 came back as success")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("server saw %d requests for a 400 — the text's digits were read as a transient status", n)
	}
}

package llm

// Web access side-calls (ADR-0017): grounded search and URL-context
// fetch. Both run as standalone GenerateContent calls with exactly one
// provider tool enabled and no function declarations — search grounding
// does not mix with function calling in a request, and the side-call
// architecture never asks it to.

import (
	"context"
	"fmt"
	"github.com/nlink-jp/nlk/backoff"
	"time"

	"google.golang.org/genai"
)

// sideUsage reads a non-streaming response's spend into the accounting
// shape (ADR-0057): every bucket, not just prompt and output — a
// record without thoughts and cached cannot be priced. These are the
// only calls that enable a built-in tool, so they are the only ones
// where ToolUsePromptTokenCount is non-zero (ADR-0066): dropping it
// here left total larger than the sum of its parts.
func sideUsage(resp *genai.GenerateContentResponse) Usage {
	if resp == nil || resp.UsageMetadata == nil {
		return Usage{}
	}
	u := resp.UsageMetadata
	return Usage{
		Prompt:     int(u.PromptTokenCount),
		Output:     int(u.CandidatesTokenCount),
		Thoughts:   int(u.ThoughtsTokenCount),
		Cached:     int(u.CachedContentTokenCount),
		ToolPrompt: int(u.ToolUsePromptTokenCount),
		Total:      int(u.TotalTokenCount),
	}
}

// WebSource is one grounding citation.
type WebSource struct {
	Title  string
	Domain string
	URI    string
}

// SearchWeb answers a query with Grounding with Google Search and
// returns the sources the answer rests on, so a claim can be checked
// rather than believed.
func (v *Vertex) SearchWeb(ctx context.Context, query string) (string, []WebSource, Usage, error) {
	cfg := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}},
		SafetySettings: v.safety,
	}
	resp, err := v.generateWithRetry(ctx,
		[]*genai.Content{genai.NewContentFromText(query, genai.RoleUser)}, cfg)
	if err != nil {
		return "", nil, Usage{}, fmt.Errorf("grounded search: %w", err)
	}
	text := resp.Text()
	if text == "" {
		return "", nil, sideUsage(resp), fmt.Errorf("grounded search returned nothing (%s)", emptyReason(resp))
	}
	return text, groundingSources(resp), sideUsage(resp), nil
}

// FetchURL reads a URL through the URL Context tool — fetched by
// Google's infrastructure, never from this machine, which is what makes
// the SSRF class structurally unreachable (ADR-0017 §5) — and returns
// the model's digest plus the retrieval status.
func (v *Vertex) FetchURL(ctx context.Context, prompt string) (string, string, Usage, error) {
	cfg := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{URLContext: &genai.URLContext{}}},
		SafetySettings: v.safety,
	}
	resp, err := v.generateWithRetry(ctx,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, cfg)
	if err != nil {
		return "", "", Usage{}, fmt.Errorf("url fetch: %w", err)
	}
	status := retrievalStatus(resp)
	text := resp.Text()
	if text == "" {
		return "", status, sideUsage(resp), fmt.Errorf("fetch returned nothing (%s; retrieval: %s)", emptyReason(resp), status)
	}
	return text, status, sideUsage(resp), nil
}

// groundingSources extracts the web citations from a grounded response.
// Pure so tests can drive it with fabricated responses.
func groundingSources(resp *genai.GenerateContentResponse) []WebSource {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].GroundingMetadata == nil {
		return nil
	}
	var out []WebSource
	seen := map[string]bool{}
	for _, chunk := range resp.Candidates[0].GroundingMetadata.GroundingChunks {
		if chunk == nil || chunk.Web == nil || chunk.Web.URI == "" || seen[chunk.Web.URI] {
			continue
		}
		seen[chunk.Web.URI] = true
		out = append(out, WebSource{Title: chunk.Web.Title, Domain: chunk.Web.Domain, URI: chunk.Web.URI})
	}
	return out
}

// retrievalStatus summarises the URL Context metadata: per-URL fetch
// outcomes (success, error, paywall, unsafe). Pure for tests.
func retrievalStatus(resp *genai.GenerateContentResponse) string {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].URLContextMetadata == nil {
		return "no retrieval metadata"
	}
	metas := resp.Candidates[0].URLContextMetadata.URLMetadata
	if len(metas) == 0 {
		return "no retrieval metadata"
	}
	out := ""
	for i, m := range metas {
		if m == nil {
			continue // defensive: a null element must not crash the turn
		}
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%s → %s", m.RetrievedURL, m.URLRetrievalStatus)
	}
	if out == "" {
		return "no retrieval metadata"
	}
	return out
}

// emptyReason names why a response carried no text.
func emptyReason(resp *genai.GenerateContentResponse) string {
	if resp == nil {
		return "nil response"
	}
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		return string(resp.PromptFeedback.BlockReason)
	}
	if len(resp.Candidates) > 0 && resp.Candidates[0].FinishReason != "" {
		return string(resp.Candidates[0].FinishReason)
	}
	return "no candidates"
}

// generateWithRetry is GenerateContent under the same transient-error
// retry as the main stream (429 / 5xx, exponential backoff, the
// context ends the wait). A single-shot side call has nothing
// consumed to duplicate, so every attempt is safe to repeat (review
// after v0.68.2: the web tools failed on the first rate limit).
func (v *Vertex) generateWithRetry(ctx context.Context, contents []*genai.Content, cfg *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	bo := backoff.New(backoff.WithBase(500*time.Millisecond), backoff.WithMax(15*time.Second))
	var lastErr error
	for attempt := 0; attempt < maxStreamAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(bo.Duration(attempt - 1)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		resp, err := v.client.Models.GenerateContent(ctx, v.model, contents, cfg)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if retryCause(err) == "error" {
			return nil, err // not transient
		}
	}
	return nil, lastErr
}

package llm

// Web access side-calls (ADR-0017): grounded search and URL-context
// fetch. Both run as standalone GenerateContent calls with exactly one
// provider tool enabled and no function declarations — search grounding
// does not mix with function calling in a request, and the side-call
// architecture never asks it to.

import (
	"context"
	"fmt"

	"google.golang.org/genai"
)

// SideUsage is one side-call's token spend (ADR-0019).
type SideUsage struct {
	Prompt, Output int
}

func sideUsage(resp *genai.GenerateContentResponse) SideUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return SideUsage{}
	}
	return SideUsage{
		Prompt: int(resp.UsageMetadata.PromptTokenCount),
		Output: int(resp.UsageMetadata.CandidatesTokenCount),
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
func (v *Vertex) SearchWeb(ctx context.Context, query string) (string, []WebSource, SideUsage, error) {
	cfg := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{GoogleSearch: &genai.GoogleSearch{}}},
		SafetySettings: v.safety,
	}
	resp, err := v.client.Models.GenerateContent(ctx, v.model,
		[]*genai.Content{genai.NewContentFromText(query, genai.RoleUser)}, cfg)
	if err != nil {
		return "", nil, SideUsage{}, fmt.Errorf("grounded search: %w", err)
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
func (v *Vertex) FetchURL(ctx context.Context, prompt string) (string, string, SideUsage, error) {
	cfg := &genai.GenerateContentConfig{
		Tools:          []*genai.Tool{{URLContext: &genai.URLContext{}}},
		SafetySettings: v.safety,
	}
	resp, err := v.client.Models.GenerateContent(ctx, v.model,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)}, cfg)
	if err != nil {
		return "", "", SideUsage{}, fmt.Errorf("url fetch: %w", err)
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
		if i > 0 {
			out += "; "
		}
		out += fmt.Sprintf("%s → %s", m.RetrievedURL, m.URLRetrievalStatus)
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

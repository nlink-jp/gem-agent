package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// Vertex is the Vertex AI (Gemini) backend using Application Default
// Credentials. The thought-signature capture/replay and the
// function-response coalescing are ported from shell-agent-v2 (ADR-0009).
type Vertex struct {
	client *genai.Client
	model  string
}

// NewVertex creates the backend. The client is created once and reused.
func NewVertex(ctx context.Context, project, location, model string) (*Vertex, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  project,
		Location: location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("vertex AI client: %w", err)
	}
	return &Vertex{client: client, model: model}, nil
}

// Model returns the configured model name.
func (v *Vertex) Model() string { return v.model }

// ChatStream implements Backend.
func (v *Vertex) ChatStream(ctx context.Context, system string, messages []Message, tools []ToolDef, onText func(string)) (*Response, error) {
	cfg := &genai.GenerateContentConfig{
		// Keep chain-of-thought text out of responses; Gemini 3 still
		// attaches thought signatures, which we capture regardless.
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: false},
	}
	if system != "" {
		cfg.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	if len(tools) > 0 {
		cfg.Tools = convertTools(tools)
	}

	contents := buildContents(messages)

	resp := &Response{}
	var text strings.Builder
	for chunk, err := range v.client.Models.GenerateContentStream(ctx, v.model, contents, cfg) {
		if err != nil {
			return nil, fmt.Errorf("vertex AI stream: %w", err)
		}
		accumulateChunk(chunk, resp, &text, onText)
	}
	resp.Content = text.String()
	return resp, nil
}

// accumulateChunk folds one stream chunk into the response accumulator.
// Package-level and side-effect-free beyond its parameters so tests can
// drive it with fabricated chunks.
func accumulateChunk(chunk *genai.GenerateContentResponse, resp *Response, text *strings.Builder, onText func(string)) {
	if chunk == nil {
		return
	}
	if chunk.UsageMetadata != nil {
		resp.PromptTokens = int(chunk.UsageMetadata.PromptTokenCount)
		resp.OutputTokens = int(chunk.UsageMetadata.CandidatesTokenCount)
	}
	if len(chunk.Candidates) == 0 || chunk.Candidates[0].Content == nil {
		return
	}
	for _, part := range chunk.Candidates[0].Content.Parts {
		if part.Thought {
			// Thought text stays internal, but the Gemini 3 signature
			// must survive for replay — dropping it fails the next
			// round with "missing a thought_signature" / 400.
			if len(part.ThoughtSignature) > 0 {
				resp.ThoughtPartSigs = append(resp.ThoughtPartSigs, part.ThoughtSignature)
			}
			continue
		}
		if part.Text != "" {
			text.WriteString(part.Text)
			if onText != nil {
				onText(part.Text)
			}
			if len(part.ThoughtSignature) > 0 {
				resp.TextPartSig = part.ThoughtSignature
			}
		}
		if part.FunctionCall != nil {
			id := part.FunctionCall.ID
			if id == "" {
				// Vertex Gemini function calls carry no first-class id;
				// synthesise one so session records can pair results.
				b := make([]byte, 6)
				if _, err := rand.Read(b); err == nil {
					id = "vc-" + hex.EncodeToString(b)
				}
			}
			args := part.FunctionCall.Args
			if args == nil {
				args = map[string]any{}
			}
			resp.ToolCalls = append(resp.ToolCalls, ToolCall{
				ID:               id,
				Name:             part.FunctionCall.Name,
				Args:             args,
				ThoughtSignature: part.ThoughtSignature,
			})
		}
	}
}

func convertTools(tools []ToolDef) []*genai.Tool {
	var decls []*genai.FunctionDeclaration
	for _, t := range tools {
		decls = append(decls, &genai.FunctionDeclaration{
			Name:                 t.Name,
			Description:          t.Description,
			ParametersJsonSchema: t.Parameters,
		})
	}
	return []*genai.Tool{{FunctionDeclarations: decls}}
}

// buildContents maps history to genai Contents.
//
// Two hard API requirements are encoded here (both learned the hard way in
// shell-agent-v2):
//
//   - When an assistant turn issued multiple FunctionCall parts, Gemini
//     requires ALL matching FunctionResponse parts packed into a single
//     user Content — one Content per result triggers HTTP 400 ("number of
//     function response parts is equal to the number of function call
//     parts"). Consecutive tool messages are therefore coalesced.
//
//   - Gemini 3 requires thought signatures captured from the original
//     response to be replayed on the same part shapes, in the order
//     thoughts → text → function calls (typical emission order).
func buildContents(messages []Message) []*genai.Content {
	var contents []*genai.Content
	var pendingToolParts []*genai.Part
	flush := func() {
		if len(pendingToolParts) > 0 {
			contents = append(contents, &genai.Content{Role: genai.RoleUser, Parts: pendingToolParts})
			pendingToolParts = nil
		}
	}
	for _, m := range messages {
		if m.Role != RoleTool {
			flush()
		}
		switch m.Role {
		case RoleAssistant:
			if len(m.ToolCalls) == 0 {
				contents = append(contents, genai.NewContentFromText(m.Content, genai.RoleModel))
				continue
			}
			var parts []*genai.Part
			for _, sig := range m.ThoughtPartSigs {
				// Empty thought text is fine: the signature is the
				// load-bearing payload.
				parts = append(parts, &genai.Part{Thought: true, ThoughtSignature: sig})
			}
			if m.Content != "" {
				p := genai.NewPartFromText(m.Content)
				if len(m.TextPartSig) > 0 {
					p.ThoughtSignature = m.TextPartSig
				}
				parts = append(parts, p)
			}
			for _, tc := range m.ToolCalls {
				args := tc.Args
				if args == nil {
					args = map[string]any{}
				}
				p := genai.NewPartFromFunctionCall(tc.Name, args)
				if len(tc.ThoughtSignature) > 0 {
					p.ThoughtSignature = tc.ThoughtSignature
				}
				parts = append(parts, p)
			}
			contents = append(contents, &genai.Content{Role: genai.RoleModel, Parts: parts})
		case RoleTool:
			pendingToolParts = append(pendingToolParts,
				genai.NewPartFromFunctionResponse(m.ToolName, map[string]any{"result": m.Content}))
		default: // RoleUser
			contents = append(contents, genai.NewContentFromText(m.Content, genai.RoleUser))
		}
	}
	flush()
	return contents
}

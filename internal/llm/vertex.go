package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nlink-jp/nlk/backoff"
	"google.golang.org/genai"
)

// Vertex is the Vertex AI (Gemini) backend using Application Default
// Credentials. The thought-signature capture/replay and the
// function-response coalescing are ported from shell-agent-v2 (ADR-0009).
type Vertex struct {
	client *genai.Client
	model  string
	safety []*genai.SafetySetting
	// thinking is the Gemini 3 thinking level for ChatStream calls
	// (ADR-0025); empty means the model's own default. Set once at
	// construction — deliberately immutable (no live edit, no mutex).
	thinking genai.ThinkingLevel
	// includeThoughts asks the model for thought summaries, streamed
	// to the observer for display and NEVER stored (ADR-0033 §3).
	includeThoughts bool
	// observer receives StreamEvents (ADR-0033); nil = nobody watching.
	// Set once at startup, before any turn — deliberately immutable.
	observer func(StreamEvent)
}

// SetObserver installs the turn-observability sink (ADR-0033). Call
// before the first turn; events fire from the streaming goroutine.
func (v *Vertex) SetObserver(fn func(StreamEvent)) { v.observer = fn }

func (v *Vertex) observe(ev StreamEvent) {
	if v.observer != nil {
		v.observer(ev)
	}
}

// NewVertex creates the backend. The client is created once and reused.
// safety selects the configurable content-filter thresholds — see
// SafetySettings.
func NewVertex(ctx context.Context, project, location, model, safety, thinking string, includeThoughts bool) (*Vertex, error) {
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  project,
		Location: location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("vertex AI client: %w", err)
	}
	return &Vertex{client: client, model: model, safety: SafetySettings(safety), thinking: ThinkingLevel(thinking), includeThoughts: includeThoughts}, nil
}

// ThinkingLevel maps a config value ("minimal".."high") to the SDK
// enum; "" stays "" (the model's own default). Validation happens at
// config load — an unknown value never reaches here.
func ThinkingLevel(s string) genai.ThinkingLevel {
	switch s {
	case "minimal":
		return genai.ThinkingLevelMinimal
	case "low":
		return genai.ThinkingLevelLow
	case "medium":
		return genai.ThinkingLevelMedium
	case "high":
		return genai.ThinkingLevelHigh
	}
	return ""
}

// SafetySettings maps a policy name to per-category thresholds.
//
// Only four categories are configurable; Vertex also applies filters
// that no setting can turn off (a block reason of PROHIBITED_CONTENT
// comes from those). Loosening these is a deliberate operator choice —
// security work trips DANGEROUS_CONTENT on ordinary material, such as
// an incident-response runbook.
func SafetySettings(policy string) []*genai.SafetySetting {
	var threshold genai.HarmBlockThreshold
	switch policy {
	case "relaxed":
		threshold = genai.HarmBlockThresholdBlockOnlyHigh
	case "off":
		threshold = genai.HarmBlockThresholdOff
	default: // "default": send nothing, keep the provider's own defaults
		return nil
	}
	categories := []genai.HarmCategory{
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	}
	settings := make([]*genai.SafetySetting, 0, len(categories))
	for _, c := range categories {
		settings = append(settings, &genai.SafetySetting{Category: c, Threshold: threshold})
	}
	return settings
}

// Model returns the configured model name.
func (v *Vertex) Model() string { return v.model }

// WithModel returns a backend on the same client (same project,
// location, credentials, safety policy) addressing a different model —
// the [model].summary slot (ADR-0014). Model choice is per-call in the
// API, so this is a name, not a second connection.
func (v *Vertex) WithModel(name string) *Vertex {
	// The thinking level is deliberately NOT inherited (ADR-0025 §2):
	// the summary model runs at its own default — the operator's dial
	// is for the main model. Neither are thoughts nor the observer
	// (ADR-0033 §3): side-call streams have no audience.
	return &Vertex{client: v.client, model: name, safety: v.safety}
}

// ContextWindow fetches the model's input token limit from the model
// metadata. Callers treat failures as "unknown", never fatal — the
// footer display must not block a backup tool.
func (v *Vertex) ContextWindow(ctx context.Context) (int, error) {
	info, err := v.client.Models.Get(ctx, v.model, nil)
	if err != nil {
		return 0, fmt.Errorf("model metadata: %w", err)
	}
	return int(info.InputTokenLimit), nil
}

// ChatStream implements Backend.
func (v *Vertex) ChatStream(ctx context.Context, system string, messages []Message, tools []ToolDef, onText func(string)) (*Response, error) {
	cfg := &genai.GenerateContentConfig{
		// Keep chain-of-thought text out of responses; Gemini 3 still
		// attaches thought signatures, which we capture regardless.
		// ThinkingLevel "" leaves the model at its own default
		// (ADR-0025).
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: v.includeThoughts, ThinkingLevel: v.thinking},
		SafetySettings: v.safety,
	}
	if system != "" {
		cfg.SystemInstruction = genai.NewContentFromText(system, genai.RoleUser)
	}
	if len(tools) > 0 {
		cfg.Tools = convertTools(tools)
	}

	contents := buildContents(messages)

	// Vertex rate limits fire readily under sequential agent traffic
	// (429), so transient failures retry with exponential backoff — but
	// only when nothing has been consumed from the stream yet: a retry
	// after emitted text would duplicate output, and after a captured
	// function call would duplicate the call.
	bo := backoff.New(backoff.WithBase(500*time.Millisecond), backoff.WithMax(15*time.Second))
	var lastErr error
	for attempt := 0; attempt < maxStreamAttempts; attempt++ {
		if attempt > 0 {
			delay := bo.Duration(attempt - 1)
			// Deliberate waiting must not look like a hang (ADR-0033
			// §2): the observer shows the retry and its cause.
			v.observe(StreamEvent{Kind: "retry", Attempt: attempt + 1, Max: maxStreamAttempts,
				Cause: retryCause(lastErr), DelayMS: int(delay.Milliseconds())})
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		resp := &Response{}
		var text strings.Builder
		chunks := 0
		var streamErr error
		for chunk, err := range v.client.Models.GenerateContentStream(ctx, v.model, contents, cfg) {
			if err != nil {
				streamErr = err
				break
			}
			// Every chunk is a heartbeat (ADR-0033 §1): thought-only
			// and metadata-only chunks prove liveness even though they
			// display nothing.
			v.observe(StreamEvent{Kind: "chunk"})
			// Only content-bearing chunks disarm the retry: a chunk
			// carrying nothing but usage metadata consumed nothing the
			// operator saw, and refusing to retry after one needlessly
			// fails the turn (ADR-0021).
			if accumulateChunk(chunk, resp, &text, onText, v.onThought()) {
				chunks++
			}
		}
		if streamErr == nil {
			resp.Content = text.String()
			return resp, nil
		}
		if !shouldRetryStream(streamErr, chunks) || ctx.Err() != nil {
			// A 400 with a configured thinking level is, in practice,
			// often the level itself: supported levels are
			// model-dependent (minimal → 400 on some models, measured
			// in ADR-0025) and the raw API error never names the knob
			// (review round 2).
			if v.thinking != "" && strings.Contains(streamErr.Error(), "400") {
				return nil, fmt.Errorf("vertex AI stream: %w (note: [model].thinking = %q — this model may not support that level; unset it or pick another)", streamErr, v.thinking)
			}
			return nil, fmt.Errorf("vertex AI stream: %w", streamErr)
		}
		lastErr = streamErr
	}
	return nil, fmt.Errorf("vertex AI stream: %d attempts exhausted: %w", maxStreamAttempts, lastErr)
}

const maxStreamAttempts = 5

// shouldRetryStream reports whether a failed stream may be retried:
// transient HTTP status AND no chunk consumed yet (otherwise a retry
// duplicates already-delivered output or captured calls).
func shouldRetryStream(err error, chunksSeen int) bool {
	if chunksSeen > 0 {
		return false
	}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// accumulateChunk folds one stream chunk into the response accumulator.
// Package-level and side-effect-free beyond its parameters so tests can
// drive it with fabricated chunks. It reports whether the chunk carried
// content (text or a function call) — metadata-only chunks must not
// disarm the transient-error retry.
// retryCause reduces a stream error to the short token the status
// line shows ("429", "503", or "error").
func retryCause(err error) string {
	if err == nil {
		return "error"
	}
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(err.Error(), code) {
			return code
		}
	}
	return "error"
}

// onThought returns the thought-summary sink for accumulateChunk: an
// observer wrapper when someone is watching, nil otherwise.
func (v *Vertex) onThought() func(string) {
	if v.observer == nil {
		return nil
	}
	return func(s string) { v.observe(StreamEvent{Kind: "thought", Thought: s}) }
}

func accumulateChunk(chunk *genai.GenerateContentResponse, resp *Response, text *strings.Builder, onText, onThought func(string)) bool {
	if chunk == nil {
		return false
	}
	if chunk.UsageMetadata != nil {
		resp.PromptTokens = int(chunk.UsageMetadata.PromptTokenCount)
		resp.OutputTokens = int(chunk.UsageMetadata.CandidatesTokenCount)
		resp.ThoughtTokens = int(chunk.UsageMetadata.ThoughtsTokenCount)
		resp.CachedTokens = int(chunk.UsageMetadata.CachedContentTokenCount)
		resp.TotalTokens = int(chunk.UsageMetadata.TotalTokenCount)
	}
	if chunk.PromptFeedback != nil && chunk.PromptFeedback.BlockReason != "" {
		resp.BlockReason = string(chunk.PromptFeedback.BlockReason)
	}
	if len(chunk.Candidates) == 0 || chunk.Candidates[0] == nil {
		return false
	}
	if r := chunk.Candidates[0].FinishReason; r != "" {
		resp.FinishReason = string(r)
	}
	if chunk.Candidates[0].Content == nil {
		return false
	}
	consumed := false
	for _, part := range chunk.Candidates[0].Content.Parts {
		if part == nil {
			continue
		}
		if part.Thought {
			// Thought text is DISPLAY-ONLY (ADR-0033 §3): streamed to
			// the observer, never into resp/history — the stored shape
			// stays signatures-only, exactly what replay was measured
			// with. The Gemini 3 signature must survive for replay —
			// dropping it fails the next round with "missing a
			// thought_signature" / 400.
			if part.Text != "" && onThought != nil {
				onThought(part.Text)
			}
			if len(part.ThoughtSignature) > 0 {
				resp.ThoughtPartSigs = append(resp.ThoughtPartSigs, part.ThoughtSignature)
			}
			continue
		}
		if part.Text != "" {
			consumed = true
			text.WriteString(part.Text)
			if onText != nil {
				onText(part.Text)
			}
			if len(part.ThoughtSignature) > 0 {
				resp.TextPartSig = part.ThoughtSignature
			}
		}
		if part.FunctionCall != nil {
			consumed = true
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
	return consumed
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
				// An empty text part violates the Part oneof and fails
				// the whole request with 400, so a message carrying
				// nothing is dropped rather than sent (defence in depth:
				// the agent already refuses to store empty turns).
				if m.Content == "" {
					continue
				}
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
			fr := &genai.FunctionResponse{
				Name:     m.ToolName,
				Response: map[string]any{"result": m.Content},
			}
			// Multimodal function responses (ADR-0012): view_image's
			// pixels ride inside the response. A separate user message
			// after the tool round was measured to 400 on the following
			// round, so this SDK mechanism is the one that works.
			for _, att := range m.Attachments {
				if len(att.Data) > 0 && att.MIME != "" {
					fr.Parts = append(fr.Parts, genai.NewFunctionResponsePartFromBytes(att.Data, att.MIME))
				}
			}
			pendingToolParts = append(pendingToolParts, &genai.Part{FunctionResponse: fr})
		default: // RoleUser
			var parts []*genai.Part
			if m.Content != "" {
				parts = append(parts, genai.NewPartFromText(m.Content))
			}
			// Image/document/media attachments become inline-data parts
			// after the text (ADR-0012/0026/0027); bucket-routed media
			// becomes a file-data part Vertex reads from GCS. Text
			// attachments never reach this point — the agent flattens
			// them into Content at send time.
			for _, att := range m.Attachments {
				switch {
				case att.URI != "" && att.MIME != "":
					parts = append(parts, genai.NewPartFromURI(att.URI, att.MIME))
				case len(att.Data) > 0 && att.MIME != "":
					parts = append(parts, genai.NewPartFromBytes(att.Data, att.MIME))
				}
			}
			if len(parts) == 0 {
				continue // see above: an empty part fails the request
			}
			contents = append(contents, &genai.Content{Role: genai.RoleUser, Parts: parts})
		}
	}
	flush()
	return contents
}

package llm

import (
	"bytes"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestBuildContentsCoalescesToolResponses(t *testing.T) {
	sigThought := []byte("sig-thought")
	sigCall1 := []byte("sig-call-1")
	sigCall2 := []byte("sig-call-2")

	msgs := []Message{
		{Role: RoleUser, Content: "list and read"},
		{
			Role:            RoleAssistant,
			Content:         "checking",
			ThoughtPartSigs: [][]byte{sigThought},
			TextPartSig:     []byte("sig-text"),
			ToolCalls: []ToolCall{
				{ID: "1", Name: "list_files", Args: map[string]any{"path": "."}, ThoughtSignature: sigCall1},
				{ID: "2", Name: "read_file", Args: map[string]any{"path": "a.txt"}, ThoughtSignature: sigCall2},
			},
		},
		{Role: RoleTool, ToolName: "list_files", ToolCallID: "1", Content: "a.txt"},
		{Role: RoleTool, ToolName: "read_file", ToolCallID: "2", Content: "hello"},
		{Role: RoleAssistant, Content: "done"},
	}

	contents := buildContents(msgs)
	if len(contents) != 4 {
		t.Fatalf("len(contents) = %d, want 4 (user, model, coalesced tool, model)", len(contents))
	}

	// Assistant tool-call turn: thought sig part → text part → 2 fc parts.
	model := contents[1]
	if model.Role != genai.RoleModel {
		t.Errorf("contents[1].Role = %v", model.Role)
	}
	if len(model.Parts) != 4 {
		t.Fatalf("model parts = %d, want 4", len(model.Parts))
	}
	if !model.Parts[0].Thought || !bytes.Equal(model.Parts[0].ThoughtSignature, sigThought) {
		t.Error("thought signature part not replayed first")
	}
	if model.Parts[1].Text != "checking" || !bytes.Equal(model.Parts[1].ThoughtSignature, []byte("sig-text")) {
		t.Error("text part signature not replayed")
	}
	if model.Parts[2].FunctionCall == nil || !bytes.Equal(model.Parts[2].ThoughtSignature, sigCall1) {
		t.Error("first function-call signature not replayed")
	}
	if model.Parts[3].FunctionCall == nil || !bytes.Equal(model.Parts[3].ThoughtSignature, sigCall2) {
		t.Error("second function-call signature not replayed")
	}

	// Both tool results must land in ONE user Content (Gemini pairs
	// function responses against the call turn; splitting them is a 400).
	toolContent := contents[2]
	if toolContent.Role != genai.RoleUser {
		t.Errorf("tool content role = %v", toolContent.Role)
	}
	if len(toolContent.Parts) != 2 {
		t.Fatalf("coalesced tool parts = %d, want 2", len(toolContent.Parts))
	}
	for i, p := range toolContent.Parts {
		if p.FunctionResponse == nil {
			t.Errorf("tool part %d is not a FunctionResponse", i)
		}
	}
}

// TestBuildContentsSkipsEmptyMessages: an empty text part violates the
// Part oneof and fails the entire request with 400 — which, once such a
// message is in history, repeats on every later turn and poisons the
// session (observed in the field).
func TestBuildContentsSkipsEmptyMessages(t *testing.T) {
	contents := buildContents([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: ""}, // an empty model response
		{Role: RoleUser, Content: ""},      // an empty user turn
		{Role: RoleUser, Content: "again"},
	})
	if len(contents) != 2 {
		t.Fatalf("len = %d, want the two non-empty messages", len(contents))
	}
	for i, c := range contents {
		for j, p := range c.Parts {
			if p.Text == "" && p.FunctionCall == nil && p.FunctionResponse == nil && !p.Thought {
				t.Errorf("contents[%d].parts[%d] carries no data", i, j)
			}
		}
	}
}

func TestBuildContentsPlainAssistantTurn(t *testing.T) {
	contents := buildContents([]Message{
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "hello"},
	})
	if len(contents) != 2 {
		t.Fatalf("len = %d", len(contents))
	}
	if contents[1].Role != genai.RoleModel || contents[1].Parts[0].Text != "hello" {
		t.Errorf("assistant turn mapped wrong: %+v", contents[1])
	}
}

func TestAccumulateChunkStreams(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	var streamed []string
	onText := func(s string) { streamed = append(streamed, s) }

	chunk := func(parts ...*genai.Part) *genai.GenerateContentResponse {
		return &genai.GenerateContentResponse{
			Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: parts}}},
		}
	}

	accumulateChunk(chunk(&genai.Part{Text: "Hel"}), resp, &text, onText, nil)
	accumulateChunk(chunk(&genai.Part{Text: "lo", ThoughtSignature: []byte("sig-text")}), resp, &text, onText, nil)
	accumulateChunk(chunk(
		&genai.Part{Thought: true, ThoughtSignature: []byte("sig-thought")},
		&genai.Part{
			FunctionCall:     &genai.FunctionCall{Name: "read_file", Args: map[string]any{"path": "a"}},
			ThoughtSignature: []byte("sig-fc"),
		},
	), resp, &text, onText, nil)
	// Usage arrives on the last chunk.
	last := chunk()
	last.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10, CandidatesTokenCount: 5}
	accumulateChunk(last, resp, &text, onText, nil)
	accumulateChunk(nil, resp, &text, onText, nil) // nil chunk must be harmless

	if text.String() != "Hello" {
		t.Errorf("text = %q", text.String())
	}
	if strings.Join(streamed, "|") != "Hel|lo" {
		t.Errorf("streamed = %v", streamed)
	}
	if !bytes.Equal(resp.TextPartSig, []byte("sig-text")) {
		t.Error("text part signature not captured")
	}
	if len(resp.ThoughtPartSigs) != 1 || !bytes.Equal(resp.ThoughtPartSigs[0], []byte("sig-thought")) {
		t.Error("thought signature not captured")
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.Name != "read_file" || !bytes.Equal(tc.ThoughtSignature, []byte("sig-fc")) {
		t.Errorf("tool call = %+v", tc)
	}
	if tc.ID == "" {
		t.Error("missing synthesised tool-call ID")
	}
	if resp.PromptTokens != 10 || resp.OutputTokens != 5 {
		t.Errorf("usage = %d/%d", resp.PromptTokens, resp.OutputTokens)
	}
}

func TestShouldRetryStream(t *testing.T) {
	rateLimited := genai.APIError{Code: 429, Message: "rate limited"}
	if !shouldRetryStream(rateLimited, 0) {
		t.Error("429 before any chunk should retry")
	}
	if shouldRetryStream(rateLimited, 1) {
		t.Error("retry after consumed chunks would duplicate output")
	}
	if shouldRetryStream(genai.APIError{Code: 404}, 0) {
		t.Error("404 is not transient")
	}
	if shouldRetryStream(genai.APIError{Code: 400}, 0) {
		t.Error("400 is not transient")
	}
	if !shouldRetryStream(genai.APIError{Code: 503}, 0) {
		t.Error("503 should retry")
	}
	if shouldRetryStream(errNotAPI, 0) {
		t.Error("non-API errors are not retryable")
	}
}

var errNotAPI = &customErr{}

type customErr struct{}

func (*customErr) Error() string { return "boom" }

func TestConvertTools(t *testing.T) {
	defs := convertTools([]ToolDef{{
		Name:        "read_file",
		Description: "Read a file.",
		Parameters:  map[string]any{"type": "object"},
	}})
	if len(defs) != 1 || len(defs[0].FunctionDeclarations) != 1 {
		t.Fatalf("shape = %+v", defs)
	}
	fd := defs[0].FunctionDeclarations[0]
	if fd.Name != "read_file" || fd.Description == "" || fd.ParametersJsonSchema == nil {
		t.Errorf("declaration = %+v", fd)
	}
}

func TestSafetySettings(t *testing.T) {
	if s := SafetySettings("default"); s != nil {
		t.Errorf("default policy must send nothing (keep the provider's own): %v", s)
	}
	for policy, want := range map[string]genai.HarmBlockThreshold{
		"relaxed": genai.HarmBlockThresholdBlockOnlyHigh,
		"off":     genai.HarmBlockThresholdOff,
	} {
		s := SafetySettings(policy)
		if len(s) != 4 {
			t.Fatalf("%s: %d settings, want the 4 configurable categories", policy, len(s))
		}
		for _, setting := range s {
			if setting.Threshold != want {
				t.Errorf("%s: %s threshold = %s, want %s", policy, setting.Category, setting.Threshold, want)
			}
		}
	}
}

// ADR-0057: the accounting buckets, exactly as the API reports them.
// Measured live: prompt + candidates + thoughts == total, so thoughts
// are a SEPARATE bucket (billed as output) and cached is a discounted
// share OF prompt. Total is carried as the checksum for both facts.
func TestAccumulateChunkCapturesEveryBillingBucket(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	last := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: nil}}},
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        25,
			CandidatesTokenCount:    174,
			ThoughtsTokenCount:      534,
			CachedContentTokenCount: 20,
			TotalTokenCount:         733,
		},
	}
	accumulateChunk(last, resp, &text, nil, nil)

	u := resp.Usage()
	want := Usage{Prompt: 25, Output: 174, Thoughts: 534, Cached: 20, Total: 733}
	if u != want {
		t.Errorf("usage = %+v, want %+v", u, want)
	}
	if u.Prompt+u.Output+u.Thoughts != u.Total {
		t.Errorf("checksum broken: %d + %d + %d != %d", u.Prompt, u.Output, u.Thoughts, u.Total)
	}
	if u.Empty() {
		t.Error("a call that spent tokens reported Empty")
	}
	if !(Usage{}).Empty() {
		t.Error("the zero usage is not Empty")
	}
}

// Package llm defines the backend abstraction and the Vertex AI Gemini
// implementation. The agent loop depends only on the Backend interface so
// tests can drive it with a scripted mock.
package llm

import "context"

// Role identifies who produced a message.
type Role string

// Message roles. The system prompt travels separately (SystemInstruction),
// not as a message.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall is one function call requested by the model.
//
// The JSON tags are load-bearing, not decoration: a Message is written
// verbatim to the session transcript and read back to resume a session
// (ADR-0005), so these names are a persisted format. []byte fields
// round-trip as base64 through encoding/json.
type ToolCall struct {
	ID   string         `json:"id,omitempty"`
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
	// ThoughtSignature is the Gemini 3 opaque continuation token attached
	// to the function-call part. It must be echoed back on the same part
	// shape in the next request or the API fails with 400 (ADR-0009
	// pattern, ported from shell-agent-v2).
	ThoughtSignature []byte `json:"thought_signature,omitempty"`
}

// Attachment is file or directory content pulled in by an @-reference.
// It is stored apart from Content because it is untrusted data: the
// agent wraps it in the turn's nonce tag, exactly like a tool result.
type Attachment struct {
	Ref     string `json:"ref"`
	Kind    string `json:"kind"`
	Content string `json:"content,omitempty"`
	// Data and MIME carry binary content — images (ADR-0012),
	// documents (ADR-0026), inline media (ADR-0027). []byte round-trips
	// as base64 through the transcript, so a resumed session keeps the
	// screenshots it was looking at.
	Data []byte `json:"data,omitempty"`
	MIME string `json:"mime,omitempty"`
	// URI references bucket-routed media (ADR-0027): the transcript
	// stores the gs:// URI, not the bytes — resume stays cheap, and
	// re-reading the media works while the object exists (retention is
	// the operator's bucket lifecycle).
	URI string `json:"uri,omitempty"`
}

// Message is one turn in the conversation history.
type Message struct {
	Role    Role   `json:"role"`
	Content string `json:"content,omitempty"`
	// Attachments accompany a user message (@-references).
	Attachments []Attachment `json:"attachments,omitempty"`

	// Assistant-role fields.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ThoughtPartSigs holds signatures of thought parts (text filtered
	// out at parse time; the signature is the load-bearing payload).
	ThoughtPartSigs [][]byte `json:"thought_part_sigs,omitempty"`
	// TextPartSig holds the signature attached to the text part, if any.
	TextPartSig []byte `json:"text_part_sig,omitempty"`

	// Tool-role fields.
	ToolName   string `json:"tool_name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolDef describes one tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Response is the parsed result of one model turn.
type Response struct {
	Content         string
	ToolCalls       []ToolCall
	ThoughtPartSigs [][]byte
	TextPartSig     []byte
	PromptTokens    int
	OutputTokens    int
	// CachedTokens counts prompt tokens served from the implicit cache
	// (ADR-0018) — the measured answer to "is caching actually firing".
	CachedTokens int
	// FinishReason and BlockReason explain a response that carries no
	// text — without them, "the model returned nothing" is untriageable
	// (MAX_TOKENS spent on thinking reads exactly like a safety block).
	FinishReason string
	BlockReason  string
	// ThoughtTokens counts reasoning tokens, which are billed as output
	// and consume the output budget on thinking models.
	ThoughtTokens int
}

// Backend is one LLM provider. onText receives streamed text deltas as
// they arrive; the returned Response carries the accumulated turn.
type Backend interface {
	ChatStream(ctx context.Context, system string, messages []Message, tools []ToolDef, onText func(string)) (*Response, error)
}

// StreamEvent is turn observability (ADR-0033): the backend reports
// stream liveness, retries, and thought summaries to whoever is
// watching (the TUI). Events are display-only — nothing here enters
// the history or the transcript.
type StreamEvent struct {
	// Kind: "chunk" (any chunk arrived — liveness), "thought" (a
	// thought-summary delta; Thought carries the text), or "retry"
	// (a backoff retry was scheduled; Attempt/Max/Cause/DelayMS set).
	Kind    string
	Thought string
	Attempt int
	Max     int
	Cause   string
	DelayMS int
}

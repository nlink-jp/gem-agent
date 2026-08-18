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
type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
	// ThoughtSignature is the Gemini 3 opaque continuation token attached
	// to the function-call part. It must be echoed back on the same part
	// shape in the next request or the API fails with 400 (ADR-0009
	// pattern, ported from shell-agent-v2).
	ThoughtSignature []byte
}

// Attachment is file or directory content pulled in by an @-reference.
// It is stored apart from Content because it is untrusted data: the
// agent wraps it in the turn's nonce tag, exactly like a tool result.
type Attachment struct {
	Ref     string
	Kind    string
	Content string
}

// Message is one turn in the conversation history.
type Message struct {
	Role    Role
	Content string
	// Attachments accompany a user message (@-references).
	Attachments []Attachment

	// Assistant-role fields.
	ToolCalls []ToolCall
	// ThoughtPartSigs holds signatures of thought parts (text filtered
	// out at parse time; the signature is the load-bearing payload).
	ThoughtPartSigs [][]byte
	// TextPartSig holds the signature attached to the text part, if any.
	TextPartSig []byte

	// Tool-role fields.
	ToolName   string
	ToolCallID string
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

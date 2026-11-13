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
	// Denial marks a gate-denial result (ADR-0060): content authored by
	// gem-agent and the operator, no one else — so the send-time wrap
	// treats it as trusted. Set in exactly one place, the executor's
	// denial path; recognizing denials by content instead would let any
	// tool output shaped like one ride unwrapped. Additive field: old
	// builds resuming a transcript that carries it just wrap the denial
	// as before.
	Denial bool `json:"denial,omitempty"`
	// RuntimeNote is gem-agent's own words appended to a tool result
	// outside the nonce tag (ADR-0075 §3): what the runtime measured
	// about a remote tool's repeated identical failure, and the action
	// to take. Trusted by provenance exactly like Denial — set in one
	// place, the executor — never recognized by content. Additive: an
	// old build resuming a transcript that carries it drops the note
	// from the replay and nothing else.
	RuntimeNote string `json:"runtime_note,omitempty"`
	// Saved lists the files the runtime wrote for this result — every
	// MCP text block, whatever its size (ADR-0105 §1). It is the record
	// evidence is checked against: set by the executor from what the
	// intake reported, never parsed out of the result text, which a
	// server writes. The model reads it as gem-agent's own note outside
	// the nonce tag. Additive: an old build resuming a transcript that
	// carries it ignores it.
	Saved []SavedFile `json:"saved,omitempty"`
}

// SavedFile is one entry of Message.Saved.
type SavedFile struct {
	Path   string `json:"path,omitempty"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256,omitempty"`
	// Error says why the block was not saved; Path is empty then.
	Error string `json:"error,omitempty"`
}

// ToolDef describes one tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Usage is one model call's token spend, in the buckets billing uses
// (ADR-0057). Thoughts are NOT part of Output — the API reports them
// separately and bills them as output — and Cached is a discounted
// SHARE of Prompt, not an addition to it. Both measured live. ToolPrompt
// is the fourth addend of the API's total (ADR-0066): the results of
// the provider's built-in tools (search grounding, URL context) fed
// back to the model as input, billed as input and never cached — zero
// everywhere but the web side calls. Total is the API's own
// totalTokenCount, kept so an aggregator can check itself instead of
// undercounting quietly: prompt + output + thoughts + tool_prompt.
type Usage struct {
	Prompt     int `json:"prompt"`
	Output     int `json:"output"`
	Thoughts   int `json:"thoughts"`
	Cached     int `json:"cached"`
	ToolPrompt int `json:"tool_prompt"`
	Total      int `json:"total"`
}

// Empty reports a call that spent nothing — nothing to account for.
func (u Usage) Empty() bool { return u.Prompt == 0 && u.Output == 0 && u.Thoughts == 0 }

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
	// ToolPromptTokens counts built-in tool results fed back as input
	// (ADR-0066) — the fourth addend of TotalTokens, structurally zero
	// on the main loop, which enables no provider tool.
	ToolPromptTokens int
	// TotalTokens is the API's own totalTokenCount — the checksum for
	// the buckets above (ADR-0057, ADR-0066).
	TotalTokens int
}

// Usage returns the call's spend as the accounting record shape.
func (r *Response) Usage() Usage {
	if r == nil {
		return Usage{}
	}
	return Usage{Prompt: r.PromptTokens, Output: r.OutputTokens,
		Thoughts: r.ThoughtTokens, Cached: r.CachedTokens,
		ToolPrompt: r.ToolPromptTokens, Total: r.TotalTokens}
}

// Backend is one LLM provider. onText receives streamed text deltas as
// they arrive; the returned Response carries the accumulated turn.
type Backend interface {
	ChatStream(ctx context.Context, system string, messages []Message, tools []ToolDef, onText func(string)) (*Response, error)
}

// RetryEvent is one backoff retry a model call scheduled: a wait the
// model did not cause. Unlike a StreamEvent it is meant to be kept
// (ADR-0116): from the outside a wait on a 429 and a slow answer are
// the same silence, and afterwards only a record tells which it was.
type RetryEvent struct {
	// Model is the model the failed attempt addressed.
	Model string
	// Source is which kind of call it was, in the words the usage
	// record uses ("main", "risk", "compact", …): the caller's WithSource.
	// "" when the caller did not say.
	Source string
	// Attempt is the attempt about to start (2 is the first retry),
	// out of Max.
	Attempt int
	Max     int
	// Cause is the HTTP status that failed the attempt before it
	// ("429", "503", …).
	Cause string
	// Status is the endpoint's machine-readable status for that attempt
	// ("RESOURCE_EXHAUSTED", "UNAVAILABLE", …), or "" when it sent none
	// that is a plain token. The endpoint's message is not kept: it is
	// free text this runtime does not control.
	Status string
	// DelayMS is the backoff wait before Attempt starts.
	DelayMS int
}

// The sources of the one-shot calls this package makes itself; they are
// the usage record's names for the same calls.
const (
	SourceWebSearch = "web_search"
	SourceWebFetch  = "web_fetch"
)

type sourceKey struct{}

// WithSource names the kind of model call made under ctx, for the retry
// record. The outermost name stands: a call made on behalf of another —
// the agentic search child's own loop — keeps the name of the tool that
// started it, as its usage record does.
func WithSource(ctx context.Context, source string) context.Context {
	if _, ok := ctx.Value(sourceKey{}).(string); ok {
		return ctx
	}
	return context.WithValue(ctx, sourceKey{}, source)
}

func sourceOf(ctx context.Context) string {
	s, _ := ctx.Value(sourceKey{}).(string)
	return s
}

// StreamEvent is turn observability (ADR-0033): the backend reports
// stream liveness, retries, and thought summaries to whoever is
// watching (the TUI). Events are display-only — nothing here enters
// the history or the transcript; a retry is also reported as a
// RetryEvent, which is kept (ADR-0116).
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

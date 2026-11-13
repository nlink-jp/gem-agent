package llm

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

// ADR-0021: a metadata-only chunk (usage, feedback — no parts) must not
// count as consumed output, or a transient error after it needlessly
// refuses the retry.
func TestMetadataOnlyChunkIsNotConsumption(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	meta := &genai.GenerateContentResponse{
		UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 10},
	}
	if accumulateChunk(meta, resp, &text, nil, nil) {
		t.Error("usage-only chunk reported as consumed")
	}
	content := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{{Text: "hi"}}}}},
	}
	if !accumulateChunk(content, resp, &text, nil, nil) {
		t.Error("text chunk not reported as consumed")
	}
	sigOnly := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, ThoughtSignature: []byte("sig")},
		}}}},
	}
	if accumulateChunk(sigOnly, resp, &text, nil, nil) {
		t.Error("signature-only chunk reported as consumed — a retry rebuilds it safely")
	}
	if len(resp.ThoughtPartSigs) != 1 {
		t.Error("signature not captured")
	}
}

// A null element in URL retrieval metadata must not panic the turn.
func TestRetrievalStatusNilElement(t *testing.T) {
	resp := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			URLContextMetadata: &genai.URLContextMetadata{
				URLMetadata: []*genai.URLMetadata{nil, {RetrievedURL: "https://example.com", URLRetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"}},
			},
		}},
	}
	got := retrievalStatus(resp)
	if !strings.Contains(got, "example.com") {
		t.Errorf("retrievalStatus = %q", got)
	}
	allNil := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{
			URLContextMetadata: &genai.URLContextMetadata{URLMetadata: []*genai.URLMetadata{nil}},
		}},
	}
	if got := retrievalStatus(allNil); got != "no retrieval metadata" {
		t.Errorf("all-nil metadata = %q", got)
	}
}

// ADR-0033 §3: thought text is display-only — routed to onThought,
// never into resp.Content (the stored/replayed shape stays
// signatures-only, exactly what replay was measured with).
func TestThoughtTextRoutedToSinkNotStored(t *testing.T) {
	resp := &Response{}
	var text strings.Builder
	var thoughts []string
	chunk := &genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
			{Thought: true, Text: "planning the approach", ThoughtSignature: []byte("sig")},
		}}}},
	}
	accumulateChunk(chunk, resp, &text, nil, func(s string) { thoughts = append(thoughts, s) })
	if len(thoughts) != 1 || thoughts[0] != "planning the approach" {
		t.Errorf("thought not routed to sink: %v", thoughts)
	}
	if text.Len() != 0 || resp.Content != "" {
		t.Errorf("thought text leaked into stored content: %q %q", text.String(), resp.Content)
	}
	if len(resp.ThoughtPartSigs) != 1 {
		t.Error("signature capture regressed")
	}
	// A nil sink (plain REPL, side-calls) is safe.
	accumulateChunk(chunk, resp, &text, nil, nil)
}

// The cause is the status the endpoint answered with, read from the
// error value. Text is not evidence: a transport error quotes ports and
// project ids, and a 503 can mention a 429 in its message.
func TestRetryCauseIsTheStatusNotTheText(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{genai.APIError{Code: 429, Message: "quota"}, "429"},
		{genai.APIError{Code: 503, Message: "unavailable"}, "503"},
		{fmt.Errorf("stream: %w", genai.APIError{Code: 502}), "502"},
		{genai.APIError{Code: 503, Message: "upstream answered 429"}, "503"},
		{genai.APIError{Code: 400, Message: "see error 429"}, "error"},
		{fmt.Errorf("googleapi: Error 429: quota"), "error"},
		{fmt.Errorf(`Post "https://example.invalid/v1/projects/p-425003": read tcp 10.0.0.5:50234->10.0.0.9:443: connection reset`), "error"},
		{nil, "error"},
	} {
		if got := retryCause(c.err); got != c.want {
			t.Errorf("retryCause(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// A retry always waits. Each wait stays within the jitter of its step,
// so the first is at least a quarter second and the steps still double.
func TestRetryBackoffNeverFiresAtOnce(t *testing.T) {
	bo := retryBackoff()
	for step := 0; step < maxStreamAttempts-1; step++ {
		want := min(retryBase<<step, retryMax)
		for range 500 {
			d := bo.Duration(step)
			if d < want-retryJitter || d > want+retryJitter {
				t.Fatalf("step %d waited %v, want %v give or take %v", step, d, want, retryJitter)
			}
		}
	}
	if lowest := retryBase - retryJitter; lowest < 250*time.Millisecond {
		t.Errorf("the shortest first wait is %v — a retry that fires at once is no backoff", lowest)
	}
}

// Both retry loops take their backoff from retryBackoff and nowhere
// else. A timing assertion meets the old, wider jitter only by chance —
// a first wait drawn from it lands inside the new bounds one time in
// four — so the call sites are pinned in the source: retryBackoff is
// the one place in this package that builds a backoff, and both loops
// call it.
func TestTheOnlyBackoffIsRetryBackoff(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	built := 0
	users := map[string]bool{}
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch f := call.Fun.(type) {
				case *ast.SelectorExpr:
					if x, ok := f.X.(*ast.Ident); ok && x.Name == "backoff" && f.Sel.Name == "New" {
						built++
						if fn.Name.Name != "retryBackoff" {
							t.Errorf("%s builds a backoff of its own at %s — use retryBackoff()", fn.Name.Name, fset.Position(call.Pos()))
						}
					}
				case *ast.Ident:
					if f.Name == "retryBackoff" {
						users[fn.Name.Name] = true
					}
				}
				return true
			})
		}
	}
	if built != 1 || !users["ChatStream"] || !users["generateWithRetry"] {
		t.Fatalf("backoff.New is called %d time(s); retryBackoff is used by %v — want one constructor, used by ChatStream and generateWithRetry", built, users)
	}
}

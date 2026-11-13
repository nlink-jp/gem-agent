package llm

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// continuing answers every request with one chunk that ends the way a
// long decode does: finish reason CONTINUATION and a token to resume
// with. An SDK left at its default sends the request again on seeing it.
func continuing(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := calls.Add(1)
		body := `{"candidates":[{"content":{"role":"model","parts":[{"text":"part"}]},"finishReason":"CONTINUATION","continuationToken":"dG9rZW4="}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":5,"totalTokenCount":105}}`
		if n > 1 {
			body = `{"candidates":[{"content":{"role":"model","parts":[{"text":" two"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":105,"candidatesTokenCount":5,"totalTokenCount":110}}`
		}
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+body+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// One model call is one request (ADR-0117). The endpoint offers a
// continuation; the stream ends there, with the usage of that one
// request, and the finish reason says so. Without oneRequest the SDK
// sends a second request and adds its prompt tokens to the first's.
func TestChatStreamDoesNotFollowAContinuation(t *testing.T) {
	srv, calls := continuing(t)
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := v.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the endpoint saw %d requests for one call, want 1", n)
	}
	if resp.Content != "part" || resp.PromptTokens != 100 || resp.FinishReason != "CONTINUATION" {
		t.Errorf("content %q, prompt %d, finish %q; want the one request's own: part, 100, CONTINUATION",
			resp.Content, resp.PromptTokens, resp.FinishReason)
	}
}

// The one-shot calls behind the web tools are single requests too.
func TestOneShotDoesNotFollowAContinuation(t *testing.T) {
	srv, calls := continuing(t)
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cfg := &genai.GenerateContentConfig{AutomaticContinuation: oneRequest()}
	resp, err := v.generateWithRetry(ctx, []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, cfg)
	if err != nil {
		t.Fatalf("generateWithRetry: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the endpoint saw %d requests for one call, want 1", n)
	}
	if u := resp.UsageMetadata; u == nil || u.PromptTokenCount != 100 {
		t.Errorf("usage = %+v, want the one request's prompt count of 100", u)
	}
}

// The control: the same endpoint, the SDK's default. If this stops
// sending a second request, the two tests above no longer show anything
// — the SDK's behaviour changed and oneRequest should be looked at again.
func TestTheSDKDefaultDoesFollowAContinuation(t *testing.T) {
	srv, calls := continuing(t)
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := v.generateWithRetry(ctx, []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, &genai.GenerateContentConfig{}); err != nil {
		t.Fatalf("generateWithRetry: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("with the SDK's default the endpoint saw %d requests, want 2 — the control no longer holds", n)
	}
}

// Every request configuration this package builds turns the automatic
// continuation off. The behaviour tests reach ChatStream's literal and
// build their own for the one-shot path, so the literals in SearchWeb
// and FetchURL are held here, in the source.
func TestEveryRequestConfigIsOneRequest(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "GenerateContentConfig" {
				return true
			}
			found++
			set := false
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "AutomaticContinuation" {
					continue
				}
				if call, ok := kv.Value.(*ast.CallExpr); ok {
					if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "oneRequest" {
						set = true
					}
				}
			}
			if !set {
				t.Errorf("%s: a GenerateContentConfig without AutomaticContinuation: oneRequest()", fset.Position(lit.Pos()))
			}
			return true
		})
	}
	if found < 3 {
		t.Fatalf("found %d GenerateContentConfig literals, want the 3 known ones — the search no longer sees them", found)
	}
}

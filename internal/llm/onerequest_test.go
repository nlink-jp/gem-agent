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

// The web tools' own entry points, not a config built for the test: a
// literal in SearchWeb or FetchURL that lost the setting, or had it
// cleared after it was built, sends two requests here.
func TestWebToolsDoNotFollowAContinuation(t *testing.T) {
	for name, call := range map[string]func(*Vertex, context.Context) error{
		"SearchWeb": func(v *Vertex, ctx context.Context) error { _, _, _, err := v.SearchWeb(ctx, "q"); return err },
		"FetchURL":  func(v *Vertex, ctx context.Context) error { _, _, _, err := v.FetchURL(ctx, "p"); return err },
	} {
		srv, calls := continuing(t)
		v := localVertex(t, srv, "m")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := call(v, ctx); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		cancel()
		if n := calls.Load(); n != 1 {
			t.Errorf("%s: the endpoint saw %d requests for one call, want 1", name, n)
		}
	}
}

// The stream's control: the SDK's default, through the SDK's own stream
// call, does send the request again. If it stops, the stream test above
// shows nothing.
func TestTheSDKDefaultStreamDoesFollowAContinuation(t *testing.T) {
	srv, calls := continuing(t)
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, err := range v.client.Models.GenerateContentStream(ctx, v.model,
		[]*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}, &genai.GenerateContentConfig{}) {
		if err != nil {
			t.Fatalf("stream: %v", err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("with the SDK's default the endpoint saw %d requests, want 2 — the control no longer holds", n)
	}
}

// events serves, per request, the stream events of the next script; the
// last script repeats.
func events(t *testing.T, scripts ...[]string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		n := int(calls.Add(1))
		script := scripts[min(n, len(scripts))-1]
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range script {
			_, _ = io.WriteString(w, "data: "+ev+"\n\n")
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const (
	evText    = `{"candidates":[{"content":{"role":"model","parts":[{"text":"partial "}]}}]}`
	evThought = `{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true}]}}],"usageMetadata":{"promptTokenCount":50,"totalTokenCount":53}}`
	evDone    = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
	evBusy    = `{"error":{"code":503,"message":"overloaded","status":"UNAVAILABLE"}}`
	evRefused = `{"error":{"code":400,"message":"bad request","status":"INVALID_ARGUMENT"}}`
)

// An error the endpoint sends as a stream event is an error of the call
// (the SDK raises it from v1.76.0; before, it arrived as an empty chunk
// and the call "succeeded" with nothing, or with half an answer). This
// runtime's rules then apply to it as to any other: a transient status
// before any output is retried and recorded; after output it ends the
// call, never passing the half as a whole; a refusal is not retried.
// Held here because it is the SDK's behaviour, not this package's code,
// that makes it so.
func TestAnErrorEventInTheStreamIsAnErrorOfTheCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msgs := []Message{{Role: RoleUser, Content: "hi"}}

	t.Run("a transient status as the first event is retried and recorded", func(t *testing.T) {
		srv, calls := events(t, []string{evBusy}, []string{evDone})
		v := localVertex(t, srv, "m")
		var log retryLog
		v.SetRetryRecorder(log.record)
		resp, err := v.ChatStream(WithSource(ctx, "main"), "", msgs, nil, nil)
		if err != nil || resp.Content != "ok" || calls.Load() != 2 {
			t.Fatalf("content %q, err %v, %d requests; want ok after a retry", resp.Content, err, calls.Load())
		}
		if got := log.events(); len(got) != 1 || got[0].Cause != "503" || got[0].Status != "UNAVAILABLE" {
			t.Errorf("retry events = %+v, want one 503 UNAVAILABLE", got)
		}
	})

	t.Run("after a thought only, it is still retried: nothing was shown", func(t *testing.T) {
		srv, calls := events(t, []string{evThought, evBusy}, []string{evDone})
		v := localVertex(t, srv, "m")
		resp, err := v.ChatStream(ctx, "", msgs, nil, nil)
		if err != nil || resp.Content != "ok" || calls.Load() != 2 {
			t.Fatalf("content %q, err %v, %d requests; want ok after a retry", resp.Content, err, calls.Load())
		}
	})

	t.Run("after text, it ends the call and is not retried", func(t *testing.T) {
		srv, calls := events(t, []string{evText, evBusy}, []string{evDone})
		v := localVertex(t, srv, "m")
		var shown strings.Builder
		resp, err := v.ChatStream(ctx, "", msgs, nil, func(s string) { shown.WriteString(s) })
		if err == nil {
			t.Fatalf("half an answer (%q) came back as a whole one", resp.Content)
		}
		if calls.Load() != 1 {
			t.Errorf("%d requests; a call that already showed %q must not be sent again", calls.Load(), shown.String())
		}
	})

	t.Run("a refusal is an error and is not retried", func(t *testing.T) {
		srv, calls := events(t, []string{evRefused}, []string{evDone})
		v := localVertex(t, srv, "m")
		if _, err := v.ChatStream(ctx, "", msgs, nil, nil); err == nil || calls.Load() != 1 {
			t.Fatalf("err %v, %d requests; want an error after one request", err, calls.Load())
		}
	})
}

// A chunk whose first candidate is null makes SDK v1.77.0 dereference
// it before this package sees the chunk. The call ends as an error; the
// process, and the terminal it is drawing on, survive.
func TestAChunkTheSDKCannotReadEndsTheCallNotTheProcess(t *testing.T) {
	srv, calls := events(t, []string{`{"candidates":[null]}`, evDone})
	v := localVertex(t, srv, "m")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := v.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	switch {
	case err != nil && calls.Load() == 1:
		// The SDK panicked and the call ended as an error, unrepeated.
	case err == nil && resp.Content == "ok":
		// An SDK that reads the chunk needs no guard: also fine.
	default:
		t.Fatalf("content %q, err %v, %d requests; want an error after one request, or the answer", resp.Content, err, calls.Load())
	}
}

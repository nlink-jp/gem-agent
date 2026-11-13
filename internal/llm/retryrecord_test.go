package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genai"
)

// rateLimited answers the first refuse requests with a 429 and the
// rest with one successful response, streamed or whole as the path
// asks. It counts what it was sent.
func rateLimited(t *testing.T, refuse int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"Resource exhausted.","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		const body = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
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

func localVertex(t *testing.T, srv *httptest.Server, model string) *Vertex {
	t.Helper()
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Project:     "p",
		Location:    "global",
		Backend:     genai.BackendVertexAI,
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL + "/"},
		HTTPClient:  srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return newVertex(client, model, "off", "", false)
}

// retryLog collects what the recorder was handed.
type retryLog struct {
	mu  sync.Mutex
	got []RetryEvent
}

func (l *retryLog) record(ev RetryEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, ev)
}

func (l *retryLog) events() []RetryEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]RetryEvent(nil), l.got...)
}

// A stream that waited out a rate limit hands the recorder one event
// per wait: the model asked and the kind of call, the attempt about to
// start, the status that failed the one before, and the backoff. Without
// the record the transcript shows only a call that took longer.
func TestStreamRetryReachesTheRecorder(t *testing.T) {
	srv, calls := rateLimited(t, 1)
	v := localVertex(t, srv, "main-model")
	var log retryLog
	// The record is written before the wait, so its time is when the
	// refused attempt came back: the second request has not been sent.
	var sentWhenRecorded int32
	v.SetRetryRecorder(func(ev RetryEvent) {
		sentWhenRecorded = calls.Load()
		log.record(ev)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := v.ChatStream(WithSource(ctx, "main"), "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q — the retry did not reach the successful answer", resp.Content)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("server saw %d requests, want 2 — the case did not retry once", n)
	}
	got := log.events()
	if len(got) != 1 {
		t.Fatalf("recorder got %d events, want 1: %+v", len(got), got)
	}
	want := RetryEvent{Model: "main-model", Source: "main", Attempt: 2, Max: maxStreamAttempts,
		Cause: "429", Status: "RESOURCE_EXHAUSTED", DelayMS: got[0].DelayMS}
	if got[0] != want {
		t.Errorf("event = %+v, want %+v", got[0], want)
	}
	if min := int((retryBase - retryJitter).Milliseconds()); got[0].DelayMS < min {
		t.Errorf("delay_ms = %d, want at least %d — the record must carry the wait", got[0].DelayMS, min)
	}
	if sentWhenRecorded != 1 {
		t.Errorf("%d requests had been sent when the retry was recorded, want 1 — the record must precede the wait", sentWhenRecorded)
	}
}

// The source is the outermost name a caller gave: a call made on behalf
// of another keeps the name of what started it, and a caller that gave
// none leaves it empty rather than guessed.
func TestRetrySourceIsTheOutermostName(t *testing.T) {
	ctx := context.Background()
	if got := sourceOf(ctx); got != "" {
		t.Errorf("no source = %q", got)
	}
	outer := WithSource(ctx, "agentic_file_search")
	if got := sourceOf(WithSource(outer, "main")); got != "agentic_file_search" {
		t.Errorf("nested source = %q, want the outer one", got)
	}
}

// Only a machine-readable status token is kept from the endpoint's
// answer. Its message, and the text of an error that is not an API
// error at all, can quote back what was sent and never reach the record.
func TestRetryStatusKeepsOnlyAToken(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{genai.APIError{Code: 429, Status: "RESOURCE_EXHAUSTED", Message: "quota for projects/123 exceeded"}, "RESOURCE_EXHAUSTED"},
		{genai.APIError{Code: 503, Status: "UNAVAILABLE"}, "UNAVAILABLE"},
		{genai.APIError{Code: 503, Status: "the key AIza… is not recognised"}, ""},
		{genai.APIError{Code: 503, Status: "lower_case"}, ""},
		{genai.APIError{Code: 503, Status: strings.Repeat("A", 41)}, ""},
		{genai.APIError{Code: 503}, ""},
		{errors.New(`Post "https://example.invalid/v1/projects/p": connection reset`), ""},
		{nil, ""},
	} {
		if got := retryStatus(c.err); got != c.want {
			t.Errorf("retryStatus(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// A slot is a copy of the main backend, made before the recorder is
// installed (cmd/root.go builds the risk slot after NewVertex and the
// recorder is set once). Its retries are the same wait on the same
// quota and must land in the same record, under the slot's own model —
// while the display observer stays the main backend's alone.
func TestSlotRetryReachesTheSameRecorderUnderItsOwnModel(t *testing.T) {
	srv, _ := rateLimited(t, 1)
	main := localVertex(t, srv, "main-model")
	var seen atomic.Int32
	main.SetObserver(func(StreamEvent) { seen.Add(1) })
	slot := main.WithModel("judge").WithThinking("low", "[model].risk_thinking")
	var log retryLog
	main.SetRetryRecorder(log.record) // after the slot exists

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := slot.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	got := log.events()
	if len(got) != 1 || got[0].Model != "judge" || got[0].Cause != "429" {
		t.Fatalf("recorder got %+v, want one 429 event for model judge", got)
	}
	if n := seen.Load(); n != 0 {
		t.Errorf("the slot's stream sent %d events to the main observer — a side call has no audience (ADR-0033 §3)", n)
	}
}

// The one-shot side calls (web search, URL fetch) retry on their own
// loop; that loop reports to the same recorder.
func TestOneShotRetryReachesTheRecorder(t *testing.T) {
	srv, calls := rateLimited(t, 1)
	v := localVertex(t, srv, "web-model")
	var log retryLog
	v.SetRetryRecorder(log.record)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	contents := []*genai.Content{genai.NewContentFromText("hi", genai.RoleUser)}
	if _, err := v.generateWithRetry(WithSource(ctx, SourceWebFetch), contents, &genai.GenerateContentConfig{}); err != nil {
		t.Fatalf("generateWithRetry: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("server saw %d requests, want 2", n)
	}
	got := log.events()
	if len(got) != 1 || got[0].Model != "web-model" || got[0].Source != "web_fetch" || got[0].Attempt != 2 || got[0].Cause != "429" || got[0].Status != "RESOURCE_EXHAUSTED" || got[0].DelayMS <= 0 {
		t.Fatalf("recorder got %+v, want one event: web-model, web_fetch, attempt 2, cause 429, a delay", got)
	}
}

// A call that succeeds first time records nothing, and a backend with
// no recorder — or one built without NewVertex — retries all the same.
func TestNoRetryNoRecordAndNoRecorderIsHarmless(t *testing.T) {
	srv, _ := rateLimited(t, 0)
	v := localVertex(t, srv, "m")
	var log retryLog
	v.SetRetryRecorder(log.record)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := v.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	if got := log.events(); len(got) != 0 {
		t.Errorf("a call that did not retry recorded %+v", got)
	}

	bare := &Vertex{model: "bare"} // no recorder holder at all
	bare.SetRetryRecorder(log.record)
	bare.retried(ctx, 2, genai.APIError{Code: 429}, time.Second)
	if got := log.events(); len(got) != 0 {
		t.Errorf("a backend with no holder recorded %+v", got)
	}

	srv2, calls := rateLimited(t, 1)
	quiet := localVertex(t, srv2, "m") // recorder never set
	if _, err := quiet.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil); err != nil {
		t.Fatalf("ChatStream without a recorder: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("server saw %d requests, want 2 — retrying must not depend on the recorder", n)
	}
}

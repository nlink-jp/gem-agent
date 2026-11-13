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
	"unicode/utf8"

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
// per wait: the model asked, the attempt about to start, the status
// that failed the one before, and the backoff. Without the record the
// transcript shows only a call that took longer.
func TestStreamRetryReachesTheRecorder(t *testing.T) {
	srv, calls := rateLimited(t, 1)
	v := localVertex(t, srv, "main-model")
	var log retryLog
	v.SetRetryRecorder(log.record)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	resp, err := v.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
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
	ev := got[0]
	if ev.Model != "main-model" || ev.Attempt != 2 || ev.Max != maxStreamAttempts || ev.Cause != "429" {
		t.Errorf("event = %+v, want model main-model, attempt 2 of %d, cause 429", ev, maxStreamAttempts)
	}
	if ev.DelayMS <= 0 {
		t.Errorf("delay_ms = %d — the record must carry the wait", ev.DelayMS)
	}
	if !strings.Contains(ev.Error, "Resource exhausted") {
		t.Errorf("error = %q, want the endpoint's own words", ev.Error)
	}
}

// The endpoint's text is kept within a bound and cut on a rune: a
// refusal that quotes a page back must not put it in the transcript.
func TestRetryErrorTextIsBounded(t *testing.T) {
	if got := retryErrorText(nil); got != "" {
		t.Errorf("nil error = %q", got)
	}
	short := errors.New("quota exceeded")
	if got := retryErrorText(short); got != "quota exceeded" {
		t.Errorf("short error = %q", got)
	}
	long := errors.New(strings.Repeat("語", retryErrorRunes+50))
	got := retryErrorText(long)
	if n := len([]rune(got)); n != retryErrorRunes+1 {
		t.Errorf("long error kept %d runes, want %d and the ellipsis", n, retryErrorRunes)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("long error was not cut on a rune with an ellipsis: %q", got[len(got)-8:])
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
	if _, err := v.generateWithRetry(ctx, contents, &genai.GenerateContentConfig{}); err != nil {
		t.Fatalf("generateWithRetry: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("server saw %d requests, want 2", n)
	}
	got := log.events()
	if len(got) != 1 || got[0].Model != "web-model" || got[0].Attempt != 2 || got[0].Cause != "429" || got[0].DelayMS <= 0 {
		t.Fatalf("recorder got %+v, want one event: web-model, attempt 2, cause 429, a delay", got)
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
	bare.retried(2, errors.New("429"), time.Second)
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

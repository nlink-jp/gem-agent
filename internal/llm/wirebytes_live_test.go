//go:build live

package llm

import (
	"context"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/genai"
)

// Follow-up measurement: during the 33s silence of the chunk-gap test,
// does the HTTP body carry bytes (server flushing partial SSE) or is
// the wire genuinely silent? The answer decides whether a byte-level
// heartbeat is possible at all.
//
//	GEM_TEST_PROJECT=<gcp project> go test -tags live -run WireBytes -v ./internal/llm/
type tapTransport struct {
	base  http.RoundTripper
	start time.Time
	t     *testing.T
}

type tapBody struct {
	io.ReadCloser
	tr   *tapTransport
	last time.Time
	n    int
}

func (b *tapBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		now := time.Now()
		b.n++
		b.tr.t.Logf("t=%6.1fs read #%d %d bytes (gap %.1fs)",
			now.Sub(b.tr.start).Seconds(), b.n, n, now.Sub(b.last).Seconds())
		b.last = now
	}
	return n, err
}

func (tr *tapTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := tr.base.RoundTrip(req)
	if err != nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &tapBody{ReadCloser: resp.Body, tr: tr, last: time.Now()}
	return resp, nil
}

func TestWireBytesDuringLargeToolCallLive(t *testing.T) {
	project := os.Getenv("GEM_TEST_PROJECT")
	if project == "" {
		t.Skip("GEM_TEST_PROJECT not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tr := &tapTransport{base: http.DefaultTransport, start: time.Now(), t: t}
	ts, err := google.DefaultTokenSource(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		t.Fatal(err)
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:    project,
		Location:   "global",
		Backend:    genai.BackendVertexAI,
		HTTPClient: &http.Client{Transport: &oauth2.Transport{Base: tr, Source: ts}},
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &genai.GenerateContentConfig{
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true},
		SafetySettings: SafetySettings("off"),
		Tools: convertTools([]ToolDef{{
			Name:        "write_file",
			Description: "Write a file. Provide the COMPLETE file content.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
				},
				"required": []any{"path", "content"},
			},
		}}),
	}
	contents := buildContents([]Message{{Role: RoleUser, Content: "Write /tmp/gemagent-gap-demo.go: a single-file Go program of at least 400 lines implementing a small in-memory key/value store with an HTTP API, TTL expiry, LRU eviction, and thorough doc comments. Call write_file once with the complete content. Do not abbreviate."}})

	chunks := 0
	last := tr.start
	for chunk, err := range client.Models.GenerateContentStream(ctx, "gemini-3.8-flash", contents, cfg) {
		if err != nil {
			t.Fatal(err)
		}
		_ = chunk
		now := time.Now()
		chunks++
		t.Logf("t=%6.1fs CHUNK #%d (gap %.1fs)", now.Sub(tr.start).Seconds(), chunks, now.Sub(last).Seconds())
		last = now
	}
	t.Logf("RESULT: chunks=%d total=%.1fs", chunks, time.Since(tr.start).Seconds())
}

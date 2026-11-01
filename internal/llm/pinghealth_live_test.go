//go:build live

package llm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Measurement for the false stall warning seen in the field on long
// contexts (2026-11-01 session: 8 of 58 main rounds ran 81–210s, at
// 230k–770k prompt tokens, one of them 154s for 54 output + 58 thought
// tokens). ADR-0056 measured only one source of silence — a function
// call arriving whole — and set the warning at 90s from it.
//
// The production client is not a bare http.Client: genai builds it
// through cloud.google.com/go/auth/httptransport, whose base transport
// runs x/net/http2.ConfigureTransports with ReadIdleTimeout = 31s. With
// PingTimeout at its 15s default, an HTTP/2 connection that receives no
// frame for 31s is pinged, and one that does not answer within 15s is
// closed — the stream then fails with an error. If that holds, the
// transport already names a dead connection (~46s), and a "maybe
// stalled" warning at 90s can only ever be shown for a live one.
//
// Pre-registered (committed before any run):
//
//	H1  the production connection to Vertex is HTTP/2 — health-check
//	    lines appear at all.
//	H2  (TestPingHealthLongPrefillLive) during every chunk gap ≥ 46s,
//	    health checks are sent and every one succeeds, and the stream
//	    completes. Refuted by: a health-check failure on a stream that
//	    completed, or a gap ≥ 46s with no health check inside it.
//	    A run whose longest gap is < 46s is INCONCLUSIVE, not a pass.
//	H3  (TestPingHealthBlackholeLive) once the connection is frozen
//	    after the request is sent, ChatStream returns an error within
//	    31s + 15s + 15s slack = 61s of the freeze, preceded by a
//	    health-check failure line. Refuted by: no error by then, or an
//	    error with no health-check failure before it (then something
//	    else detected it, and the attribution is wrong).
//
// Cost cap (estimated before launch, gemini-4-argon at $2/M input):
// long-prefill ≤ 5 runs × ~300k uncached tokens ≈ $3.25; blackhole
// ≤ 2 runs × ~16k tokens ≈ $0.07. These calls go straight to the API,
// so gem-usage-lens does not see them.
//
// Both tests call the shipped constructor (NewVertex) and the shipped
// ChatStream, so the transport measured is the transport that ships.
// The only hook is the dialer of http.DefaultTransport, which the auth
// library clones for its base transport.
//
//	GODEBUG=http2debug=1 GEM_TEST_PROJECT=<project> GEM_TEST_MODEL=<model> \
//	  go test -tags live -run PingHealthLongPrefill -v -timeout 15m ./internal/llm/
//
// http2debug=1 also logs every request header, the bearer token
// included: only whitelisted lines ever leave h2Log.

// h2Log keeps the transport's health-check lines, stamped relative to
// start, and drops everything else the verbose log writes.
type h2Log struct {
	mu    sync.Mutex
	start time.Time
	lines []h2Line
}

type h2Line struct {
	at   time.Duration
	text string
}

var h2Keep = []string{"health check", "connection lost", "GOAWAY", "closing"}

func (l *h2Log) Write(p []byte) (int, error) {
	for _, s := range strings.Split(string(p), "\n") {
		for _, k := range h2Keep {
			if strings.Contains(s, k) && !strings.Contains(strings.ToLower(s), "authorization") {
				l.mu.Lock()
				l.lines = append(l.lines, h2Line{time.Since(l.start), s})
				l.mu.Unlock()
				break
			}
		}
	}
	return len(p), nil
}

func (l *h2Log) startAt() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.start
}

// restart moves the clock origin to the moment the request goes out.
func (l *h2Log) restart() time.Time {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.start = time.Now()
	return l.start
}

func (l *h2Log) snapshot() []h2Line {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]h2Line(nil), l.lines...)
}

func installH2Log(t *testing.T) *h2Log {
	t.Helper()
	if !strings.Contains(os.Getenv("GODEBUG"), "http2debug=1") {
		t.Skip("run with GODEBUG=http2debug=1: the health check is only observable through its log")
	}
	l := &h2Log{start: time.Now()}
	log.SetFlags(0)
	log.SetOutput(l)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return l
}

func liveEnv(t *testing.T) (project, model string) {
	t.Helper()
	project, model = os.Getenv("GEM_TEST_PROJECT"), os.Getenv("GEM_TEST_MODEL")
	if project == "" || model == "" {
		t.Skip("GEM_TEST_PROJECT and GEM_TEST_MODEL must be set")
	}
	return project, model
}

// corpus concatenates this repository's Go sources up to n bytes, behind
// a nonce so no earlier run's prefix can be served from cache.
func corpus(t *testing.T, n int) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "dist" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".go") {
			paths = append(paths, p)
		}
		return nil
	})
	sort.Strings(paths)
	var b strings.Builder
	fmt.Fprintf(&b, "run nonce %d\n\n", time.Now().UnixNano())
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(root, p)
		fmt.Fprintf(&b, "=== %s\n%s\n", rel, data)
		if b.Len() >= n {
			break
		}
	}
	return b.String()[:min(b.Len(), n)]
}

func TestPingHealthLongPrefillLive(t *testing.T) {
	project, model := liveEnv(t)
	h2 := installH2Log(t)
	size := 1_100_000 // ≈ 300k tokens of Go source
	if s := os.Getenv("GEM_TEST_CORPUS_BYTES"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		size = n
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	backend, err := NewVertex(ctx, project, "global", model, "off", "high", true)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var chunks []time.Duration
	backend.SetObserver(func(ev StreamEvent) {
		if ev.Kind == "chunk" {
			mu.Lock()
			chunks = append(chunks, time.Since(h2.startAt()))
			mu.Unlock()
		}
	})
	prompt := corpus(t, size) + "\n\nIn one sentence: what is the value of the constant stallSeconds in this corpus, and in which file is it declared?"
	start := h2.restart()
	resp, err := backend.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: prompt}}, nil, nil)
	end := time.Since(start)
	lines := h2.snapshot()
	for _, l := range lines {
		t.Logf("t=%6.1fs h2: %s", l.at.Seconds(), l.text)
	}
	if err != nil {
		t.Fatalf("stream failed at t=%.1fs: %v", end.Seconds(), err)
	}
	t.Logf("prompt=%d cached=%d output=%d thoughts=%d total=%.1fs answer=%q",
		resp.PromptTokens, resp.CachedTokens, resp.OutputTokens, resp.ThoughtTokens, end.Seconds(), resp.Content)

	// Gaps: from the request to the first chunk, then chunk to chunk.
	mu.Lock()
	marks := append([]time.Duration{0}, chunks...)
	mu.Unlock()
	longest := time.Duration(0)
	for i := 1; i < len(marks); i++ {
		from, to := marks[i-1], marks[i]
		gap := to - from
		longest = max(longest, gap)
		if gap < 46*time.Second {
			continue
		}
		checks, failures := 0, 0
		for _, l := range lines {
			if l.at < from || l.at > to {
				continue
			}
			if strings.Contains(l.text, "sending health check") {
				checks++
			}
			if strings.Contains(l.text, "health check failure") {
				failures++
			}
		}
		t.Logf("GAP %.1fs → %.1fs (%.1fs): health checks sent=%d failures=%d", from.Seconds(), to.Seconds(), gap.Seconds(), checks, failures)
		if checks == 0 {
			t.Errorf("H2 refuted: a %.1fs gap with no health check", gap.Seconds())
		}
		if failures > 0 {
			t.Errorf("H2 refuted: %d health-check failures on a stream that completed", failures)
		}
	}
	t.Logf("RESULT: chunks=%d longest gap=%.1fs", len(chunks), longest.Seconds())
	if longest < 46*time.Second {
		t.Skipf("INCONCLUSIVE: longest gap %.1fs < 46s", longest.Seconds())
	}
}

// relay forwards every dial through a local socket pair so the test can
// freeze one connection: from then on every byte in either direction is
// read and dropped, both sockets stay open, and no write ever blocks —
// what a path whose packets vanish looks like from here.
type relay struct {
	target  string // freeze only connections to this host
	trigger int64  // freeze 2s after this many bytes have gone upstream, so the request is fully out
	frozeAt atomic.Int64
	start   time.Time
}

func (r *relay) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	up, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(addr, r.target) {
		return up, nil
	}
	client, server := net.Pipe()
	frozen := make(chan struct{})
	var once, armed sync.Once
	freeze := func() {
		once.Do(func() {
			r.frozeAt.Store(int64(time.Since(r.start)))
			close(frozen)
		})
	}
	var sent atomic.Int64
	pump := func(dst io.Writer, src io.Reader, upstream bool) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				select {
				case <-frozen:
					continue // dropped
				default:
				}
				if _, werr := io.Copy(dst, bytes.NewReader(buf[:n])); werr != nil {
					return
				}
				if upstream && sent.Add(int64(n)) >= r.trigger {
					armed.Do(func() { time.AfterFunc(2*time.Second, freeze) })
				}
			}
			if err != nil {
				return
			}
		}
	}
	go pump(up, server, true)
	go pump(server, up, false)
	return client, nil
}

func TestPingHealthBlackholeLive(t *testing.T) {
	project, model := liveEnv(t)
	h2 := installH2Log(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	filler := corpus(t, 64<<10)
	r := &relay{target: "aiplatform.googleapis.com", trigger: int64(len(filler)), start: time.Now()}
	dt := http.DefaultTransport.(*http.Transport)
	orig := dt.DialContext
	dt.DialContext = func(dctx context.Context, network, addr string) (net.Conn, error) {
		return r.dial(ctx, network, addr)
	}
	t.Cleanup(func() { dt.DialContext = orig })

	backend, err := NewVertex(ctx, project, "global", model, "off", "high", true)
	if err != nil {
		t.Fatal(err)
	}
	r.start = h2.restart()
	_, err = backend.ChatStream(ctx, "", []Message{{Role: RoleUser, Content: filler + "\n\nSummarize the corpus above in three paragraphs."}}, nil, nil)
	end := time.Since(r.start)
	for _, l := range h2.snapshot() {
		t.Logf("t=%6.1fs h2: %s", l.at.Seconds(), l.text)
	}
	frozeAt := time.Duration(r.frozeAt.Load())
	if frozeAt == 0 {
		t.Fatalf("the relay never froze (err=%v) — the trigger did not fire, the run says nothing", err)
	}
	t.Logf("RESULT: froze at %.1fs, ChatStream returned at %.1fs (%.1fs after the freeze): %v",
		frozeAt.Seconds(), end.Seconds(), (end - frozeAt).Seconds(), err)
	if err == nil {
		t.Fatal("H3 refuted: the stream completed through a frozen connection — the freeze did not hold")
	}
	if ctx.Err() != nil {
		t.Fatal("H3 refuted: nothing detected the frozen connection before the 3-minute cap")
	}
	if end-frozeAt > 61*time.Second {
		t.Errorf("H3 refuted: detection took %.1fs > 61s", (end - frozeAt).Seconds())
	}
	failed := false
	for _, l := range h2.snapshot() {
		if strings.Contains(l.text, "health check failure") && l.at <= end {
			failed = true
		}
	}
	if !failed {
		t.Error("H3 refuted: the error was not preceded by a health-check failure — something else detected it")
	}
}

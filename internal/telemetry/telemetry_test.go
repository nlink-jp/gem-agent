package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	collogpb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	"google.golang.org/protobuf/proto"
)

// recorder is an in-memory sdklog.Exporter.
type recorder struct {
	mu   sync.Mutex
	recs []sdklog.Record
}

func (r *recorder) Export(_ context.Context, records []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		r.recs = append(r.recs, rec.Clone())
	}
	return nil
}
func (r *recorder) Shutdown(context.Context) error   { return nil }
func (r *recorder) ForceFlush(context.Context) error { return nil }

func recordingSink(t *testing.T) (*Sink, *recorder) {
	t.Helper()
	rec := &recorder{}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(rec)))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return &Sink{logger: provider.Logger("test"), provider: provider}, rec
}

func attrsOf(rec sdklog.Record) map[string]string {
	out := map[string]string{}
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		out[string(kv.Key)] = kv.Value.String()
		return true
	})
	return out
}

// The nil sink (telemetry disabled) is free and safe on every method —
// call sites never branch (ADR-0035 §4).
func TestNopSinkIsSafe(t *testing.T) {
	var s *Sink
	s.SessionStart("m", true, false, 0)
	s.SessionEnd()
	s.TurnEnd(1, time.Second, "ok")
	s.ToolCall("t", true, "d", "why", time.Second, "ok")
	s.Approval("t", "approved", "gate", false, "")
	s.Usage(1, 2, 3, 4, 0, 10)
	s.Compaction(1, 2)
	s.MediaUpload(1, "gs://x")
	s.Shutdown()
	Nop().SessionEnd()
}

// Every event lands with its name and audit attributes.
func TestEventsCarryAuditAttributes(t *testing.T) {
	s, rec := recordingSink(t)
	s.ToolCall("shell_exec", true, "command=make build", "why", 1500*time.Millisecond, "ok")
	s.Approval("write_file", "denied", "gate", true, "blocked by rule")
	s.TurnEnd(3, 2*time.Second, "interrupted")

	if len(rec.recs) != 3 {
		t.Fatalf("records = %d, want 3", len(rec.recs))
	}
	tool := rec.recs[0]
	if tool.EventName() != "tool.call" {
		t.Errorf("event name = %q", tool.EventName())
	}
	a := attrsOf(tool)
	if a["tool"] != "shell_exec" || a["detail"] != "command=make build" ||
		a["outcome"] != "ok" || a["duration_ms"] != "1500" || a["mutating"] != "true" {
		t.Errorf("tool.call attrs = %v", a)
	}
	appr := attrsOf(rec.recs[1])
	if appr["decision"] != "denied" || appr["source"] != "gate" || appr["must_prompt"] != "true" {
		t.Errorf("approval attrs = %v", appr)
	}
	turn := attrsOf(rec.recs[2])
	if turn["rounds"] != "3" || turn["outcome"] != "interrupted" {
		t.Errorf("turn.end attrs = %v", turn)
	}
}

// Metadata, not payloads (ADR-0035 §3): long details are clipped.
func TestDetailIsClipped(t *testing.T) {
	s, rec := recordingSink(t)
	s.ToolCall("shell_exec", true, strings.Repeat("x", 1000), "why", time.Second, "ok")
	a := attrsOf(rec.recs[0])
	if len([]rune(a["detail"])) > 301 {
		t.Errorf("detail not clipped: %d runes", len([]rune(a["detail"])))
	}
}

// The wire E2E: a real otlploghttp exporter posts protobuf the
// collector contract accepts — decoded from an actual HTTP request.
func TestOTLPHTTPWireFormat(t *testing.T) {
	got := make(chan *collogpb.ExportLogsServiceRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req collogpb.ExportLogsServiceRequest
		if err := proto.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal: %v", err)
		}
		select {
		case got <- &req:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	endpoint := strings.TrimPrefix(srv.URL, "http://")
	sink, err := New(context.Background(), Config{
		Enabled: true, Backend: "otlp-http", Endpoint: endpoint, Insecure: true,
	}, "", "v-test", "sess-1", "/proj")
	if err != nil {
		t.Fatal(err)
	}
	sink.SessionStart("gemini-test", true, false, 2)
	sink.Shutdown()

	select {
	case req := <-got:
		if len(req.ResourceLogs) == 0 {
			t.Fatal("no resource logs")
		}
		var haveSession, haveEvent bool
		for _, attr := range req.ResourceLogs[0].Resource.Attributes {
			if attr.Key == "session.id" && attr.Value.GetStringValue() == "sess-1" {
				haveSession = true
			}
		}
		for _, sl := range req.ResourceLogs[0].ScopeLogs {
			for _, lr := range sl.LogRecords {
				if lr.EventName == "session.start" {
					haveEvent = true
				}
			}
		}
		if !haveSession || !haveEvent {
			t.Errorf("wire payload missing session resource or event: session=%v event=%v", haveSession, haveEvent)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no OTLP export arrived")
	}
}

// The gcp backend's record→entry mapping: event name plus attributes
// as a structured payload.
func TestEntryFromRecord(t *testing.T) {
	sink, capture := recordingSink(t)
	sink.ToolCall("shell_exec", true, "make build", "why", 1200*time.Millisecond, "ok")
	entries := capture.recs
	if len(entries) != 1 {
		t.Fatalf("records = %d", len(entries))
	}
	e := entryFromRecord(entries[0])
	payload, ok := e.Payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type %T", e.Payload)
	}
	if payload["event"] != "tool.call" || payload["tool"] != "shell_exec" ||
		payload["duration_ms"] != int64(1200) || payload["mutating"] != true {
		t.Errorf("payload = %v", payload)
	}
}

// The auth-header file (operator report: env-var-only auth breaks
// under launchd/cron/fresh shells): 0600 required, flat JSON, and the
// headers must actually ride the wire.
func TestHeadersFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/auth.json"

	// Group/other-readable credentials are refused, naming the fix.
	_ = os.WriteFile(path, []byte(`{"Authorization":"Bearer tok"}`), 0o644)
	if _, err := loadHeaders(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("world-readable headers file accepted: %v", err)
	}
	_ = os.Chmod(path, 0o600)
	h, err := loadHeaders(path)
	if err != nil || h["Authorization"] != "Bearer tok" {
		t.Fatalf("headers = %v, %v", h, err)
	}
	// Absent config = nil, no error (env fallback path).
	if h, err := loadHeaders(""); h != nil || err != nil {
		t.Errorf("unset headers_file: %v %v", h, err)
	}
	// Garbage is named as such.
	_ = os.WriteFile(path, []byte("not json"), 0o600)
	if _, err := loadHeaders(path); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Errorf("garbage accepted: %v", err)
	}
}

func TestHeadersRideTheWire(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case gotAuth <- r.Header.Get("Authorization"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	path := dir + "/auth.json"
	_ = os.WriteFile(path, []byte(`{"Authorization":"Bearer file-token"}`), 0o600)

	sink, err := New(context.Background(), Config{
		Enabled: true, Backend: "otlp-http",
		Endpoint: strings.TrimPrefix(srv.URL, "http://"), Insecure: true,
		HeadersFile: path,
	}, "", "v", "s", "/p")
	if err != nil {
		t.Fatal(err)
	}
	sink.SessionStart("m", true, false, 0)
	sink.Shutdown()
	select {
	case auth := <-gotAuth:
		if auth != "Bearer file-token" {
			t.Errorf("Authorization = %q, want the file's token", auth)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no export arrived")
	}
}

// ADR-0066 §3: the audit stream carries the same buckets as the
// transcript record, tool_prompt included, so a fleet-wide figure from
// Cloud Logging uses the same four-term arithmetic as a local one.
func TestUsageEventCarriesEveryBucket(t *testing.T) {
	s, rec := recordingSink(t)
	s.Usage(1200, 900, 40, 0, 7000, 9140)
	if len(rec.recs) != 1 {
		t.Fatalf("records = %d, want 1", len(rec.recs))
	}
	attrs := attrsOf(rec.recs[0])
	want := map[string]string{
		"prompt_tokens": "1200", "output_tokens": "900", "thought_tokens": "40",
		"cached_tokens": "0", "tool_prompt_tokens": "7000", "total_tokens": "9140",
	}
	for k, v := range want {
		if attrs[k] != v {
			t.Errorf("%s = %q, want %q", k, attrs[k], v)
		}
	}
}

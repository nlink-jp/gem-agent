package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/riskbook"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// draftBackend answers the summarizer call with a scripted draft and
// captures what it was shown.
type draftBackend struct {
	draft   string
	err     error
	system  string
	payload string
}

func (b *draftBackend) ChatStream(ctx context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	b.system = system
	if len(msgs) > 0 {
		b.payload = msgs[0].Content
	}
	if b.err != nil {
		return nil, b.err
	}
	return &llm.Response{Content: b.draft}, nil
}

// riskbookFixture seeds one session with typed decisions and builds a
// runner wired to isolated state.
func riskbookFixture(t *testing.T, b *draftBackend, answer func() (bool, error)) (*riskbookRunner, string, *[]string, *[]map[string]any) {
	t.Helper()
	t.Setenv("GEMAGENT_STATE_DIR", t.TempDir())
	dir := t.TempDir()
	proj := "/work/proj"
	var sb strings.Builder
	add := func(kind string, data any) {
		rec, err := json.Marshal(map[string]any{"ts": "2026-08-26T10:00:00Z", "kind": kind, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		sb.Write(rec)
		sb.WriteByte('\n')
	}
	add(session.KindHeader, map[string]any{"schema": session.SchemaVersion, "version": "t", "model": "m", "project": proj})
	add(session.KindMessage, map[string]any{"role": "user", "content": "go"})
	add("gate_decision", map[string]any{"name": "mcp__asn__lookup_ip", "decision": "approved",
		"key": "mcp__asn__lookup_ip", "detail": "lookup_ip 203.0.113.7", "source": "operator"})
	add("auto_decision", map[string]any{"name": "mcp__asn__lookup_ip", "approved": false, "key": "mcp__asn__lookup_ip"})
	if err := os.WriteFile(filepath.Join(dir, "20260826-100000.jsonl"), []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	var emitted []string
	var records []map[string]any
	r := &riskbookRunner{
		cfgPath: cfgPath, sessionsDir: dir, projectDir: proj,
		backend: b, modelName: "test-lite", langName: "English",
		msgs: uitext.For(uitext.EN),
		apply: func() (riskbook.Book, error) {
			return riskbook.Load(cfgPath, proj)
		},
		ask:  func(ctx context.Context, q, accept, discard string) (bool, error) { return answer() },
		emit: func(lines []string) { emitted = append(emitted, lines...) },
		record: func(kind string, data any) {
			m, _ := data.(map[string]any)
			records = append(records, map[string]any{"kind": kind, "data": m})
		},
		now: func() time.Time { return time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC) },
	}
	return r, proj, &emitted, &records
}

// The pipeline: the summarizer sees the wrapped enumeration; the stored
// file is byte-for-byte what was shown at review, provenance included.
func TestRiskbookLearnAcceptStoresWhatWasReviewed(t *testing.T) {
	b := &draftBackend{draft: "mcp__asn__lookup_ip: you approved this pattern; correct toward approval (1 typed approval, 1 escalation)."}
	r, proj, emitted, records := riskbookFixture(t, b, func() (bool, error) { return true, nil })
	r.Learn(context.Background())

	// The enumeration reached the summarizer, nonce-wrapped.
	if !strings.Contains(b.payload, "pattern: mcp__asn__lookup_ip") {
		t.Errorf("summarizer did not see the enumeration: %q", b.payload)
	}
	if !strings.Contains(b.payload, "<decision_record_") {
		t.Errorf("enumeration not nonce-wrapped: %q", b.payload)
	}
	if !strings.Contains(b.system, "UNTRUSTED DATA") {
		t.Errorf("summarizer prompt lacks the defensive framing: %q", b.system)
	}

	path, err := riskbook.ProjectPath(proj)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("nothing stored: %v", err)
	}
	if !strings.Contains(string(stored), "2026-08-26") || !strings.Contains(string(stored), b.draft) {
		t.Errorf("stored = %q", stored)
	}
	// Byte-identity with the review: every stored line was emitted.
	joined := strings.Join(*emitted, "\n")
	for _, line := range strings.Split(string(stored), "\n") {
		if line != "" && !strings.Contains(joined, line) {
			t.Errorf("stored line was never shown at review: %q", line)
		}
	}
	if len(*records) == 0 || (*records)[0]["kind"] != "riskbook_update" {
		t.Errorf("no riskbook_update record: %v", *records)
	}
}

// Discard stores nothing: a draft is an offer.
func TestRiskbookLearnDiscardStoresNothing(t *testing.T) {
	b := &draftBackend{draft: "some notes"}
	r, proj, _, _ := riskbookFixture(t, b, func() (bool, error) { return false, nil })
	r.Learn(context.Background())
	path, _ := riskbook.ProjectPath(proj)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a discarded draft was stored")
	}
}

// A declined dialog stops the pass and stores nothing.
func TestRiskbookLearnDeclineStops(t *testing.T) {
	b := &draftBackend{draft: "some notes"}
	r, proj, emitted, _ := riskbookFixture(t, b, func() (bool, error) { return false, errors.New("declined") })
	r.Learn(context.Background())
	path, _ := riskbook.ProjectPath(proj)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a declined pass stored a draft")
	}
	if !strings.Contains(strings.Join(*emitted, "\n"), "stopped") {
		t.Errorf("a stopped pass did not say so: %v", *emitted)
	}
}

// No decisions on record is a normal outcome that teaches both routes.
func TestRiskbookLearnNoData(t *testing.T) {
	b := &draftBackend{draft: "x"}
	r, _, emitted, _ := riskbookFixture(t, b, func() (bool, error) { return true, nil })
	r.sessionsDir = t.TempDir() // nothing recorded
	r.Learn(context.Background())
	out := strings.Join(*emitted, "\n")
	if !strings.Contains(out, "no gate decisions") || !strings.Contains(out, "risk-rules.md") {
		t.Errorf("no-data message missing or unteaching: %v", out)
	}
	if b.payload != "" {
		t.Error("the summarizer was called with no decisions on record")
	}
}

// show / reload / clear answer through the synchronous subcommand path.
func TestRiskbookCommandShowReloadClear(t *testing.T) {
	b := &draftBackend{draft: "notes"}
	r, proj, _, _ := riskbookFixture(t, b, func() (bool, error) { return true, nil })

	out, isErr := r.Command(nil)
	if isErr || !strings.Contains(out, "no risk rules in force") {
		t.Errorf("empty show = %q (err=%v)", out, isErr)
	}
	if err := os.WriteFile(riskbook.BasePath(r.cfgPath), []byte("network installs need eyes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := riskbook.SaveProject(proj, "writes under /data are routine"); err != nil {
		t.Fatal(err)
	}
	out, isErr = r.Command([]string{"show"})
	if isErr || !strings.Contains(out, "network installs need eyes") || !strings.Contains(out, "/data are routine") {
		t.Errorf("show = %q", out)
	}
	if out, isErr = r.Command([]string{"reload"}); isErr || !strings.Contains(out, "reloaded") {
		t.Errorf("reload = %q (err=%v)", out, isErr)
	}
	if out, isErr = r.Command([]string{"clear"}); isErr || !strings.Contains(out, "removed") {
		t.Errorf("clear = %q (err=%v)", out, isErr)
	}
	path, _ := riskbook.ProjectPath(proj)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("clear left the project layer in place")
	}
	if out, _ = r.Command([]string{"clear"}); !strings.Contains(out, "no project risk rules") {
		t.Errorf("second clear = %q", out)
	}
	if out, isErr = r.Command([]string{"bogus"}); !isErr || !strings.Contains(out, "usage:") {
		t.Errorf("unknown subcommand = %q (err=%v)", out, isErr)
	}
}

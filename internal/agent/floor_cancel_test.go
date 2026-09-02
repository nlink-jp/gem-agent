package agent

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// ADR-0065 §2: the return-guaranteed floor. A tool that ignores its
// context must not wedge the turn; a tool that honours it inside the
// grace keeps its (partial) result.

// safeLog is recordingLog with a mutex: the late-return record is
// written from the abandoned goroutine, concurrently with the test.
type safeLog struct {
	mu    sync.Mutex
	kinds []string
}

func (l *safeLog) Log(kind string, data any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.kinds = append(l.kinds, kind)
	return nil
}

func (l *safeLog) has(kind string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, k := range l.kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func newFloorTestAgent(t *testing.T, backend llm.Backend, extra *tools.Tool, log SessionLog, sink *telemetry.Sink) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/true") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(extra); err != nil {
		t.Fatal(err)
	}
	return New(Options{
		Backend: backend, Registry: reg, Gate: &approveAll{}, System: "sys",
		MaxTurns: 3, Log: log, Telemetry: sink,
	})
}

func callThen(name string, final string) *mockBackend {
	return &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: name, Args: map[string]any{}}}},
		{Content: final},
	}}
}

func lastToolResult(t *testing.T, a *Agent) string {
	t.Helper()
	for i := len(a.history) - 1; i >= 0; i-- {
		if a.history[i].Role == llm.RoleTool {
			return a.history[i].Content
		}
	}
	t.Fatal("no tool result in history")
	return ""
}

func toolOutcome(t *testing.T, rec *telemetry.Recording, tool string) string {
	t.Helper()
	for _, e := range rec.Events() {
		if e.Name == "tool.call" && e.Attrs["tool"] == tool {
			return e.Attrs["outcome"]
		}
	}
	t.Fatalf("no tool.call event for %s", tool)
	return ""
}

func TestFloorAbandonsToolThatIgnoresCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	stuck := &tools.Tool{
		Name: "stuck", Description: "blocks until released, ignores ctx",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			started <- struct{}{}
			<-release // a blocking syscall on a hung mount, in miniature
			return "late result", nil
		},
	}
	sink, rec := telemetry.NewRecording()
	log := &safeLog{}
	a := newFloorTestAgent(t, callThen("stuck", "done"), stuck, log, sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = a.Run(ctx, "go", nil)
		close(done)
	}()
	<-started
	cancel()
	t0 := time.Now()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never returned — the floor is missing")
	}
	if lag := time.Since(t0); lag > 3*time.Second {
		t.Errorf("returned after %s; the grace is %s", lag, abandonGrace)
	}
	if got := lastToolResult(t, a); got != abandonedResult {
		t.Errorf("tool result = %q, want the abandoned notice", got)
	}
	if got := toolOutcome(t, rec, "stuck"); got != "abandoned" {
		t.Errorf("audit outcome = %q, want abandoned", got)
	}
	if log.has("tool_late_return") {
		t.Fatal("late-return record written before the tool returned")
	}
	// The abandoned goroutine eventually returns: the audit trail gets
	// the record, and nothing else changes.
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for !log.has("tool_late_return") {
		if time.Now().After(deadline) {
			t.Fatal("no tool_late_return record after the abandoned call returned")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFloorKeepsCooperativeResultReturnedWithinGrace(t *testing.T) {
	started := make(chan struct{}, 1)
	polite := &tools.Tool{
		Name: "polite", Description: "returns a partial result on cancel",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			started <- struct{}{}
			<-ctx.Done()
			return "3 matches [interrupted after 12 files scanned — results above are partial]", nil
		},
	}
	sink, rec := telemetry.NewRecording()
	log := &safeLog{}
	a := newFloorTestAgent(t, callThen("polite", "done"), polite, log, sink)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = a.Run(ctx, "go", nil)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never returned")
	}
	if got := lastToolResult(t, a); !strings.Contains(got, "partial") {
		t.Errorf("cooperative partial result lost: %q", got)
	}
	if got := toolOutcome(t, rec, "polite"); got != "interrupted" {
		t.Errorf("audit outcome = %q, want interrupted", got)
	}
	if log.has("tool_late_return") {
		t.Error("a cooperative return must not be recorded as late")
	}
}

// Without a cancel the floor is invisible: the result and the outcome
// are exactly what they were before ADR-0065.
func TestFloorIsInvisibleOnAnOrdinaryCall(t *testing.T) {
	quick := &tools.Tool{
		Name: "quick", Description: "returns at once",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return "fast", nil
		},
	}
	sink, rec := telemetry.NewRecording()
	a := newFloorTestAgent(t, callThen("quick", "done"), quick, &safeLog{}, sink)
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got := lastToolResult(t, a); got != "fast" {
		t.Errorf("tool result = %q", got)
	}
	if got := toolOutcome(t, rec, "quick"); got != "ok" {
		t.Errorf("audit outcome = %q, want ok", got)
	}
}

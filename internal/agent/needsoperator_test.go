package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

type retryKey struct{}

// probeQuestion is a tool owner's question as the agent sees it.
type probeQuestion struct {
	resolves *int
	// answer is what Resolve returns: a retry context when again is
	// true, a final result otherwise.
	again  bool
	result string
}

func (q *probeQuestion) Error() string { return "needs the operator" }
func (q *probeQuestion) Resolve(ctx context.Context) (context.Context, string) {
	*q.resolves++
	if !q.again {
		return nil, q.result
	}
	return context.WithValue(ctx, retryKey{}, *q.resolves), ""
}
func (q *probeQuestion) StillUnresolved() string { return "still unresolved" }

func probeAgent(t *testing.T, run func(ctx context.Context) (string, error)) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(&tools.Tool{Name: "probe", Description: "d",
		Parameters: map[string]any{"type": "object"},
		Run:        func(ctx context.Context, _ map[string]any) (string, error) { return run(ctx) }}); err != nil {
		t.Fatal(err)
	}
	return New(Options{Registry: reg, Gate: &approveAll{}})
}

// ADR-0119 §8: a tool that cannot go on until the operator answers says
// so; the agent has the question put and runs the tool once more under
// the context the answer returned.
func TestNeedsOperatorIsResolvedThenTheToolRunsAgain(t *testing.T) {
	resolves, runs := 0, 0
	a := probeAgent(t, func(ctx context.Context) (string, error) {
		runs++
		if ctx.Value(retryKey{}) == nil {
			return "", &probeQuestion{resolves: &resolves, again: true}
		}
		return "loaded", nil
	})
	out, denied, _, _ := a.execCall(context.Background(), llm.ToolCall{ID: "c", Name: "probe"})
	if out != "loaded" || denied || resolves != 1 || runs != 2 {
		t.Errorf("out=%q denied=%v resolves=%d runs=%d", out, denied, resolves, runs)
	}
}

// An answer that is not "go on" is the call's result, as the owner
// wrote it, and the tool does not run again.
func TestNeedsOperatorRefusalIsTheResult(t *testing.T) {
	resolves, runs := 0, 0
	a := probeAgent(t, func(context.Context) (string, error) {
		runs++
		return "", &probeQuestion{resolves: &resolves, result: "the operator disabled it"}
	})
	out, _, _, _ := a.execCall(context.Background(), llm.ToolCall{ID: "c", Name: "probe"})
	if out != "the operator disabled it" || resolves != 1 || runs != 1 {
		t.Errorf("out=%q resolves=%d runs=%d", out, resolves, runs)
	}
}

// What the question was about may move while the operator answers. The
// call asks once more and then stops: it cannot keep a dialog coming
// for as long as something keeps changing.
func TestNeedsOperatorIsBounded(t *testing.T) {
	resolves, runs := 0, 0
	a := probeAgent(t, func(context.Context) (string, error) {
		runs++
		return "", &probeQuestion{resolves: &resolves, again: true}
	})
	out, _, _, _ := a.execCall(context.Background(), llm.ToolCall{ID: "c", Name: "probe"})
	if out != "still unresolved" || resolves != tools.OperatorResolves || runs != tools.OperatorResolves+1 {
		t.Errorf("out=%q resolves=%d runs=%d", out, resolves, runs)
	}
}

// A turn that was cancelled asks nothing: no dialog on behalf of a dead
// call.
func TestNeedsOperatorAsksNothingForACancelledTurn(t *testing.T) {
	resolves := 0
	ctx, cancel := context.WithCancel(context.Background())
	a := probeAgent(t, func(context.Context) (string, error) {
		cancel()
		return "", &probeQuestion{resolves: &resolves, again: true}
	})
	out, _, _, _ := a.execCall(ctx, llm.ToolCall{ID: "c", Name: "probe"})
	if resolves != 0 {
		t.Errorf("the question was put for a cancelled turn (out=%q)", out)
	}
}

// An ordinary error is still an ordinary error.
func TestAnOrdinaryErrorIsNotAQuestion(t *testing.T) {
	a := probeAgent(t, func(context.Context) (string, error) { return "", errors.New("boom") })
	out, _, _, _ := a.execCall(context.Background(), llm.ToolCall{ID: "c", Name: "probe"})
	if out != "error: boom" {
		t.Errorf("out=%q", out)
	}
}

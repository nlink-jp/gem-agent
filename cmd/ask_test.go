package cmd

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

func askRegistry(t *testing.T, ask askFunc) *tools.Registry {
	t.Helper()
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := registerAskTool(reg, ask); err != nil {
		t.Fatal(err)
	}
	return reg
}

func runAsk(t *testing.T, reg *tools.Registry, args map[string]any) (string, error) {
	t.Helper()
	tool, ok := reg.Get("ask_user")
	if !ok {
		t.Fatal("ask_user not registered")
	}
	return tool.Run(context.Background(), args)
}

// ADR-0036: the tool is read-only (a gate on a question would be a
// dialog to permit a dialog), the choice comes back named, and a
// decline is information, not an error.
func TestAskUserTool(t *testing.T) {
	var gotQ string
	var gotOpts []string
	reg := askRegistry(t, func(_ context.Context, q string, opts []string) (int, error) {
		gotQ, gotOpts = q, opts
		return 1, nil
	})
	tool, _ := reg.Get("ask_user")
	if tool.Mutating {
		t.Error("ask_user must be read-only")
	}
	out, err := runAsk(t, reg, map[string]any{
		"question": "どの方式にしますか？",
		"options":  []any{"A 案", "B 案", "C 案"},
	})
	if err != nil || out != "The user chose: B 案" {
		t.Errorf("out=%q err=%v", out, err)
	}
	if gotQ != "どの方式にしますか？" || len(gotOpts) != 3 {
		t.Errorf("asker got %q %v", gotQ, gotOpts)
	}
}

func TestAskUserDeclineAndBounds(t *testing.T) {
	reg := askRegistry(t, func(context.Context, string, []string) (int, error) {
		return 0, errAskDeclined
	})
	out, err := runAsk(t, reg, map[string]any{
		"question": "q", "options": []any{"a", "b"}})
	if err != nil || !strings.Contains(out, "declined to choose") {
		t.Errorf("decline: %q %v", out, err)
	}

	if _, err := runAsk(t, reg, map[string]any{"question": "q", "options": []any{"only"}}); err == nil {
		t.Error("one option accepted")
	}
	many := make([]any, 9)
	for i := range many {
		many[i] = "x"
	}
	if _, err := runAsk(t, reg, map[string]any{"question": "q", "options": many}); err == nil ||
		!strings.Contains(err.Error(), "too many") {
		t.Errorf("9 options accepted: %v", err)
	}
	if _, err := runAsk(t, reg, map[string]any{"options": []any{"a", "b"}}); err == nil {
		t.Error("missing question accepted")
	}
}

// One-shot mode has nobody to ask — a pipeline must not hang.
func TestOneShotAskRefusesInformatively(t *testing.T) {
	_, err := oneShotAsk(context.Background(), "q", []string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "one-shot") {
		t.Errorf("err = %v", err)
	}
}

// Plain-REPL asker: numbered prompt, a number answers, empty and
// garbage decline (the approve.Gate discipline: fail toward the
// harmless outcome).
func TestPlainAsk(t *testing.T) {
	var out strings.Builder
	ask := plainAsk(bufio.NewReader(strings.NewReader("2\n")), &out)
	idx, err := ask(context.Background(), "which?", []string{"a", "b", "c"})
	if err != nil || idx != 1 {
		t.Errorf("idx=%d err=%v", idx, err)
	}
	if !strings.Contains(out.String(), "1) a") || !strings.Contains(out.String(), "which?") {
		t.Errorf("prompt = %q", out.String())
	}
	for _, in := range []string{"\n", "junk\n", "9\n", ""} {
		ask := plainAsk(bufio.NewReader(strings.NewReader(in)), &strings.Builder{})
		if _, err := ask(context.Background(), "q", []string{"a", "b"}); err != errAskDeclined {
			t.Errorf("input %q: err = %v, want decline", in, err)
		}
	}
}

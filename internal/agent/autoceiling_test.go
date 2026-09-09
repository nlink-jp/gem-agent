package agent

// The auto state tightens the ceiling and never loosens it
// (ADR-0080 §2).

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// ceilingBackend answers the ceiling question with a fixed verdict and
// records the prompts it was asked with.
type ceilingBackend struct {
	verdict   string
	err       error
	systems   []string
	payloads  []string
	turnReply *llm.Response
}

func (b *ceilingBackend) ChatStream(_ context.Context, system string, msgs []llm.Message, defs []llm.ToolDef, _ func(string)) (*llm.Response, error) {
	if len(defs) == 0 && strings.Contains(system, "changes nothing") {
		b.systems = append(b.systems, system)
		if len(msgs) > 0 {
			b.payloads = append(b.payloads, msgs[len(msgs)-1].Content)
		}
		if b.err != nil {
			return nil, b.err
		}
		return &llm.Response{Content: b.verdict}, nil
	}
	if b.turnReply != nil {
		return b.turnReply, nil
	}
	return &llm.Response{Content: "done"}, nil
}

func autoCeilingAgent(t *testing.T, b llm.Backend, state string) (*Agent, *[]string) {
	t.Helper()
	return autoCeilingAgentIn(t, b, state, uitext.EN)
}

func autoCeilingAgentIn(t *testing.T, b llm.Backend, state string, lang uitext.Lang) (*Agent, *[]string) {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	a := New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s",
		MaxTurns: 3, Ceiling: ceilingFor(state), Msgs: uitext.For(lang),
		OnNotice: func(m string) { notices = append(notices, m) }})
	return a, &notices
}

func TestAutoCeilingTightensAndSaysWhy(t *testing.T) {
	// Both languages: the operator reads one of them, and only one of
	// them was ever checked.
	for _, tc := range []struct {
		lang   uitext.Lang
		change string
	}{
		{uitext.EN, "now read-only"},
		{uitext.JA, "読み取り専用モードに切り替えました"},
	} {
		b := &ceilingBackend{verdict: `{"read_only": true, "quote": "変更はしないで"}`}
		a, notices := autoCeilingAgentIn(t, b, "auto", tc.lang)
		if _, err := a.Run(context.Background(), "レビューして。変更はしないで", nil); err != nil {
			t.Fatal(err)
		}
		if !a.CeilingState().ReadOnly {
			t.Fatalf("%v: state = %+v, want read-only", tc.lang, a.CeilingState())
		}
		if len(*notices) != 1 {
			t.Fatalf("%v: notices = %v", tc.lang, *notices)
		}
		// Cause, change, way back — the three things that make a printed
		// line worth printing, and nothing beyond them.
		n := (*notices)[0]
		for _, want := range []string{tc.change, "変更はしないで", "/readonly off"} {
			if !strings.Contains(n, want) {
				t.Errorf("%v: notice lacks %q: %q", tc.lang, want, n)
			}
		}
		// The operator's message is wrapped as data, not handed over as
		// instructions.
		if len(b.payloads) != 1 || strings.HasPrefix(b.payloads[0], "レビューして") {
			t.Errorf("%v: the message was not isolated: %q", tc.lang, b.payloads)
		}
	}
}

func TestAutoCeilingDoesNotTightenOnOrdinaryWork(t *testing.T) {
	b := &ceilingBackend{verdict: `{"read_only": false}`}
	a, notices := autoCeilingAgent(t, b, "auto")
	if _, err := a.Run(context.Background(), "テストが落ちるので直して", nil); err != nil {
		t.Fatal(err)
	}
	if a.CeilingState().ReadOnly {
		t.Errorf("state = %+v, want auto", a.CeilingState())
	}
	if len(*notices) != 0 {
		t.Errorf("said something with nothing to say: %v", *notices)
	}
}

// Off spends no tokens: the default costs nothing. On is already
// tightened and has nothing to decide.
func TestAutoCeilingRunsOnlyInTheAutoState(t *testing.T) {
	for _, state := range []string{"off", "on", ""} {
		b := &ceilingBackend{verdict: `{"read_only": true, "quote": "x"}`}
		a, _ := autoCeilingAgent(t, b, state)
		if _, err := a.Run(context.Background(), "レビューして。変更はしないで", nil); err != nil {
			t.Fatal(err)
		}
		if len(b.systems) != 0 {
			t.Errorf("state %q asked the ceiling question anyway", state)
		}
	}
}

// A failed inference is not evidence of anything, and it must not
// loosen: the state stays exactly where it was.
func TestAutoCeilingFailureLeavesTheStateAlone(t *testing.T) {
	for _, b := range []*ceilingBackend{
		{err: context.DeadlineExceeded},
		{verdict: "not json"},
	} {
		a, notices := autoCeilingAgent(t, b, "auto")
		if _, err := a.Run(context.Background(), "レビューして", nil); err != nil {
			t.Fatal(err)
		}
		if a.CeilingState().ReadOnly {
			t.Errorf("state = %+v, want auto", a.CeilingState())
		}
		if len(*notices) != 0 {
			t.Errorf("notices = %v", *notices)
		}
	}
}

// Nothing in the runtime loosens the ceiling: once tightened, a later
// turn that reads as ordinary work leaves it on. And the watcher does
// not even run while the ceiling is in force — there is nothing left
// for it to decide, and the earlier version of this test passed
// whether or not that guard existed (independent review, 2026-09-09).
func TestAutoCeilingNeverLoosens(t *testing.T) {
	b := &ceilingBackend{verdict: `{"read_only": true, "quote": "見るだけ"}`}
	a, _ := autoCeilingAgent(t, b, "auto")
	if _, err := a.Run(context.Background(), "見るだけにして", nil); err != nil {
		t.Fatal(err)
	}
	if c := a.CeilingState(); !c.ReadOnly || !c.Auto {
		t.Fatalf("state = %+v, want the ceiling up and the watcher still armed", c)
	}
	rounds := len(b.systems)
	if rounds != 1 {
		t.Fatalf("watcher rounds = %d, want 1", rounds)
	}

	// Already in force: the watcher has nothing to decide and spends
	// nothing, whatever the next turn says.
	b.verdict = `{"read_only": false}`
	if _, err := a.Run(context.Background(), "やっぱり直して", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.systems) != rounds {
		t.Errorf("the watcher ran again with the ceiling already up: %d rounds", len(b.systems))
	}
	if c := a.CeilingState(); !c.ReadOnly {
		t.Errorf("the runtime loosened the ceiling by itself: %+v", c)
	}

	// And after the operator lifts it, the watcher is still armed and
	// runs again — a lift releases the ceiling, not the watcher.
	a.SetReadOnly(false, "operator")
	b.verdict = `{"read_only": true, "quote": "確認だけ"}`
	if _, err := a.Run(context.Background(), "確認だけして", nil); err != nil {
		t.Fatal(err)
	}
	if len(b.systems) != rounds+1 {
		t.Errorf("the watcher did not run after the lift: %d rounds", len(b.systems))
	}
	if c := a.CeilingState(); !c.ReadOnly || !c.Auto {
		t.Errorf("state after the lift and a new request = %+v", c)
	}
}

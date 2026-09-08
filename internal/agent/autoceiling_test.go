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
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
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
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/bash", "-c", c) },
		5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var notices []string
	a := New(Options{Backend: b, Registry: reg, Gate: &recordingGate{}, System: "s",
		MaxTurns: 3, ReadOnly: state,
		OnNotice: func(m string) { notices = append(notices, m) }})
	return a, &notices
}

func TestAutoCeilingTightensAndSaysWhy(t *testing.T) {
	b := &ceilingBackend{verdict: `{"read_only": true, "quote": "変更はしないで"}`}
	a, notices := autoCeilingAgent(t, b, sandbox.CeilingAuto)
	if _, err := a.Run(context.Background(), "レビューして。変更はしないで", nil); err != nil {
		t.Fatal(err)
	}
	if a.ReadOnly() != sandbox.CeilingOn {
		t.Fatalf("state = %q, want on", a.ReadOnly())
	}
	if len(*notices) != 1 {
		t.Fatalf("notices = %v", *notices)
	}
	// The change, its cause in the operator's own words, and the way
	// back — the three things that make a printed line worth printing.
	n := (*notices)[0]
	for _, want := range []string{"read-only", "変更はしないで", "/readonly off"} {
		if !strings.Contains(n, want) {
			t.Errorf("notice lacks %q: %q", want, n)
		}
	}
	// The operator's message is wrapped as data, not handed over as
	// instructions.
	if len(b.payloads) != 1 || strings.HasPrefix(b.payloads[0], "レビューして") {
		t.Errorf("the message was not isolated: %q", b.payloads)
	}
}

func TestAutoCeilingDoesNotTightenOnOrdinaryWork(t *testing.T) {
	b := &ceilingBackend{verdict: `{"read_only": false}`}
	a, notices := autoCeilingAgent(t, b, sandbox.CeilingAuto)
	if _, err := a.Run(context.Background(), "テストが落ちるので直して", nil); err != nil {
		t.Fatal(err)
	}
	if a.ReadOnly() != sandbox.CeilingAuto {
		t.Errorf("state = %q, want auto", a.ReadOnly())
	}
	if len(*notices) != 0 {
		t.Errorf("said something with nothing to say: %v", *notices)
	}
}

// Off spends no tokens: the default costs nothing. On is already
// tightened and has nothing to decide.
func TestAutoCeilingRunsOnlyInTheAutoState(t *testing.T) {
	for _, state := range []string{sandbox.CeilingOff, sandbox.CeilingOn, ""} {
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
		a, notices := autoCeilingAgent(t, b, sandbox.CeilingAuto)
		if _, err := a.Run(context.Background(), "レビューして", nil); err != nil {
			t.Fatal(err)
		}
		if a.ReadOnly() != sandbox.CeilingAuto {
			t.Errorf("state = %q, want auto", a.ReadOnly())
		}
		if len(*notices) != 0 {
			t.Errorf("notices = %v", *notices)
		}
	}
}

// Nothing in the runtime loosens the ceiling: once tightened, a later
// turn that reads as ordinary work leaves it on.
func TestAutoCeilingNeverLoosens(t *testing.T) {
	b := &ceilingBackend{verdict: `{"read_only": true, "quote": "見るだけ"}`}
	a, _ := autoCeilingAgent(t, b, sandbox.CeilingAuto)
	if _, err := a.Run(context.Background(), "見るだけにして", nil); err != nil {
		t.Fatal(err)
	}
	if a.ReadOnly() != sandbox.CeilingOn {
		t.Fatalf("state = %q", a.ReadOnly())
	}
	b.verdict = `{"read_only": false}`
	if _, err := a.Run(context.Background(), "やっぱり直して", nil); err != nil {
		t.Fatal(err)
	}
	if a.ReadOnly() != sandbox.CeilingOn {
		t.Errorf("the runtime loosened the ceiling by itself: %q", a.ReadOnly())
	}
}

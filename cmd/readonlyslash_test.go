package cmd

// /readonly both shows the state and sets it, so its text has to
// describe a state rather than a transition: "the session may change
// things again" is a lie when nothing changed. Operator report,
// 2026-09-09 — the command shipped without a test, and this is it.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func readOnlyAgent(t *testing.T, state string) *agent.Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, c string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/echo", "x") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return agent.New(agent.Options{Registry: reg, System: "s", MaxTurns: 3, ReadOnly: state})
}

func readOnlySlash(t *testing.T, a *agent.Agent, input string, lang uitext.Lang) string {
	t.Helper()
	out, isErr, _ := slashOutput(input, a, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(lang), nil)
	if isErr {
		t.Fatalf("%s reported an error: %q", input, out)
	}
	return out
}

// Showing must not claim a change. Every state's text is used on both
// paths, so none of them may be written as a transition.
func TestReadonlyShowingReportsTheStateWithoutClaimingAChange(t *testing.T) {
	transitions := []string{"戻りました", "again", "now on", "now off", "switched", "切り替えました"}
	for _, lang := range []uitext.Lang{uitext.JA, uitext.EN} {
		for _, state := range []string{sandbox.CeilingOff, sandbox.CeilingOn, sandbox.CeilingAuto} {
			a := readOnlyAgent(t, state)
			out := readOnlySlash(t, a, "/readonly", lang)
			// The token the operator actually reads, not a case-folded
			// match that "/readonly off" inside another state's line
			// would also satisfy.
			if !strings.Contains(out, strings.ToUpper(state)) {
				t.Errorf("%v/%s: the line does not name the state: %q", lang, state, out)
			}
			for _, verb := range transitions {
				if strings.Contains(out, verb) {
					t.Errorf("%v/%s: showing the state claims a change (%q): %q", lang, state, verb, out)
				}
			}
			// Showing changes nothing.
			if a.ReadOnly() != state {
				t.Errorf("%v/%s: showing moved the state to %q", lang, state, a.ReadOnly())
			}
		}
	}
}

func TestReadonlySetsEachState(t *testing.T) {
	for _, want := range []string{sandbox.CeilingOn, sandbox.CeilingAuto, sandbox.CeilingOff} {
		a := readOnlyAgent(t, sandbox.CeilingOff)
		out := readOnlySlash(t, a, "/readonly "+want, uitext.JA)
		if a.ReadOnly() != want {
			t.Errorf("/readonly %s → state %q", want, a.ReadOnly())
		}
		if !strings.Contains(strings.ToUpper(out), strings.ToUpper(want)) {
			t.Errorf("/readonly %s: the line does not confirm the state: %q", want, out)
		}
	}
}

// The ON line names the way back, because that is the state where the
// operator may need it and the one they may not have set themselves.
func TestReadonlyOnNamesTheWayBack(t *testing.T) {
	for _, lang := range []uitext.Lang{uitext.JA, uitext.EN} {
		a := readOnlyAgent(t, sandbox.CeilingOn)
		if out := readOnlySlash(t, a, "/readonly", lang); !strings.Contains(out, "/readonly off") {
			t.Errorf("%v: ON does not name the way back: %q", lang, out)
		}
	}
}

func TestReadonlyRejectsAnUnknownStateWithoutChangingAnything(t *testing.T) {
	a := readOnlyAgent(t, sandbox.CeilingOn)
	out := readOnlySlash(t, a, "/readonly maybe", uitext.EN)
	if !strings.Contains(out, "/readonly on|off|auto") {
		t.Errorf("no usage line: %q", out)
	}
	if a.ReadOnly() != sandbox.CeilingOn {
		t.Errorf("an unknown state moved the ceiling to %q", a.ReadOnly())
	}
}

package cmd

// /readonly shows two settings and sets either without touching the
// other (ADR-0080 §1). Its text describes a state rather than a
// transition, because showing must not claim a change — operator
// report, 2026-09-09, twice.

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

func readOnlyAgent(t *testing.T, c sandbox.Ceiling) *agent.Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, cmd string) *exec.Cmd { return exec.CommandContext(ctx, "/bin/echo", "x") },
		time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return agent.New(agent.Options{Registry: reg, System: "s", MaxTurns: 3, Ceiling: c})
}

func readOnlySlash(t *testing.T, a *agent.Agent, input string, lang uitext.Lang) string {
	t.Helper()
	out, isErr, _ := slashOutput(input, a, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(lang), nil)
	if isErr {
		t.Fatalf("%s reported an error: %q", input, out)
	}
	return out
}

// Showing must not claim a change, in either language, and must not
// move anything.
func TestReadonlyShowsWithoutClaimingAChange(t *testing.T) {
	transitions := []string{"戻りました", "again", "switched", "切り替えました"}
	for _, lang := range []uitext.Lang{uitext.JA, uitext.EN} {
		for _, c := range []sandbox.Ceiling{
			{}, {ReadOnly: true}, {Auto: true}, {ReadOnly: true, Auto: true},
		} {
			a := readOnlyAgent(t, c)
			out := readOnlySlash(t, a, "/readonly", lang)
			want := "OFF"
			if c.ReadOnly {
				want = "ON"
			}
			if !strings.Contains(out, want) {
				t.Errorf("%v/%+v: the line does not name the ceiling: %q", lang, c, out)
			}
			// The watcher line appears only when armed: off is the
			// default and its absence says the same thing.
			if got := strings.Count(out, "\n") > 1; got != c.Auto {
				t.Errorf("%v/%+v: watcher line present = %v: %q", lang, c, got, out)
			}
			for _, verb := range transitions {
				if strings.Contains(out, verb) {
					t.Errorf("%v/%+v: showing claims a change (%q): %q", lang, c, verb, out)
				}
			}
			if a.CeilingState() != c {
				t.Errorf("%v/%+v: showing moved the state to %+v", lang, c, a.CeilingState())
			}
		}
	}
}

// Each argument moves one setting and leaves the other alone. A first
// draft made them one tri-state, so `off` disarmed a watcher nobody
// asked to give up.
func TestReadonlySetsEitherSettingIndependently(t *testing.T) {
	cases := []struct {
		input string
		start sandbox.Ceiling
		want  sandbox.Ceiling
	}{
		{"/readonly on", sandbox.Ceiling{Auto: true}, sandbox.Ceiling{ReadOnly: true, Auto: true}},
		{"/readonly off", sandbox.Ceiling{ReadOnly: true, Auto: true}, sandbox.Ceiling{Auto: true}},
		{"/readonly auto", sandbox.Ceiling{ReadOnly: true}, sandbox.Ceiling{ReadOnly: true, Auto: true}},
		{"/readonly auto on", sandbox.Ceiling{}, sandbox.Ceiling{Auto: true}},
		{"/readonly auto off", sandbox.Ceiling{ReadOnly: true, Auto: true}, sandbox.Ceiling{ReadOnly: true}},
	}
	for _, tc := range cases {
		a := readOnlyAgent(t, tc.start)
		readOnlySlash(t, a, tc.input, uitext.JA)
		if got := a.CeilingState(); got != tc.want {
			t.Errorf("%q from %+v → %+v, want %+v", tc.input, tc.start, got, tc.want)
		}
	}
}

// The ON line names the way back: it is the state an operator may not
// have set themselves, since the watcher can turn it on.
func TestReadonlyOnNamesTheWayBack(t *testing.T) {
	for _, lang := range []uitext.Lang{uitext.JA, uitext.EN} {
		a := readOnlyAgent(t, sandbox.Ceiling{ReadOnly: true})
		if out := readOnlySlash(t, a, "/readonly", lang); !strings.Contains(out, "/readonly off") {
			t.Errorf("%v: ON does not name the way back: %q", lang, out)
		}
	}
}

func TestReadonlyRejectsUnknownArgumentsWithoutChangingAnything(t *testing.T) {
	for _, input := range []string{"/readonly maybe", "/readonly on auto", "/readonly auto maybe"} {
		start := sandbox.Ceiling{ReadOnly: true, Auto: true}
		a := readOnlyAgent(t, start)
		out := readOnlySlash(t, a, input, uitext.EN)
		if !strings.Contains(out, "/readonly on|off") {
			t.Errorf("%q: no usage line: %q", input, out)
		}
		if a.CeilingState() != start {
			t.Errorf("%q moved the state to %+v", input, a.CeilingState())
		}
	}
}

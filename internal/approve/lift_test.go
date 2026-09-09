package approve

// The plain REPL's mode question (ADR-0080 §4). It had no test at all,
// including the one that matters: 'a' is not an answer here, because a
// mode is not a call (independent review, 2026-09-09).

import (
	"bufio"
	"strings"
	"testing"
)

func liftGate(t *testing.T, input string) (*Gate, *strings.Builder) {
	t.Helper()
	var out strings.Builder
	return &Gate{in: bufio.NewReader(strings.NewReader(input)), out: &out, always: map[string]bool{}}, &out
}

func TestApproveLiftAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		want        bool
		wantReason  string
	}{
		{"yes", "y\n", true, ""},
		{"no", "n\n", false, ""},
		{"empty is no", "\n", false, ""},
		{"reasoned no", "N\nnot while I am reviewing\n", false, "not while I am reviewing"},
		// Fails closed: no input is not consent to remove a restriction.
		{"eof", "", false, ""},
		// 'a' is an answer about a tool. Here it is not an answer at
		// all, so the gate re-asks rather than treating it as yes.
		{"a is re-asked, then denied", "a\nn\n", false, ""},
		{"p is re-asked, then denied", "p\nn\n", false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, out := liftGate(t, tc.input)
			ok, reason := g.ApproveLift("write_file", "path=x", "why", "capped at the read lane")
			if ok != tc.want || reason != tc.wantReason {
				t.Errorf("got (%v, %q), want (%v, %q)", ok, reason, tc.want, tc.wantReason)
			}
			if !strings.Contains(out.String(), "read-only") {
				t.Errorf("the prompt does not say what is being decided: %q", out.String())
			}
		})
	}
}

// Whatever is typed, no allowlist entry is created: the ceiling exists
// to withhold exactly that.
func TestApproveLiftRegistersNoAllowlist(t *testing.T) {
	for _, input := range []string{"y\n", "a\ny\n", "p\ny\n", "n\n"} {
		g, _ := liftGate(t, input)
		g.ApproveLift("write_file", "path=x", "why", "capped")
		if len(g.always) != 0 {
			t.Errorf("input %q registered %v", input, g.always)
		}
	}
}

// And it consults none either: a tool already allowlisted still asks,
// because the question is about the session, not the tool.
func TestApproveLiftConsultsNoAllowlist(t *testing.T) {
	g, out := liftGate(t, "n\n")
	g.always["write_file"] = true
	if ok, _ := g.ApproveLift("write_file", "path=x", "why", "capped"); ok {
		t.Error("an allowlisted tool answered the mode question")
	}
	if !strings.Contains(out.String(), "read-only") {
		t.Errorf("no prompt was shown: %q", out.String())
	}
}

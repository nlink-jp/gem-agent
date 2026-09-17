package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// TestShowSlashDrawsWhatTheOperatorNamed is ADR-0091 §3. The operator typed
// the path, which is why this route exists at all: the symlink hazard that
// rules out a runtime-chosen path does not apply to one the operator wrote,
// and it is the same trust line `@<image>` already carries.
func TestShowSlashDrawsWhatTheOperatorNamed(t *testing.T) {
	var asked string
	reloads := slashReloads{show: func(path string) (string, bool) {
		asked = path
		return "", false
	}}
	out, isErr, _ := slashOutput("/show ~/Desktop/shot one.png", nil, nil, nil, nil, reloads, nil, nil, nil, "", uitext.For(uitext.EN), nil)
	if isErr {
		t.Errorf("/show reported an error: %q", out)
	}
	// The whole remainder is the path: an operator's path may hold spaces.
	if asked != "~/Desktop/shot one.png" {
		t.Errorf("path handed on as %q", asked)
	}
	if out != "" {
		t.Errorf("a drawn picture is the output; the command printed %q", out)
	}
}

// TestShowSlashSaysWhyNot: a refusal is the one thing that must reach the
// operator in words, because the screen itself stays silent (ADR-0089).
func TestShowSlashSaysWhyNot(t *testing.T) {
	reloads := slashReloads{show: func(string) (string, bool) {
		return "not shown: this terminal cannot draw inline images", true
	}}
	out, isErr, _ := slashOutput("/show x.png", nil, nil, nil, nil, reloads, nil, nil, nil, "", uitext.For(uitext.EN), nil)
	if !isErr || !strings.Contains(out, "cannot draw") {
		t.Errorf("refusal not reported: %q (isErr=%v)", out, isErr)
	}

	// No argument names the shape rather than failing as unknown.
	out, isErr, _ = slashOutput("/show", nil, nil, nil, nil, reloads, nil, nil, nil, "", uitext.For(uitext.EN), nil)
	if !isErr || !strings.Contains(out, "<path>") {
		t.Errorf("bare /show should name its argument: %q", out)
	}

	// Where there is no route at all — the plain REPL — it reads as an
	// unknown command rather than as a promise the entrance cannot keep.
	out, isErr, _ = slashOutput("/show x.png", nil, nil, nil, nil, slashReloads{}, nil, nil, nil, "", uitext.For(uitext.EN), nil)
	if !isErr || strings.Contains(out, "not shown") {
		t.Errorf("unwired /show should be unknown, got %q", out)
	}
}

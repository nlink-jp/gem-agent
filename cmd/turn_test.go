package cmd

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestRunTurnSuccess(t *testing.T) {
	if err := runTurn(context.Background(), func(ctx context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

// TestRunTurnRealErrorNotMasked is the regression test for the smoke-test
// bug: signal.NotifyContext's stop() cancels the context, so an
// after-stop check reported every backend error (e.g. model 404) as
// "(interrupted)".
func TestRunTurnRealErrorNotMasked(t *testing.T) {
	backendErr := errors.New("vertex AI: model not found")
	err := runTurn(context.Background(), func(ctx context.Context) error { return backendErr })
	if !errors.Is(err, backendErr) {
		t.Fatalf("real error was masked: got %v", err)
	}
	if errors.Is(err, errInterrupted) {
		t.Fatal("real error misclassified as interrupt")
	}
}

func TestAbbreviateHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := abbreviateHome(home + "/works/demo"); got != "~/works/demo" {
		t.Errorf("got %q", got)
	}
	if got := abbreviateHome(home); got != "~" {
		t.Errorf("home itself = %q", got)
	}
	if got := abbreviateHome("/opt/other"); got != "/opt/other" {
		t.Errorf("non-home path altered: %q", got)
	}
	// A directory whose name merely starts with the home path (a
	// "<home>backup" sibling) must not be abbreviated.
	if got := abbreviateHome(home + "backup/x"); got != home+"backup/x" {
		t.Errorf("prefix-sibling path altered: %q", got)
	}
}

func TestDenyGateAlwaysDenies(t *testing.T) {
	var buf strings.Builder
	g := denyGate{out: &buf}
	if allowed(g.Approve("shell_exec", "rm -rf /", "", "", false)) {
		t.Fatal("one-shot gate must deny mutating tools")
	}
	if !strings.Contains(buf.String(), "one-shot") {
		t.Errorf("denial should explain itself: %q", buf.String())
	}
	// The generic line must name the remedies (ADR-0053 §3).
	for _, remedy := range []string{"--auto", "--allow"} {
		if !strings.Contains(buf.String(), remedy) {
			t.Errorf("denial should name %s: %q", remedy, buf.String())
		}
	}
}

// --allow entries merge into the global policy scope at flag precedence
// with the [approval.tools] vocabulary (ADR-0053 §2).
func TestApplyAllowFlag(t *testing.T) {
	merged := map[string]string{
		"write_file": "always", // from a config file — the flag outranks it
		"read_file":  "never",
	}
	err := applyAllowFlag(merged, []string{"write_file", " mcp__slack__send_message ", "mcp__lookup__*"})
	if err != nil {
		t.Fatalf("valid entries rejected: %v", err)
	}
	for pattern, want := range map[string]string{
		"write_file":               "never", // flag precedence
		"mcp__slack__send_message": "never", // trimmed
		"mcp__lookup__*":           "never",
		"read_file":                "never", // untouched
	} {
		if merged[pattern] != want {
			t.Errorf("merged[%q] = %q, want %q", pattern, merged[pattern], want)
		}
	}
	// The one entry that means too much stays unreachable, and the
	// error names the flag, not a config table.
	err = applyAllowFlag(map[string]string{}, []string{"*"})
	if err == nil || !strings.Contains(err.Error(), "--allow") {
		t.Errorf(`bare "*" must fail naming --allow, got: %v`, err)
	}
	if err := applyAllowFlag(map[string]string{}, []string{"mcp__*__x"}); err == nil {
		t.Error("inner wildcard must be rejected")
	}
	if err := applyAllowFlag(map[string]string{}, []string{"  "}); err == nil {
		t.Error("blank entry must be rejected")
	}
}

// A reason handed to the gate — a ladder escalation's cause, a Block
// verdict — is the denial's story, not something to swallow (ADR-0053 §3).
func TestDenyGateShowsTheReason(t *testing.T) {
	var buf strings.Builder
	g := denyGate{out: &buf}
	if allowed(g.Approve("write_file", "path=/tmp/x", "", "writes outside the project", true)) {
		t.Fatal("one-shot gate must deny even with a reason")
	}
	if !strings.Contains(buf.String(), "writes outside the project") {
		t.Errorf("escalation reason lost: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "one-shot") {
		t.Errorf("denial should still say which mode refused: %q", buf.String())
	}
}

// The one-shot contract of ADR-0053: config arms interactive sessions
// only, the flag arms any mode.
func TestEffectiveAuto(t *testing.T) {
	for _, tc := range []struct {
		cfgAuto, oneShot, flagAuto, want bool
	}{
		{false, false, false, false},
		{true, false, false, true}, // interactive: config stands
		{true, true, false, false}, // one-shot ignores config
		{false, true, true, true},  // one-shot armed by the flag only
		{true, true, true, true},   // flag wins regardless of config
		{false, false, true, true}, // interactive --auto = start in auto
	} {
		if got := effectiveAuto(tc.cfgAuto, tc.oneShot, tc.flagAuto); got != tc.want {
			t.Errorf("effectiveAuto(cfg=%v, oneShot=%v, flag=%v) = %v, want %v",
				tc.cfgAuto, tc.oneShot, tc.flagAuto, got, tc.want)
		}
	}
}

func TestRunTurnCancellationIsInterrupt(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	err := runTurn(parent, func(ctx context.Context) error {
		cancel() // simulates SIGINT arriving mid-turn
		<-ctx.Done()
		return ctx.Err()
	})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("cancellation should map to errInterrupted, got %v", err)
	}
}

func allowed(approved, _ bool) bool { return approved }

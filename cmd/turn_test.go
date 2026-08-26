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
	if g.Approve("shell_exec", "rm -rf /", "", "", false) {
		t.Fatal("one-shot gate must deny mutating tools")
	}
	if !strings.Contains(buf.String(), "one-shot") {
		t.Errorf("denial should explain itself: %q", buf.String())
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

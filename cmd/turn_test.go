package cmd

import (
	"context"
	"errors"
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

func TestDenyGateAlwaysDenies(t *testing.T) {
	var buf strings.Builder
	g := denyGate{out: &buf}
	if g.Approve("shell_exec", "rm -rf /") {
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

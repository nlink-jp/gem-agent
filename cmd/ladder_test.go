package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

// ADR-0065 §3: the three-press ladder outside the TUI.

func TestLadderStepShape(t *testing.T) {
	for n, want := range map[int]string{1: "cancel", 2: "warn", 3: "quit", 4: "quit"} {
		if got := ladderStep(n); got != want {
			t.Errorf("press %d = %s, want %s", n, got, want)
		}
	}
}

// Real SIGINTs to the test process: the first cancels the turn, the
// second warns, the third quits — and none of them is swallowed the
// way signal.NotifyContext swallowed presses two onward.
func TestRunTurnLadderCancelsWarnsQuits(t *testing.T) {
	// Registering our own channel first disables the default action
	// (process death) for the whole test binary while we raise SIGINT.
	guard := make(chan os.Signal, 8)
	signal.Notify(guard, os.Interrupt)
	defer signal.Stop(guard)

	armed := make(chan struct{}, 1)
	interrupting := make(chan struct{}, 1)
	warned := make(chan struct{}, 1)
	quit := make(chan struct{}, 1)
	ladder := &interruptLadder{
		Armed:        func() { armed <- struct{}{} },
		Interrupting: func() { interrupting <- struct{}{} },
		Warn:         func() { warned <- struct{}{} },
		Quit:         func() { quit <- struct{}{} },
	}
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runTurnWith(context.Background(), ladder, func(ctx context.Context) error {
			<-ctx.Done()
			cancelled <- struct{}{}
			<-release // the wedged tool: cancelled, still not returning
			return ctx.Err()
		})
	}()
	raise := func() {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(ch <-chan struct{}, what string) {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("no %s after SIGINT", what)
		}
	}
	wait(armed, "arming")
	raise()
	wait(cancelled, "cancel")
	wait(interrupting, "interrupting notice")
	raise()
	wait(warned, "warning")
	raise()
	wait(quit, "quit")
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, errInterrupted) {
			t.Errorf("turn error = %v, want errInterrupted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("turn did not return after release")
	}
}

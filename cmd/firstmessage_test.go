package cmd

import (
	"strings"
	"testing"
)

// The positional argument is the first interactive turn (ADR-0064):
// whitespace-only counts as absent, and combining it with -p is a
// named refusal, never a silent precedence.
func TestFirstMessage(t *testing.T) {
	if got, err := firstMessage(nil, false); got != "" || err != nil {
		t.Errorf("no args: got %q, %v", got, err)
	}
	if got, err := firstMessage([]string{"  \n "}, false); got != "" || err != nil {
		t.Errorf("whitespace arg: got %q, %v", got, err)
	}
	if got, err := firstMessage([]string{"  run the tests  "}, false); got != "run the tests" || err != nil {
		t.Errorf("arg: got %q, %v", got, err)
	}
	_, err := firstMessage([]string{"run the tests"}, true)
	if err == nil {
		t.Fatal("-p plus a first message must be refused")
	}
	if !strings.Contains(err.Error(), "-p") || !strings.Contains(err.Error(), "interactive") {
		t.Errorf("refusal must name both meanings: %v", err)
	}
	// A whitespace-only argument beside -p is simply absent, not a
	// conflict.
	if _, err := firstMessage([]string{"   "}, true); err != nil {
		t.Errorf("whitespace arg with -p: %v", err)
	}
}

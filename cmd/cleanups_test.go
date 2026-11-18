package cmd

import (
	"strings"
	"testing"
)

// The stack runs last-pushed first, once, and traces each step.
func TestCleanupsRunLastFirstOnce(t *testing.T) {
	var ran, traced []string
	c := &cleanups{trace: func(n string) { traced = append(traced, n) }}
	for _, n := range []string{"a", "b", "c"} {
		c.push(n, func() { ran = append(ran, n) })
	}
	c.run()
	c.run()
	if got := strings.Join(ran, ""); got != "cba" {
		t.Fatalf("ran %q, want cba once", got)
	}
	if got := strings.Join(traced, ""); got != "cba" {
		t.Fatalf("traced %q", got)
	}
}

// A step that panics does not skip the ones after it — what separate
// defers did — and the panic still reaches the caller.
func TestCleanupsPanicRunsTheRest(t *testing.T) {
	var ran []string
	c := &cleanups{}
	c.push("first", func() { ran = append(ran, "first") })
	c.push("boom", func() { panic("boom") })
	c.push("last", func() { ran = append(ran, "last") })
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic was swallowed")
			}
		}()
		c.run()
	}()
	if got := strings.Join(ran, ","); got != "last,first" {
		t.Fatalf("ran %q, want last,first", got)
	}
}

// What was never pushed is never run: an early failure undoes only what
// was set up.
func TestCleanupsOnlyWhatWasPushed(t *testing.T) {
	var ran []string
	c := &cleanups{}
	c.push("set up", func() { ran = append(ran, "set up") })
	c.run()
	if len(ran) != 1 {
		t.Fatalf("ran %v", ran)
	}
}

// Final steps run after every pushed step, in the order pushed, however
// early they were pushed.
func TestCleanupsFinalStepsRunLast(t *testing.T) {
	var ran []string
	c := &cleanups{}
	c.push("a", func() { ran = append(ran, "a") })
	c.pushFinal("f1", func() { ran = append(ran, "f1") })
	c.push("b", func() { ran = append(ran, "b") })
	c.pushFinal("f2", func() { ran = append(ran, "f2") })
	c.run()
	if got := strings.Join(ran, ","); got != "b,a,f1,f2" {
		t.Fatalf("ran %q, want b,a,f1,f2", got)
	}
}

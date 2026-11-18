package cmd

// cleanups is the session's teardown, pushed by each part as it sets
// something up and run once when the session ends (ADR-0124 §2.5). It
// replaces the defers runREPL used to register across 1,200 lines: the
// order is still last-pushed first, an early failure still undoes only
// what was set up, and each step still runs under its own defer, so a
// step that panics does not skip the ones after it.
type cleanups struct {
	steps []cleanupStep
	// trace hears each step's name as it starts; the characterization
	// tests read the exit sequence through it. nil in the binary.
	trace func(name string)
}

type cleanupStep struct {
	name string
	fn   func()
}

func (c *cleanups) push(name string, fn func()) {
	c.steps = append(c.steps, cleanupStep{name: name, fn: fn})
}

// run runs every pushed step, last first, once.
func (c *cleanups) run() {
	steps := c.steps
	c.steps = nil
	for _, s := range steps {
		defer func() {
			if c.trace != nil {
				c.trace(s.name)
			}
			s.fn()
		}()
	}
}

// cleanupTrace, when set, is handed to the session's cleanups. Only the
// characterization tests set it.
var cleanupTrace func(name string)

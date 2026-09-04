package hooks

import (
	"testing"
	"time"
)

// Review round 4: a hook whose background child keeps stdout open must
// not hold the session past the hook's own exit — WaitDelay bounds the
// wait for the inherited pipe.
func TestGrandchildHoldingStdoutDoesNotStallTheHook(t *testing.T) {
	h := Hook{Matcher: "*", Command: `sleep 20 & echo done`, Timeout: 500 * time.Millisecond}
	start := time.Now()
	deny, _, _ := run(t, h, "shell_exec", nil)
	if deny {
		t.Error("the hook denied")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the hook took %s — the grandchild's pipe held it", took)
	}
}

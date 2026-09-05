package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// Review after v0.68.2: the timeout kills the hook's process group, so
// a child the hook started does not survive it as an orphan.
func TestTimeoutKillsTheHooksChildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	h := Hook{Matcher: "*", Command: `sleep 30 & echo $! > ` + pidFile + `; wait`, Timeout: 300 * time.Millisecond}
	run(t, h, "shell_exec", nil)
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("the hook did not record its child's pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := syscall.Kill(pid, 0); err == nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the hook's child (pid %d) outlived the timeout", pid)
	}
}

// ADR-0072 §4.5: a hook's output is bounded as it arrives.
func TestHookOutputIsBounded(t *testing.T) {
	h := Hook{Matcher: "*", Command: `head -c 5000000 /dev/zero | tr '\0' 'a'; echo`, Timeout: 5 * time.Second}
	r := New(Hooks{PreToolUse: []Hook{h}}, nil)
	out, ran := r.exec(context.Background(), h, t.TempDir(), map[string]any{"x": 1})
	if !ran {
		t.Fatal("hook did not run")
	}
	if len(out.stdout) > hookStdoutCap {
		t.Fatalf("stdout held %d bytes; the cap is %d", len(out.stdout), hookStdoutCap)
	}
}

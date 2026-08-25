package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func run(t *testing.T, h Hook, name string, args map[string]any) (bool, string, []string) {
	t.Helper()
	var notes []string
	r := New([]Hook{h}, func(s string) { notes = append(notes, s) })
	deny, why := r.Pre(context.Background(), name, t.TempDir(), args)
	return deny, why, notes
}

// The org's guard denies via stdout JSON with exit 0 — the measured
// contract, not the documented one (ADR-0044 context).
func TestDenyViaHookSpecificOutputJSON(t *testing.T) {
	h := Hook{Matcher: "shell_exec", Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"relative path"}}'`}
	deny, why, _ := run(t, h, "shell_exec", map[string]any{"command": "sed -i x y"})
	if !deny || !strings.Contains(why, "relative path") {
		t.Fatalf("deny=%v why=%q", deny, why)
	}
}

// The older top-level decision form also denies.
func TestDenyViaDecisionBlock(t *testing.T) {
	h := Hook{Matcher: "*", Command: `echo '{"decision":"block","reason":"nope"}'`}
	deny, why, _ := run(t, h, "write_file", nil)
	if !deny || why != "nope" {
		t.Fatalf("deny=%v why=%q", deny, why)
	}
}

// Exit code 2 denies with stderr as the reason.
func TestDenyViaExit2(t *testing.T) {
	h := Hook{Matcher: "shell_exec", Command: `echo "stop that" >&2; exit 2`}
	deny, why, _ := run(t, h, "shell_exec", nil)
	if !deny || why != "stop that" {
		t.Fatalf("deny=%v why=%q", deny, why)
	}
}

// Matchers speak both vocabularies: a hooks block copied from Claude
// Code settings ("Bash") matches gem-agent's shell_exec (ADR-0044 §4).
func TestMatcherAcceptsClaudeCodeNames(t *testing.T) {
	h := Hook{Matcher: "Bash", Command: `echo '{"decision":"block","reason":"x"}'`}
	if deny, _, _ := run(t, h, "shell_exec", nil); !deny {
		t.Error("Bash matcher did not cover shell_exec")
	}
	if deny, _, _ := run(t, h, "list_files", nil); deny {
		t.Error("Bash matcher covered an unrelated tool")
	}
	alt := Hook{Matcher: "Write|Edit", Command: `echo '{"decision":"block","reason":"x"}'`}
	if deny, _, _ := run(t, alt, "edit_file", nil); !deny {
		t.Error("alternation did not cover edit_file")
	}
}

// Everything that is not an explicit denial fails open, with a notice
// for real failures: hooks only ever tighten (ADR-0044 §3).
func TestFailOpenWithNotice(t *testing.T) {
	for name, h := range map[string]Hook{
		"nonzero exit": {Matcher: "*", Command: `echo boom >&2; exit 1`},
		"missing tool": {Matcher: "*", Command: `/no/such/binary-xyz`},
	} {
		deny, _, notes := run(t, h, "shell_exec", nil)
		if deny {
			t.Errorf("%s: denied instead of failing open", name)
		}
		if len(notes) != 1 {
			t.Errorf("%s: expected one warning, got %v", name, notes)
		}
	}
	// Plain informational output is not a failure and not a verdict.
	deny, _, notes := run(t, Hook{Matcher: "*", Command: `echo checked`}, "shell_exec", nil)
	if deny || len(notes) != 0 {
		t.Errorf("informational output: deny=%v notes=%v", deny, notes)
	}
	// An explicit allow decision is pass-through, not a bypass.
	deny, _, _ = run(t, Hook{Matcher: "*", Command: `echo '{"hookSpecificOutput":{"permissionDecision":"allow"}}'`}, "shell_exec", nil)
	if deny {
		t.Error("allow denied")
	}
}

// A timeout fails open with a notice; the turn is not stalled forever.
func TestTimeoutFailsOpen(t *testing.T) {
	h := Hook{Matcher: "*", Command: `sleep 5`, Timeout: 200 * time.Millisecond}
	start := time.Now()
	deny, _, notes := run(t, h, "shell_exec", nil)
	if deny {
		t.Error("timeout denied")
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout did not bound the run")
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "timed out") {
		t.Errorf("notes = %v", notes)
	}
}

// The payload is the Claude Code shape: the installed org guard reads
// tool_input.command from it verbatim.
func TestPayloadShape(t *testing.T) {
	h := Hook{Matcher: "*", Command: `python3 -c "
import json,sys
p=json.load(sys.stdin)
assert p['hook_event_name']=='PreToolUse', p
assert p['tool_name']=='shell_exec', p
assert p['tool_input']['command']=='gofmt -w .', p
assert p['cwd'], p
print(json.dumps({'decision':'block','reason':'shape ok'}))
"`}
	deny, why, notes := run(t, h, "shell_exec", map[string]any{"command": "gofmt -w ."})
	if !deny || why != "shape ok" {
		t.Fatalf("deny=%v why=%q notes=%v", deny, why, notes)
	}
}

// First denial wins; later hooks do not run after a deny.
func TestFirstDenyWins(t *testing.T) {
	r := New([]Hook{
		{Matcher: "*", Command: `echo '{"decision":"block","reason":"first"}'`},
		{Matcher: "*", Command: `echo '{"decision":"block","reason":"second"}'`},
	}, nil)
	deny, why := r.Pre(context.Background(), "shell_exec", t.TempDir(), nil)
	if !deny || why != "first" {
		t.Fatalf("deny=%v why=%q", deny, why)
	}
}

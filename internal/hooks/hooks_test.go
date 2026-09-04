package hooks

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func run(t *testing.T, h Hook, name string, args map[string]any) (bool, string, []string) {
	t.Helper()
	var notes []string
	r := New(Hooks{PreToolUse: []Hook{h}}, func(s string) { notes = append(notes, s) })
	deny, why := r.Pre(context.Background(), Session{ID: "sess-1", TranscriptPath: "/tmp/sess-1.jsonl", CWD: t.TempDir()}, name, args)
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
// tool_input.command from it verbatim, and a per-session hook (agent-
// board's claims) reads session_id (ADR-0069 addendum).
func TestPayloadShape(t *testing.T) {
	h := Hook{Matcher: "*", Command: `python3 -c "
import json,sys
p=json.load(sys.stdin)
assert p['hook_event_name']=='PreToolUse', p
assert p['session_id']=='sess-1', p
assert p['transcript_path']=='/tmp/sess-1.jsonl', p
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
	r := New(Hooks{PreToolUse: []Hook{
		{Matcher: "*", Command: `echo '{"decision":"block","reason":"first"}'`},
		{Matcher: "*", Command: `echo '{"decision":"block","reason":"second"}'`},
	}}, nil)
	deny, why := r.Pre(context.Background(), Session{CWD: t.TempDir()}, "shell_exec", nil)
	if !deny || why != "first" {
		t.Fatalf("deny=%v why=%q", deny, why)
	}
}

// --- context hooks (ADR-0069) ---------------------------------------

func contextRunner(t *testing.T, hs Hooks) (*Runner, *[]string, Session) {
	t.Helper()
	notes := &[]string{}
	r := New(hs, func(s string) { *notes = append(*notes, s) })
	s := Session{ID: "sess-1", TranscriptPath: "/tmp/sess-1.jsonl", CWD: t.TempDir()}
	return r, notes, s
}

// The SessionStart payload is Claude Code's (measured 2026-09-04):
// session_id, transcript_path, cwd, hook_event_name, source — and plain
// stdout on exit 0 is the injected context (ADR-0069 §2–3).
func TestSessionStartPayloadAndPlainContext(t *testing.T) {
	r, notes, s := contextRunner(t, Hooks{SessionStart: []Hook{{Command: `python3 -c "
import json,sys
p=json.load(sys.stdin)
assert p['hook_event_name']=='SessionStart', p
assert p['session_id']=='sess-1', p
assert p['transcript_path']=='/tmp/sess-1.jsonl', p
assert p['source']=='resume', p
assert p['cwd'], p
print('recent notes: build needs make warm')
"`}}})
	got := r.SessionStart(context.Background(), s, "resume")
	if got != "recent notes: build needs make warm" {
		t.Fatalf("context = %q notes=%v", got, *notes)
	}
	if len(*notes) != 0 {
		t.Errorf("unexpected notes: %v", *notes)
	}
}

// The UserPromptSubmit payload carries the typed text under "prompt"
// (measured — the docs said user_input), and the JSON
// hookSpecificOutput.additionalContext form is context too.
func TestPromptSubmitPayloadAndJSONContext(t *testing.T) {
	r, notes, s := contextRunner(t, Hooks{UserPromptSubmit: []Hook{{Command: `python3 -c "
import json,sys
p=json.load(sys.stdin)
assert p['hook_event_name']=='UserPromptSubmit', p
assert p['prompt']=='deploy it', p
assert p['session_id']=='sess-1', p
print(json.dumps({'hookSpecificOutput':{'hookEventName':'UserPromptSubmit','additionalContext':'two other sessions are editing release.md'}}))
"`}}})
	extra, block, why := r.PromptSubmit(context.Background(), s, "deploy it")
	if block || why != "" {
		t.Fatalf("blocked: %q", why)
	}
	if extra != "two other sessions are editing release.md" {
		t.Fatalf("context = %q notes=%v", extra, *notes)
	}
}

// A prompt hook blocks by exit 2 (stderr is the reason) or by either
// JSON block form; the first block wins and no context survives it.
func TestPromptSubmitBlocks(t *testing.T) {
	for name, h := range map[string]Hook{
		"exit 2":         {Command: `echo "not now" >&2; exit 2`},
		"decision block": {Command: `echo '{"decision":"block","reason":"not now"}'`},
		"deny":           {Command: `echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"not now"}}'`},
	} {
		r, _, s := contextRunner(t, Hooks{UserPromptSubmit: []Hook{
			{Command: `echo context-before`}, h, {Command: `echo context-after`},
		}})
		extra, block, why := r.PromptSubmit(context.Background(), s, "x")
		if !block || why != "not now" || extra != "" {
			t.Errorf("%s: extra=%q block=%v why=%q", name, extra, block, why)
		}
	}
	// A block without a reason still names its origin.
	r, _, s := contextRunner(t, Hooks{UserPromptSubmit: []Hook{{Command: `exit 2`}}})
	if _, block, why := r.PromptSubmit(context.Background(), s, "x"); !block || why == "" {
		t.Errorf("reasonless block: block=%v why=%q", block, why)
	}
}

// A session start cannot be blocked (Claude Code's contract): a block
// verdict or exit 2 is a reported failure that injects nothing, and
// the other hooks still run.
func TestSessionStartCannotBlock(t *testing.T) {
	for name, h := range map[string]Hook{
		"exit 2":         {Command: `echo "no" >&2; exit 2`},
		"decision block": {Command: `echo '{"decision":"block","reason":"no"}'`},
	} {
		r, notes, s := contextRunner(t, Hooks{SessionStart: []Hook{h, {Command: `echo still-here`}}})
		got := r.SessionStart(context.Background(), s, "startup")
		if got != "still-here" {
			t.Errorf("%s: context = %q", name, got)
		}
		if len(*notes) != 1 || !strings.Contains((*notes)[0], "cannot be blocked") {
			t.Errorf("%s: notes = %v", name, *notes)
		}
	}
}

// JSON on stdout is a verdict, not context: a verdict object without
// additionalContext injects nothing (Claude Code treats stdout JSON as
// structured output, never as text for the model).
func TestVerdictJSONWithoutContextInjectsNothing(t *testing.T) {
	r, notes, s := contextRunner(t, Hooks{
		SessionStart:     []Hook{{Command: `echo '{"hookSpecificOutput":{"hookEventName":"SessionStart"}}'`}},
		UserPromptSubmit: []Hook{{Command: `echo '{"decision":"approve"}'`}},
	})
	if got := r.SessionStart(context.Background(), s, "startup"); got != "" {
		t.Errorf("session start injected %q", got)
	}
	if extra, block, _ := r.PromptSubmit(context.Background(), s, "x"); extra != "" || block {
		t.Errorf("prompt submit: extra=%q block=%v", extra, block)
	}
	if len(*notes) != 0 {
		t.Errorf("notes = %v", *notes)
	}
}

// Injected context is capped at ContextCap runes with a visible cut
// (ADR-0069 §4) — measured in runes, so multi-byte text is not cut
// mid-character.
func TestContextCap(t *testing.T) {
	r, _, s := contextRunner(t, Hooks{SessionStart: []Hook{{Command: `python3 -c "print('あ'*9000)"`}}})
	got := r.SessionStart(context.Background(), s, "startup")
	if !strings.HasSuffix(got, "[hook output truncated at 8000 runes]") {
		t.Fatalf("no truncation marker: …%q", got[len(got)-60:])
	}
	body := got[:strings.LastIndex(got, "\n")]
	if n := utf8.RuneCountInString(body); n != ContextCap {
		t.Errorf("kept %d runes, want %d", n, ContextCap)
	}
	if !utf8.ValidString(got) {
		t.Error("cut landed inside a rune")
	}
}

// A session-start matcher selects the source (startup / resume /
// clear); an empty matcher covers every source.
func TestSessionStartMatcherSelectsSource(t *testing.T) {
	r, _, s := contextRunner(t, Hooks{SessionStart: []Hook{
		{Matcher: "resume", Command: `echo only-on-resume`},
		{Matcher: "startup|clear", Command: `echo fresh`},
		{Command: `echo always`},
	}})
	if got := r.SessionStart(context.Background(), s, "resume"); got != "only-on-resume\n\nalways" {
		t.Errorf("resume: %q", got)
	}
	if got := r.SessionStart(context.Background(), s, "clear"); got != "fresh\n\nalways" {
		t.Errorf("clear: %q", got)
	}
}

// Context hooks fail open like pre-tool hooks: a crash or timeout
// injects nothing and leaves one notice; the prompt is never blocked
// by a broken hook.
func TestContextHooksFailOpen(t *testing.T) {
	for name, h := range map[string]Hook{
		"nonzero exit": {Command: `echo boom >&2; exit 1`},
		"missing tool": {Command: `/no/such/binary-xyz`},
		"timeout":      {Command: `sleep 5`, Timeout: 200 * time.Millisecond},
	} {
		r, notes, s := contextRunner(t, Hooks{SessionStart: []Hook{h}, UserPromptSubmit: []Hook{h}})
		if got := r.SessionStart(context.Background(), s, "startup"); got != "" {
			t.Errorf("%s: session start injected %q", name, got)
		}
		extra, block, _ := r.PromptSubmit(context.Background(), s, "x")
		if extra != "" || block {
			t.Errorf("%s: prompt submit extra=%q block=%v", name, extra, block)
		}
		if len(*notes) != 2 {
			t.Errorf("%s: notes = %v", name, *notes)
		}
	}
}

// Several context hooks concatenate in order; empty output contributes
// nothing and no separator.
func TestContextHooksConcatenate(t *testing.T) {
	r, _, s := contextRunner(t, Hooks{UserPromptSubmit: []Hook{
		{Command: `echo one`}, {Command: `true`}, {Command: `echo two`},
	}})
	extra, _, _ := r.PromptSubmit(context.Background(), s, "x")
	if extra != "one\n\ntwo" {
		t.Errorf("extra = %q", extra)
	}
}

// The runner reports which context events are configured, so the
// caller can skip the work entirely in the common case.
func TestHasContextHooks(t *testing.T) {
	if r := New(Hooks{}, nil); r.HasSessionStart() || r.HasPromptSubmit() {
		t.Error("empty runner reports hooks")
	}
	r := New(Hooks{SessionStart: []Hook{{Command: "true"}}}, nil)
	if !r.HasSessionStart() || r.HasPromptSubmit() {
		t.Error("session start not reported")
	}
}

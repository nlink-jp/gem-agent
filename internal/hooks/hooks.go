// Package hooks runs operator-configured commands at fixed points of a
// session: before a model tool call (ADR-0044), when a session starts,
// and when the operator submits a prompt (ADR-0069). The stdin payloads
// and the verdict contracts are Claude Code's, measured against the
// installed guard and against Claude Code itself rather than taken from
// documentation, so a hooks block written for Claude Code registers
// here unchanged.
//
// Pre-tool hooks are a deterministic floor: a deny stands before the
// approval ladder ever runs. Context hooks (session start, prompt
// submit) inject text the model sees as data — never as instructions
// and never inside the operator's typed input — and a prompt hook can
// refuse a prompt outright. Every other outcome fails open with a
// notice: hooks only ever tighten.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"
)

// DefaultTimeout bounds one hook run. Short on purpose: a hook is a
// deterministic check, and the model's turn is stalled while it runs.
const DefaultTimeout = 10 * time.Second

// ContextCap bounds the context one context hook may inject, in runes
// (ADR-0069 §4). A hook that prints without limit would crowd the turn
// it was meant to inform; the cut is marked so the model knows.
const ContextCap = 8000

// Hook is one configured command.
type Hook struct {
	// Matcher selects what the hook covers: for pre-tool hooks the tool
	// name (exact, "a|b" alternation, or "*"); for session-start hooks
	// the source (startup, resume, clear), empty meaning every source.
	// Prompt hooks take no matcher.
	Matcher string
	Command string // run via sh -c, payload on stdin
	Timeout time.Duration
}

// Hooks groups the operator's hooks by event.
type Hooks struct {
	PreToolUse       []Hook
	SessionStart     []Hook
	UserPromptSubmit []Hook
}

// Session identifies the running session to context hooks (ADR-0069
// §2) — the fields of Claude Code's SessionStart / UserPromptSubmit
// payloads that gem-agent can supply. An empty ID and TranscriptPath
// mean the session log is disabled; the fields are still sent, empty,
// so a script sees the same shape every time.
type Session struct {
	ID             string
	TranscriptPath string
	CWD            string
}

// aliases maps gem-agent tool names to their Claude Code names, so a
// hooks block copied from Claude Code settings works without renaming
// (ADR-0044 §4). The payload still carries gem-agent's real name — the
// org's guard measurably ignores it, and lying to scripts that do look
// would be worse.
var aliases = map[string]string{
	"shell_exec": "Bash",
	"write_file": "Write",
	"edit_file":  "Edit",
	"read_file":  "Read",
}

// Runner evaluates the configured hooks.
type Runner struct {
	hooks  Hooks
	notify func(string) // non-blocking failures surface here; never nil
}

// New builds a Runner. notify receives fail-open warnings (a broken or
// timed-out hook); pass nil to drop them.
func New(hs Hooks, notify func(string)) *Runner {
	if notify == nil {
		notify = func(string) {}
	}
	return &Runner{hooks: hs, notify: notify}
}

// HasSessionStart reports whether any session-start hook is configured.
func (r *Runner) HasSessionStart() bool { return len(r.hooks.SessionStart) > 0 }

// HasPromptSubmit reports whether any prompt-submit hook is configured.
func (r *Runner) HasPromptSubmit() bool { return len(r.hooks.UserPromptSubmit) > 0 }

// matches reports whether one matcher covers the name, in either
// vocabulary.
func matches(matcher, name string) bool {
	for _, m := range strings.Split(matcher, "|") {
		m = strings.TrimSpace(m)
		if m == "*" || m == name || m == aliases[name] {
			return true
		}
	}
	return false
}

// preToolPayload is the Claude Code PreToolUse stdin shape (measured
// contract, ADR-0044).
type preToolPayload struct {
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	CWD           string         `json:"cwd"`
}

// sessionStartPayload is the Claude Code SessionStart stdin shape
// (measured 2026-09-04 against Claude Code 2.1.226, ADR-0069 §2).
type sessionStartPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Source         string `json:"source"`
}

// promptPayload is the Claude Code UserPromptSubmit stdin shape
// (measured: the typed text arrives under "prompt", ADR-0069 §2).
type promptPayload struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	Prompt         string `json:"prompt"`
}

// verdict covers the stdout JSON forms Claude Code scripts use: the
// hookSpecificOutput form (the org's guard, measured), the older
// top-level decision form, and — for context hooks — the
// additionalContext field (measured, ADR-0069 §3).
type verdict struct {
	Decision           string `json:"decision"` // "block" denies
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"` // "deny" denies
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// outcome is one finished hook process.
type outcome struct {
	stdout, stderr string
	timedOut       bool
	err            error // spawn failure or non-zero exit
}

// exec runs one hook with the payload on stdin. ok is false when the
// payload could not even be encoded (notified; the hook is skipped).
func (r *Runner) exec(ctx context.Context, h Hook, cwd string, payload any) (outcome, bool) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	in, err := json.Marshal(payload)
	if err != nil {
		r.notify(fmt.Sprintf("hook %q skipped: cannot encode the payload: %v", h.Command, err))
		return outcome{}, false
	}
	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", h.Command)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	out := outcome{stdout: stdout.String(), stderr: stderr.String(), err: runErr}
	if cctx.Err() == context.DeadlineExceeded {
		out.timedOut = true
		r.notify(fmt.Sprintf("hook %q timed out after %s", h.Command, timeout))
	}
	return out, true
}

// exitCode2 reports whether the process ended with the deny/block exit
// code of the simple contract.
func exitCode2(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee) && ee.ExitCode() == 2
}

// Pre runs every matching pre-tool hook in order and reports the first
// denial. Anything that is not an explicit denial — pass, non-zero
// exit, unparseable output, timeout — lets the call proceed to the
// normal approval ladder: hooks only ever tighten (ADR-0044 §3).
func (r *Runner) Pre(ctx context.Context, name, cwd string, args map[string]any) (deny bool, reason string) {
	for _, h := range r.hooks.PreToolUse {
		if !matches(h.Matcher, name) {
			continue
		}
		out, ok := r.exec(ctx, h, cwd, preToolPayload{
			HookEventName: "PreToolUse", ToolName: name, ToolInput: args, CWD: cwd,
		})
		if !ok || out.timedOut {
			continue // the call proceeds
		}
		if out.err != nil {
			if exitCode2(out.err) {
				return true, orDefault(out.stderr, "blocked by a pre-tool hook")
			}
			r.notify(fmt.Sprintf("hook %q failed (%v) — the call proceeds", h.Command, out.err))
			continue
		}
		if v, isJSON := parseVerdict(out.stdout); isJSON {
			if blocked, why := v.denies(); blocked {
				return true, orDefault(why, "blocked by a pre-tool hook")
			}
		}
	}
	return false, ""
}

// SessionStart runs the session-start hooks whose matcher covers source
// and returns the context they injected, joined. A session start cannot
// be blocked (Claude Code's contract, measured): a block verdict or exit
// 2 is reported as a failure and contributes nothing.
func (r *Runner) SessionStart(ctx context.Context, s Session, source string) string {
	var parts []string
	for _, h := range r.hooks.SessionStart {
		if strings.TrimSpace(h.Matcher) != "" && !matches(h.Matcher, source) {
			continue
		}
		out, ok := r.exec(ctx, h, s.CWD, sessionStartPayload{
			HookEventName: "SessionStart", SessionID: s.ID, TranscriptPath: s.TranscriptPath,
			CWD: s.CWD, Source: source,
		})
		if !ok {
			continue
		}
		text, block, why := r.interpret(h, out, "session_start")
		if block {
			r.notify(fmt.Sprintf("session_start hook %q asked to block (%s) — a session start cannot be blocked; ignored", h.Command, why))
			continue
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// PromptSubmit runs the prompt-submit hooks in order with the typed
// prompt. The first block wins and erases the prompt (Claude Code's
// contract, measured: the turn never starts). Otherwise the context
// the hooks injected is returned, joined.
func (r *Runner) PromptSubmit(ctx context.Context, s Session, prompt string) (extra string, block bool, reason string) {
	var parts []string
	for _, h := range r.hooks.UserPromptSubmit {
		out, ok := r.exec(ctx, h, s.CWD, promptPayload{
			HookEventName: "UserPromptSubmit", SessionID: s.ID, TranscriptPath: s.TranscriptPath,
			CWD: s.CWD, Prompt: prompt,
		})
		if !ok {
			continue
		}
		text, blocked, why := r.interpret(h, out, "user_prompt_submit")
		if blocked {
			return "", true, orDefault(why, "blocked by a user_prompt_submit hook")
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n"), false, ""
}

// interpret turns one context-hook outcome into injected text and/or a
// block. Measured contract (ADR-0069 §3): exit 0 with plain stdout is
// context; exit 0 with JSON is a verdict whose additionalContext (if
// any) is context; exit 2 blocks with stderr as the reason. Anything
// else fails open with a notice and no context.
func (r *Runner) interpret(h Hook, out outcome, event string) (text string, block bool, reason string) {
	if out.timedOut {
		return "", false, ""
	}
	if out.err != nil {
		if exitCode2(out.err) {
			return "", true, strings.TrimSpace(out.stderr)
		}
		r.notify(fmt.Sprintf("%s hook %q failed (%v) — no context injected", event, h.Command, out.err))
		return "", false, ""
	}
	raw := strings.TrimSpace(out.stdout)
	if raw == "" {
		return "", false, ""
	}
	v, isJSON := parseVerdict(raw)
	if !isJSON {
		return clip(raw), false, ""
	}
	if blocked, why := v.denies(); blocked {
		return "", true, why
	}
	return clip(strings.TrimSpace(v.HookSpecificOutput.AdditionalContext)), false, ""
}

// parseVerdict reads stdout as a JSON verdict object. Plain text — the
// context form, or informational output — is not JSON.
func parseVerdict(out string) (verdict, bool) {
	out = strings.TrimSpace(out)
	if !strings.HasPrefix(out, "{") {
		return verdict{}, false
	}
	var v verdict
	if json.Unmarshal([]byte(out), &v) != nil {
		return verdict{}, false
	}
	return v, true
}

// denies reports a block in either JSON contract, with its reason.
func (v verdict) denies() (bool, string) {
	if v.HookSpecificOutput.PermissionDecision == "deny" {
		return true, strings.TrimSpace(v.HookSpecificOutput.PermissionDecisionReason)
	}
	if v.Decision == "block" {
		return true, strings.TrimSpace(v.Reason)
	}
	return false, ""
}

// clip bounds injected context at ContextCap runes, marking the cut.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= ContextCap {
		return s
	}
	rs := []rune(s)
	return string(rs[:ContextCap]) + fmt.Sprintf("\n… [hook output truncated at %d runes]", ContextCap)
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}

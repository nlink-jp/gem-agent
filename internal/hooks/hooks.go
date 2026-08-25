// Package hooks runs operator-configured pre-tool commands (ADR-0044).
// A hook sees every model tool call whose name its matcher covers, and
// can refuse the call before the approval ladder ever runs — the same
// standing as the risk tier's Block floor. The org's Claude Code
// guards run unchanged: the stdin payload and both verdict contracts
// are Claude Code's, measured against the installed guard rather than
// taken from documentation.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DefaultTimeout bounds one hook run. Short on purpose: a hook is a
// deterministic check, and the model's turn is stalled while it runs.
const DefaultTimeout = 10 * time.Second

// Hook is one configured pre-tool command.
type Hook struct {
	Matcher string // exact tool name, "a|b" alternation, or "*"
	Command string // run via sh -c, payload on stdin
	Timeout time.Duration
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

// Runner evaluates the configured hooks for one tool call.
type Runner struct {
	hooks  []Hook
	notify func(string) // non-blocking failures surface here; never nil
}

// New builds a Runner. notify receives fail-open warnings (a broken or
// timed-out hook); pass nil to drop them.
func New(hs []Hook, notify func(string)) *Runner {
	if notify == nil {
		notify = func(string) {}
	}
	return &Runner{hooks: hs, notify: notify}
}

// matches reports whether one matcher covers the tool name, in either
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

// payload is the Claude Code PreToolUse stdin shape (measured contract).
type payload struct {
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name"`
	ToolInput     map[string]any `json:"tool_input"`
	CWD           string         `json:"cwd"`
}

// verdict covers both deny contracts Claude Code scripts use: the
// hookSpecificOutput form (the org's guard, measured) and the older
// top-level decision form.
type verdict struct {
	Decision           string `json:"decision"` // "block" denies
	Reason             string `json:"reason"`
	HookSpecificOutput struct {
		PermissionDecision       string `json:"permissionDecision"` // "deny" denies
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	} `json:"hookSpecificOutput"`
}

// Pre runs every matching hook in order and reports the first denial.
// Anything that is not an explicit denial — pass, non-zero exit,
// unparseable output, timeout — lets the call proceed to the normal
// approval ladder: hooks only ever tighten (ADR-0044 §3).
func (r *Runner) Pre(ctx context.Context, name, cwd string, args map[string]any) (deny bool, reason string) {
	for _, h := range r.hooks {
		if !matches(h.Matcher, name) {
			continue
		}
		if d, why := r.runOne(ctx, h, name, cwd, args); d {
			return true, why
		}
	}
	return false, ""
}

func (r *Runner) runOne(ctx context.Context, h Hook, name, cwd string, args map[string]any) (bool, string) {
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	in, err := json.Marshal(payload{
		HookEventName: "PreToolUse", ToolName: name, ToolInput: args, CWD: cwd,
	})
	if err != nil {
		r.notify(fmt.Sprintf("hook %q skipped: cannot encode the call: %v", h.Command, err))
		return false, ""
	}
	cmd := exec.CommandContext(cctx, "/bin/sh", "-c", h.Command)
	cmd.Dir = cwd
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	runErr := cmd.Run()
	if cctx.Err() == context.DeadlineExceeded {
		r.notify(fmt.Sprintf("hook %q timed out after %s — the call proceeds", h.Command, timeout))
		return false, ""
	}
	if runErr != nil {
		// Exit 2 is the simple deny contract: stderr carries the reason.
		if ee, ok := runErr.(*exec.ExitError); ok && ee.ExitCode() == 2 {
			why := strings.TrimSpace(stderr.String())
			if why == "" {
				why = "blocked by a pre-tool hook"
			}
			return true, why
		}
		r.notify(fmt.Sprintf("hook %q failed (%v) — the call proceeds", h.Command, runErr))
		return false, ""
	}
	// Exit 0: the verdict, if any, is JSON on stdout.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		return false, ""
	}
	var v verdict
	if json.Unmarshal([]byte(out), &v) != nil {
		// Not JSON — informational output, not a verdict.
		return false, ""
	}
	if v.HookSpecificOutput.PermissionDecision == "deny" {
		why := strings.TrimSpace(v.HookSpecificOutput.PermissionDecisionReason)
		if why == "" {
			why = "blocked by a pre-tool hook"
		}
		return true, why
	}
	if v.Decision == "block" {
		why := strings.TrimSpace(v.Reason)
		if why == "" {
			why = "blocked by a pre-tool hook"
		}
		return true, why
	}
	return false, ""
}

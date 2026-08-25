# ADR-0044: operator pre-tool hooks — the org's guards survive the fallback

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-25 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: can gem-agent carry an equivalent of Claude Code's hooks? The org's PreToolUse guard exists because "procedure fails exactly when attention lapses, so the control has to live outside the agent" — and it vanishes at the moment of fallback |

## Context

The organization runs a Claude Code `PreToolUse` hook
(`guard-recursive-write.py`) that refuses recursive in-place rewrites
aimed at relative paths — a failure that has reformatted the whole
workspace twice, and that the guard demonstrably still catches (it
fired twice on the primary tool the day this ADR was written). The
guard's own rationale is that a written rule cannot prevent a lapse of
attention; the control must sit outside the agent.

gem-agent is the fallback for exactly the moments when tooling
misbehaves — and it is the one place where that outside control does
not exist. Falling back removes the guard precisely when an unfamiliar
tool raises the odds of the mistake it guards against.

Measured before designing (the guard's real contract, not the
documented one):

- stdin: `{"tool_name": …, "tool_input": {…}}`
- verdict: **stdout JSON** — `hookSpecificOutput.permissionDecision:
  "deny"` with a `permissionDecisionReason`; the process exits 0 either
  way. (Claude Code also supports an exit-code-2-with-stderr form.)
- the guard never reads `tool_name`, only `tool_input.command` — and
  gem-agent's `shell_exec` argument is also named `command`, so **the
  org's guard runs unchanged on a gem-agent payload**. This was
  verified by piping a gem-agent-shaped call into the installed guard.

## Decision

### 1. `PreToolUse` only, from gem-agent's own config

`[[hooks.pre_tool_use]]` entries in `config.toml` name a `matcher`, a
`command`, and an optional `timeout_sec`. Claude Code's other events
(PostToolUse, Stop, SessionStart, …) are not implemented: the one event
with demonstrated demand is PreToolUse, and a capability without a
demonstrated trigger is dead weight (the ADR-0020 §5 lesson). The same
mechanism accepts more events later if demand appears.

`~/.claude/settings.json` is **not** read — the ADR-0011 principle
stands. The org installer is likewise untouched (operator direction):
registration is one block in gem-agent's own config pointing at the
same script file, so there is no second copy to drift.

### 2. The hook is a deterministic floor, and the model is told

The hook runs at the top of tool dispatch, before the risk ladder: a
deny cannot be overridden by the model tier, auto-approve, or the
session allowlist — the same standing as the Block floor. The refusal
reason is returned to the model as the tool result (the ADR-0043
principle: rejecting without telling the author is not honest), which
is also how the guard behaves under Claude Code, where the reason
demonstrably steers the model to a compliant retry.

Hooks guard **the model's calls**. The operator's own `!command`
escape does not pass through them, by design: the control exists to
catch the agent's lapses, not to gate its operator.

### 3. Both verdict forms; anything else fails open with a notice

A hook denies by either contract: stdout JSON
(`hookSpecificOutput.permissionDecision: "deny"`, or the older
`decision: "block"`), or exit code 2 with stderr as the reason. Any
other outcome — non-zero exit, unparseable output, timeout — is a
**non-blocking failure**: the call proceeds and the operator sees a
warning. This matches Claude Code's semantics (drop-in compatibility
covers failure behaviour too), and a fallback tool must not brick
itself on a broken guard script. Hooks run unsandboxed (operator
configuration is trusted; a guard may need to read anywhere) with a
short default timeout.

### 4. Matchers speak both vocabularies

A matcher is an exact tool name, a `|`-alternation, or `*`. It matches
against gem-agent's name **and** its Claude Code alias (`shell_exec` ↔
`Bash`, `write_file` ↔ `Write`, `edit_file` ↔ `Edit`, `read_file` ↔
`Read`), so a hooks block copied from Claude Code settings works
without renaming. The payload's `tool_name` is gem-agent's real name —
measured as irrelevant to the org's guard, and honest to scripts that
do look.

### 5. Global config only

A project-level hook would mean a cloned repository executes an
arbitrary command on every tool call. No project surface exists in v1;
if one is ever added it sits behind ADR-0023 project trust like
`.mcp.json`.

## Consequences

- The org's delete rules and rewrite guards hold across the fallback,
  enforced by the same script files Claude Code runs.
- Every model tool call pays one `matcher` comparison; matched calls
  pay one process spawn. Unmatched calls cost nothing measurable.
- A malicious or buggy hook can deny everything (denial of service,
  visible) but cannot approve anything: pass-through continues to the
  normal ladder, so hooks only ever tighten.
- One more `[hooks]` section in the config reference; the settings
  panel shows nothing new (hooks have no runtime toggle — they are a
  security control, and a toggle would be a bypass).

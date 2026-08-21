# ADR-0034: Cancellation must end the call — process groups, WaitDelay, and a last-resort exit

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a skill's python tool call never returned; Ctrl+C showed "interrupting…" and deadlocked |

## Context

`shell_exec` ran commands with `exec.CommandContext` +
`CombinedOutput()`. On timeout or Ctrl+C, Go kills the DIRECT child
only — `sandbox-exec` (or bash). A grandchild the command spawned —
the skill's python — survives, still holding the inherited
stdout/stderr pipe, and `CombinedOutput`'s internal Wait blocks until
that pipe reaches EOF. Both cancellation paths fall into the same
hole: the 120s timeout fired and then hung in Wait; the operator's
Ctrl+C cancelled the context and then hung in the same Wait. The TUI
honestly reported "interrupting…" forever — the turn goroutine was
never coming back, so TurnDone never arrived, and no key could help.

## Decision

### 1. The whole process GROUP dies, not the direct child

Every shell command starts as a process-group leader (`Setpgid`), and
the context's `Cancel` sends SIGKILL to the group (`kill -pid`):
sandbox-exec, the shell, python, and anything else the command spawned
die together, closing the pipes immediately.

### 2. `WaitDelay` as the backstop

A process that calls `setsid`, or double-forks into a new group,
escapes the group kill. `WaitDelay` (2s) bounds the damage: after the
context is cancelled and the direct child is dead, Wait stops waiting
for inherited pipes and returns. The escapee may briefly live on as an
orphan — the operator's session does not hang for it. Output produced
before the cut is kept and truncated as usual.

### 3. A last-resort exit in the TUI

Defense in depth for whatever future tool wedges in a way not yet
imagined: while "interrupting…" is showing, a second Ctrl+C warns that
the tool is not responding to cancellation and that one more Ctrl+C
quits; a third quits gem-agent. The transcript is append-per-event, so
everything up to the wedged call is already on disk — the message says
so. Three presses, not two: a panic double-tap must not kill a session
that was one second from finishing cleanly.

## Consequences

- Timeout and interrupt now end shell calls that spawn children —
  measured: a command whose background child holds the pipe returns in
  milliseconds after cancel where it previously hung for the child's
  full lifetime.
- Group SIGKILL is abrupt by design: a cancelled command gets no
  cleanup window. Cancellation here has always meant "stop it now";
  graceful-then-forceful is complexity this backup tool does not need.
- The rare setsid escapee outlives the call as an orphan process. The
  alternative — hanging the session — is strictly worse; the kill is
  best-effort, the return is guaranteed.

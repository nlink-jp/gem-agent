# ADR-0065: Cancellation must end the call, part 2 — cooperative walks, a return-guaranteed floor, and the escape ladder everywhere

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-02 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: Ctrl+C during a file search stayed on "interrupting…" and the search ran on; the third Ctrl+C took a while too |
| Extends | ADR-0034 |

## Context

ADR-0034 made cancellation end a *shell* call: process-group kill,
`WaitDelay`, and a three-press exit in the TUI. The in-process tools
were never given the same contract. `search_files` and `list_tree`
receive the turn's context and never consult it; their walks run
`ReadDir`/`ReadFile` to the end of the project. The agent calls
`Tool.Run` synchronously with no floor under it, so the only thing
Ctrl+C does is cancel a context nobody reads. The TUI shows
"interrupting…" and waits for TurnDone, which arrives only after the
walk finishes on its own and the next model call fails on the dead
context. The delegated `agentic_file_search` child passes the same
context to the same tools, so it wedges the same way.

Measured with a faithful replica of the walk over 30,000 files on a
local SSD: a full walk takes 1.6 s; cancelled at 20 ms, the shipped
shape returns after 1.6 s (the whole remaining walk), a walk that
checks the context per directory and per file returns 2 ms after the
cancel, and a goroutine-plus-select wrapper returns in 1 ms. On a
slow filesystem — a network mount, a cold cache, a huge tree — the
shipped lag is the remaining walk time, which is what the operator
saw.

Two more holes surfaced in the same investigation:

- The plain REPL and `-p` use `signal.NotifyContext`, which swallows
  every SIGINT after the first while it is registered. Outside the
  TUI there is no escape ladder at all: a wedged walk can only be
  killed from another terminal.
- The wait after the third Ctrl+C is the exit path, not the walk: the
  deferred audit-log flush is bounded at 3 s and says nothing while
  it runs. A bounded silence reads as a hang (the ADR-0033 lesson).

A cooperative check cannot interrupt a blocking syscall. `ReadDir` on
a hung NFS or SMB mount returns when the kernel says so, and Go has no
way to cancel it. Any fix that only adds checks leaves that case
exactly where ADR-0034 found `Wait`.

## Decision

### 1. The walks consult the context

`search_files` and `list_tree` check `ctx.Err()` before every
directory read and before every file read. On cancellation they stop
where they are and return what they have, labelled: `search_files`
appends `[interrupted after N files scanned — results above are
partial]`, `list_tree` appends `[interrupted — the tree above is
partial]`. The partial result is a result, not an error: it goes into
the transcript, so a resumed session sees what was found before the
interrupt (the "progress is saved" stance of ADR-0040) and no
truncation is ever silent (ADR-0052).

### 2. A return-guaranteed floor under every tool call

`execCall` runs `Tool.Run` in a goroutine and selects on the result
and the context. When the context is cancelled it waits a short grace
(500 ms) for the cooperative return — on a healthy filesystem the
walk of §1 comes back within one syscall, and the partial result must
win that race — then abandons the call: the model receives
`error: interrupted — the call was abandoned; it may still complete
in the background and its result is discarded`, and the audit event
records `outcome = abandoned`. If the abandoned goroutine returns
later, a `tool_late_return` session record (tool name, outcome,
duration) closes the audit gap; nothing else consumes the late
result. A cooperative tool that returns after the cancel, inside the
grace, is recorded as `outcome = interrupted`.

This is ADR-0034's rule extended to the process: the stop is
best-effort, the return is guaranteed. The floor also covers whatever
future tool ignores the context, and it does nothing for MCP calls,
which already select on the context (kill-and-respawn).

### 3. The escape ladder exists outside the TUI too

The plain REPL and `-p` get the same three-press ladder as ADR-0034
§3: the first Ctrl+C cancels the turn, the second warns that the
turn is not responding and that one more quits, the third exits the
process with status 130. The deferred flushes are skipped on that
exit — the transcript is written per event, so everything up to the
wedged call is on disk, and the message says so.

### 4. The exit says when it is flushing

When audit logging is enabled, the exit prints `sending audit
events… (up to 3s)` before the bounded flush. The bound is unchanged;
the silence is gone.

## Consequences

- Ctrl+C returns within the grace on any filesystem that answers.
  On a hung mount the floor returns and one goroutine plus one open
  file descriptor stay behind until the syscall returns or the
  process exits — the same shape as ADR-0034's orphan, and the same
  trade: a leaked goroutine is far cheaper than a wedged session.
- A mutating tool abandoned mid-call may still complete: the model
  sees "interrupted", the file may still be written. The late-return
  record is the mitigation; restricting the floor to read-only tools
  was rejected (below).
- The contract for tool authors is now explicit (AGENTS.md): every
  `Tool.Run` consults its context; the floor guarantees the return,
  not the stop.
- The `tool.call` audit vocabulary gains two outcomes: `interrupted`
  (the tool returned after the cancel, within the grace) and
  `abandoned` (the floor returned without it).

## Alternatives considered

- **Floor without a grace.** Returns at the instant of cancellation
  and loses the cooperative partial result to a race on every healthy
  filesystem. The grace costs at most 500 ms of "interrupting…" and
  keeps the found matches.
- **Cooperative checks only.** Cannot end a blocking syscall; the
  hung-mount case would stay exactly where ADR-0034 found `Wait`.
  Kept as §1 because it makes the common case cheap and leak-free,
  rejected as the whole fix.
- **Floor for read-only tools only.** Leaves `write_file` on a hung
  mount wedging the session, which is the failure ADR-0034 refused
  to accept. The audit record of a late return is the honest
  alternative to a hang.
- **A timeout on the walks.** The wrong axis: a slow, large project is
  legitimate and finite. ADR-0033 already declined automatic
  timeouts for the same reason.
- **Two presses to quit outside the TUI.** ADR-0034 §3 chose three so
  a panic double-tap cannot kill a session about to finish cleanly;
  the plain REPL gets the same ladder rather than a different one.

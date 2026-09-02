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
saw. The probe's input is local and warm; the design below does not
depend on the slow case being measured, because it bounds the wait
instead of predicting the syscall.

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
directory read and before every file read, and `search_files` checks
again every 1024 lines inside a file — a 2 MB file under a heavy
pattern can outlast the grace of §2 on its own. On cancellation they
stop where they are and return what they have, labelled:
`search_files` appends `[interrupted after N files scanned — results
above are partial]` (a file cut mid-scan is not counted),
`list_tree` appends `[interrupted — the tree above is partial]`. The
partial result is a result, not an error: it goes into the
transcript, so a resumed session sees what was found before the
interrupt (the "progress is saved" stance of ADR-0040) and no
truncation is ever silent (ADR-0052).

### 2. A return-guaranteed floor under every tool call

`execCall` runs `Tool.Run` in a goroutine and selects on the result
and the context. When the context is cancelled it waits a grace of
1 s for the cooperative return — on a filesystem that answers, the
walk of §1 is back within one syscall, and that partial result must
win the race — then abandons the call: the model receives `error:
interrupted — the call was abandoned; it may still complete in the
background and its result is discarded`, and the audit event records
`outcome = abandoned`. A cooperative tool that returns after the
cancel, inside the grace, is recorded as `outcome = interrupted`.

The grace is longer than the shell's `WaitDelay` on purpose, and the
`WaitDelay` of ADR-0034 §2 drops from 2 s to 500 ms to make that so:
a cancelled shell call whose escapee held the pipe returns the output
produced before the cut at the `WaitDelay`, and the floor must still
be waiting for it. A test pins the ordering.

Two things are outside the floor. The approval gate: a dialog waiting
on the operator is not a wedged tool. And a tool that itself waits on
the operator — `ask_user`, marked `WaitsOnOperator` — because an
abandoned stdin read would be a second reader on the plain REPL's one
stdin, eating the operator's next line (the shared-reader rule in
AGENTS.md). The operator decides when those return.

What the floor abandons is accounted for. The count of abandoned
calls still running is kept in the usage statistics and named on the
exit receipt when it is not zero — a goroutine holding a syscall may
write after the process is gone. When an abandoned call does return,
a `tool_late_return` session record and a `tool.late_return` audit
event say so (name, outcome, duration); both are best-effort once the
session has closed its log or shut down its exporter. A MUTATING call
that returns late is also announced to the model at the start of the
next turn, as a user-role note before that turn's message: its last
word on the call was "result discarded", and the write may have
landed. Nothing consumes the late result itself.

This is ADR-0034's rule extended to the process: the stop is
best-effort, the return is guaranteed. The floor also covers whatever
future tool ignores the context, and it does nothing for MCP calls,
which already select on the context (kill-and-respawn). The
delegated `agentic_file_search` child sits under the same floor as a
tool of the parent; when its own tools return partial results to a
dead context, the child's next model call fails and its findings are
not surfaced — the child keeps no transcript by design (ADR-0037).

### 3. The escape ladder exists outside the TUI too

Every turn that runs outside the TUI — the `-p` one-shot, a plain
REPL turn, the `!` shell escape, `/compact` — climbs the same
three-press ladder as ADR-0034 §3: the first Ctrl+C cancels the turn
and prints "interrupting…" so the press is seen to land (the plain
REPL used to say nothing until the turn returned), the second warns
with the TUI's own text that the turn is not responding and that one
more quits, the third exits the process with status 130. The deferred
flushes are skipped on that exit — the transcript is written per
event, so everything up to the wedged call is on disk, and the
warning said so. A first-press interrupt of `-p` still exits 1
through the ordinary error path, as before; 130 marks the forced
exit. A source-scan test pins every call site to the ladder, the way
the shared stdin reader is pinned.

### 4. The exit says when it is flushing

When audit logging is enabled, the interactive exit prints `sending
audit events… (up to 3s)` before the bounded flush; one-shot mode
stays quiet for pipelines, like the exit receipt. The string lives in
the ADR-0029 catalog with the receipt it accompanies. The bound is
unchanged; the silence is gone.

## Consequences

- Ctrl+C returns within the grace on any filesystem that answers.
  On a hung mount the floor returns and one goroutine plus one open
  file descriptor stay behind until the syscall returns or the
  process exits — the same shape as ADR-0034's orphan, and the same
  trade: a leaked goroutine is far cheaper than a wedged session. The
  exit receipt counts them.
- An abandoned goroutine may still be running while the next turn
  runs its own tools. The agent's shared state is mutex-guarded; a
  tool's own side effects (two writes to one path) are not
  serialised, which is why a late mutating return is announced to the
  model rather than hidden.
- A mutating tool abandoned mid-call may still complete: the model
  sees "interrupted", the file may still be written. The next-turn
  note and the late-return records are the mitigation; restricting
  the floor to read-only tools was rejected (below).
- The contract for tool authors is now explicit (AGENTS.md): every
  `Tool.Run` consults its context; the floor guarantees the return,
  not the stop; a tool that waits on the operator says so with
  `WaitsOnOperator`.
- The `tool.call` audit vocabulary gains two outcomes, `interrupted`
  and `abandoned`, and one event, `tool.late_return`.
- The test contract: the walks are cut deterministically with a FIFO
  stalling `ReadFile` (the hung-mount shape in miniature); the floor
  is exercised in its three shapes — a tool that never returns, a
  tool that returns inside the grace, an ordinary call — under the
  race detector, plus the operator-bound exemption and the late
  mutating announcement; the ladder is driven by real SIGINTs to the
  test process; the grace-over-WaitDelay ordering is pinned.

## Alternatives considered

- **Floor without a grace.** Returns at the instant of cancellation
  and loses the cooperative partial result to a race on every healthy
  filesystem. The grace costs at most a second of "interrupting…" and
  keeps the found matches.
- **Cooperative checks only.** Cannot end a blocking syscall; the
  hung-mount case would stay exactly where ADR-0034 found `Wait`.
  Kept as §1 because it makes the common case cheap and leak-free,
  rejected as the whole fix.
- **Floor for read-only tools only.** Leaves `write_file` on a hung
  mount wedging the session, which is the failure ADR-0034 refused
  to accept. The late-return records and the next-turn note are the
  honest alternative to a hang.
- **Floor around the approval gate too.** A gate waiting on the
  operator is not wedged, and abandoning a stdin read breaks the
  one-reader rule. The same reasoning exempts `ask_user`.
- **A configurable grace.** A knob on a wait that has already lost
  its purpose invites tuning instead of fixing; ADR-0033's stance
  against timers on legitimate work applies. The grace is a named
  constant with a test pinning its one real constraint (longer than
  the shell `WaitDelay`).
- **A timeout on the walks.** The wrong axis: a slow, large project is
  legitimate and finite. ADR-0033 already declined automatic
  timeouts for the same reason.
- **Two presses to quit outside the TUI.** ADR-0034 §3 chose three so
  a panic double-tap cannot kill a session about to finish cleanly;
  the plain REPL gets the same ladder rather than a different one.

## References

- ADR-0033 — turn observability; no automatic timeouts on legitimate work
- ADR-0034 — cancellation must end the call, part 1 (shell): the rule this ADR extends
- ADR-0037 — the delegated file-search child keeps no transcript
- ADR-0040 — "progress is saved" on a stopped turn
- ADR-0052 — every skip and cut is reported, never silent
- nlink-jp/knowledge, config-and-io: exec cancellation kills only the direct child

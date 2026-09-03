# ADR-0067: A one-shot run waiting on piped stdin says so

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A scripted `gem-agent -p …` launched from a tool harness hung for ten minutes with nothing on either stream; the harness had handed it an open, idle pipe as stdin |
| Amends | ADR-0055 §2 (the "never hangs" clause covered only a terminal stdin) |

## Context

ADR-0055 made `data | gem-agent -p "…"` read a non-terminal stdin to
EOF and attach it as data. It closed the interactive hole — a
terminal stdin is never read — and stopped there. The other shape of
idle stdin was left uncovered: a pipe that is open but that nobody
will ever write to or close. Task schedulers, launchd jobs, CI steps,
and agent tool harnesses routinely spawn a child with such a pipe
inherited. Under ADR-0055 that child blocks in `io.ReadAll` forever,
and blocks silently: no banner line names the wait, so the operator
sees a process that produces nothing and reads it as a hang.

Reading to EOF is the right contract — it is what `claude -p` does,
it is what every filter does, and a producer that is merely slow
(`curl … | gem-agent -p`) must not be cut off. The defect is the
silence. ADR-0033 §2 already states the rule for this runtime:
deliberate waiting must not look like a hang. Waiting on stdin is a
cause the status machinery does not see: it happens before the turn
starts, and the heartbeat is TUI-only — it never runs in `-p`.

## Decision

### 1. The wait is announced, once, after a short grace

`-p` still reads a non-terminal stdin to EOF. If EOF has not arrived
**2 seconds** after the read began, one line goes to stderr:

    [stdin: waiting for piped input to end (no EOF after 2s) — close the pipe, or run with < /dev/null if nothing should be attached]

The grace keeps the common shapes quiet: `< /dev/null`, a here-string,
and `echo … |` deliver EOF at once and print nothing. Only a pipe that
is actually slow or actually idle earns the line — which is exactly
the population that needs it. The line names both remedies, because
the operator reading it does not know which case they are in.

### 2. The wait gets a closing line

When the notice was printed, the read's outcome is reported on the
same channel: the existing `[stdin: N bytes attached as data]` when
content arrived, and a new `[stdin: ended empty — nothing attached]`
when the pipe closed without data. A wait that was announced must
also be seen to end (ADR-0033 §2); a silent resolution would leave
the operator guessing whether the notice was the last thing that
happened.

### 3. Nothing else changes

The read stays bounded, binary stdin is still skipped with its
warning, an empty stdin still attaches nothing, a terminal stdin is
still never read, and stdout carries model text only. The notice is
stderr, like every other status line.

## Alternatives considered

- **An explicit `--stdin` flag** — rejected: it breaks the canonical
  `data | gem-agent -p` shape ADR-0055 exists to support, and drop-in
  compatibility with `claude -p` is the first requirement.
- **Skip the read when the pipe has no data immediately** — rejected:
  a producer that takes three seconds to fetch would be silently
  ignored, the very failure ADR-0055 fixed. Data-readiness cannot
  distinguish "slow" from "idle"; only EOF can.
- **A notice at the start of every non-terminal read** — rejected:
  `< /dev/null` is the recommended idiom for a headless launch, and
  stamping every such run with a stdin line makes the idiom noisy.
  The grace costs nothing on the fast path and puts the line only
  where a person might be waiting.

## Consequences

- The reported hang becomes a two-second wait followed by a line that
  says what to do. A genuinely slow producer sees the same line and
  loses nothing.
- Health-check runbooks and pipeline docs can name `< /dev/null` as
  the idiom for "no data intended" with the runtime itself pointing
  at it.
- The grace (2 s) is a constant, not a knob. It is the only wait
  notice in play for `-p`: the connection heartbeat (ADR-0033 §1,
  threshold revised by ADR-0056) belongs to the TUI and does not run
  in one-shot mode.

## References

- ADR-0033 §2 (deliberate waiting must not look like a hang)
- ADR-0055 (piped stdin as data; the clause this amends)

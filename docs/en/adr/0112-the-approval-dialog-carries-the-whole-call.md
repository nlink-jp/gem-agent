# ADR-0112: the approval dialog carries the whole call, and scrolls

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-09) — implemented |
| Date | 2026-11-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator (2026-11-09): "in the current UI you can't actually see what's in it"; a repository audit found the same |
| Relates to | ADR-0021 (the dialog; an approval prompt may not hide an argument), ADR-0060 (the reason field), ADR-0072 §2.7 (an unseen edit target), ADR-0093 (the TUI's input is made inert at entry) |

## Context

`CallDetail` was written for one-line event rows: a shell command is clipped to 300 runes, each other argument to 120, the line to 300. The same string was the only thing the approval gates received. The dialog then showed at most 8 rows of it. So a long command could put its effect past what the operator saw, an edit showed about 270 columns of its strings and little of its path, and the marker's advice was to deny and inspect some other way.

The dialog's job is informed consent. A count of hidden lines is honest about the problem and does nothing for it.

## Decision

1. **Two renderings of a call.** `Describe` / `CallDetail` stay the clipped summary for events, telemetry and records. The gates get `approvalDetail` / `FullCallDetail`: the full shell command, or every argument, `path` first, a long or multi-line value on its own lines. A tool's annotation (for example "replaces existing file") leads, so it is not lost below a long body.
2. **A bound with a statement.** One call carries up to 64 KiB. Past that the text says how many bytes were left out: a model can pass any size of `write_file`, and the dialog re-wraps its text as it draws.
3. **The dialog scrolls.** The window is up to 20 rows, less on a short terminal. ↑↓ move by a line, PgUp and PgDn by a page, Home and End to the ends. A marker gives the position and the hidden count whenever the text does not fit. A new question opens at the top.
4. **↑↓ leave the answer alone.** They used to move the selection, undocumented; ←→ and Tab still do. Arrow keys are what stay usable with a Japanese IME on, so scrolling cannot be letter keys.
5. **Reports stay short.** `denyGate` reports a call in one line (`ClipDetail`). The plain REPL's prompt prints the whole call, since a terminal's scrollback is its scroll.

## Consequences

- Reading is not enforced: the operator may answer without scrolling, and the marker says what they have not seen.
- The transcript's `gate_decision` keeps the clipped summary, as before. The full call is in the transcript's own tool-call record.

## Alternatives considered

- **Require scrolling to the end before `y`** — rejected: a long `write_file` an operator is happy to allow would demand ritual key presses.
- **A separate full-screen pager** — rejected: a second view and its keys, for what a window in the box already does.
- **Raise the clip limits** — rejected: it moves the cut without giving a way past it.

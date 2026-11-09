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

1. **Two renderings of a call.** `Describe` / `CallDetail` stay the clipped summary for events, telemetry and records. The gates get `approvalDetail` / `FullCallDetail`: the full shell command, or every argument: `path`, then the other short values, then the long ones, so a long body cannot push a short argument off the first screen. A value that is not a string is rendered as JSON, which keeps a list's boundaries and a number's form. A long or multi-line value is indented under its name. A tool's annotation (for example "replaces existing file") leads, so it is not lost below a long body.
2. **A bound with a statement.** The values of one call carry up to 64 KiB. Only a call over that is cut, and then only what is over its share of the limit, so short arguments are untouched. A cut value keeps its start and its end, because the end of a command is where its effect can be, with the omission marked in place. The first line of the detail says how much is left out, since a mark further down warns nobody who does not scroll there. A model can pass any size of `write_file`, and the dialog re-wraps its text as it draws.
3. **The dialog scrolls.** The window is up to 20 rows, less on a short terminal. ↑↓ move by a line, PgUp and PgDn by a page, Home and End to the ends. A marker gives the position and the hidden count whenever the text does not fit; where not one row fits, it gives the count and says not to approve. A new question opens at the top. Tabs are expanded before the wrap: the wrap counts a tab as no cells and the renderer as several, so a row would outgrow the box and lose its end with no sign. Every row of the call starts with `┊`, so the eye can tell where the call begins and ends. macOS Terminal's default profile may keep the page, Home and End keys for its own scrollback unless Shift is held; ↑↓ work everywhere.
4. **↑↓ leave the answer alone.** They used to move the selection, undocumented; ←→ and Tab still do. Arrow keys are what stay usable with a Japanese IME on, so scrolling cannot be letter keys.
5. **Reports stay short.** `denyGate` reports a call in one line (`ClipDetail` folds the lines and clips). The plain REPL's prompt prints the whole call, since a terminal's scrollback is its scroll.

## Consequences

- The layout is for reading and proves nothing. The text is the model's: a wrapped row, the declared purpose or a multi-line command can be made to look like another argument or like the dialog's own rows, and an earlier draft that promised otherwise was withdrawn after review. What is promised is finite: every argument is present, short ones first, tabs do not lose a line's end, and what is left out is stated.
- Reading is not enforced: the operator may answer without scrolling, and the marker says what they have not seen.
- The transcript's `gate_decision` keeps the clipped summary, as before. The full call is in the transcript's own tool-call record.

## Alternatives considered

- **Require scrolling to the end before `y`** — rejected: a long `write_file` an operator is happy to allow would demand ritual key presses.
- **A separate full-screen pager** — rejected: a second view and its keys, for what a window in the box already does.
- **Raise the clip limits** — rejected: it moves the cut without giving a way past it.

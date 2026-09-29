# ADR-0094: a narrowing sweeps the frame's rows, not the screen

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-29) — measurement arms and `make resizeprobe` built; the choice waits on the operator's readings from iTerm2 and kitty |
| Date | 2026-09-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator measurement, 2026-09-29 (ADR-0092 §4 and its Consequences; CHANGELOG "Known limitations"): narrowing the window loses the pictures on the visible screen and piles black space into the scrollback, on iTerm2 and kitty, with text alone as well |
| Relates to | [ADR-0003](0003-bottom-pinned-layout.md) (the pin), [ADR-0021](0021-review-fixes.md) (**§9's shrink-only clear is what this revisits**; its rejection of clearing on growth is untouched), [ADR-0024](0024-bottom-hold.md) and [ADR-0028](0028-self-healing-line-counter.md) (the counter the sweep must leave true), [ADR-0089](0089-inline-images-declare-their-height.md) (the declared box and its accounting are not touched), [ADR-0092](0092-mermaid-fences-render-as-pictures.md) (§4 names this as a known limitation to be revisited in its own ADR) |

## Context

### What happens on a narrowing today

The terminal reflows first: every row of the inline frame (the input box,
the footer, and the pad or hold rows above them) is a line this runtime
wrote at the old width less one column, and at a narrower width each such
line re-wraps into more rows. Bubble Tea's inline renderer then repaints
relative to the cursor — it moves up `linesRendered − 1` rows and draws —
so it starts below the frame's re-wrapped top, and the rows above it keep
stale copies of the input box (the staircase, AGENTS.md).

ADR-0021 swept them with `tea.ClearScreen` on a genuine width shrink, once,
and reset the row counter (`hold.printed = 0`, `hold.lastTotal = 0`). The
clear is the renderer's `ESC[2J` followed by the cursor home — the whole
screen, not the frame. §9 of that ADR rejected clearing on growth as well.
All of this is read from the source (`internal/tui/model.go`, bubbletea
v1.3.10 `standard_renderer.go`).

### What it costs — measured

The operator, 2026-09-29, on iTerm2 and kitty (ADR-0092 §4):

- **Pictures on the visible screen at that moment are lost.** kitty deletes
  them; iTerm2 leaves black space where they were. Text survives into the
  scrollback. On kitty, pictures higher up in the scrollback survive.
- **Black or empty space piles into the scrollback** on both terminals, with
  text-only content too.
- Widening the window earlier also showed extra empty space.

A picture does not survive a clear the way text does, and mermaid diagrams
(ADR-0092) and `/show` pictures (ADR-0091) both reach the screen as
pictures, so the defect is now visible where it used to cost nothing.

### What is not known, and why it is measured before choosing

The source tells us what bytes are sent. It does not tell us what these two
terminals do with them, and every candidate below rests on one of these:

1. **Where the black space comes from.** Either the terminal moves the screen
   into the scrollback on `ESC[2J` (some do; that would carry the frame and
   the empty rows with it), or the reset counter pads the next frame down from
   row one and the empty rows scroll up later — or both.
2. **Whether a picture survives the reflow itself.** If a terminal drops or
   blacks out an on-screen picture when it re-wraps, no sweep can keep it,
   and the decision is a different one.
3. **Where the re-wrapped frame lands.** The candidate below assumes the
   terminal keeps the cursor on its logical line and grows the frame upward
   from it, so the stale rows lie directly above where the renderer resumes.
   A terminal that grew the region downward, or pushed part of it into the
   scrollback, would leave rows the sweep cannot reach.
4. **How a drag arrives.** One drag delivers several size reports. Whether a
   repaint lands between two of them decides whether a sweep's row count
   must describe one reflow or several.

## Candidates, built as measurement arms

The model gains a seam, `tui.Options.Shrink`, whose zero value is today's
behaviour. The other two exist so the operator can measure them against it
in the same run; the one not chosen is removed when this ADR is decided.

**A. Clear the screen (today, the control).** `ESC[2J`, cursor home, counter
reset — ADR-0021 §9 unchanged.

**B. Leave it alone (the control for the reflow).** Nothing is swept and the
counter is not touched. It shows the staircase on purpose: it is the only
arm that answers questions 2 and 3, because it is the only one that does not
act on the rows it is measuring.

**C. Erase the frame's rows.** On a width shrink, compute K — the rows the
frame gained by re-wrapping — from the last frame the renderer actually drew:
for every line above the cursor's line, `physicalRows(line, newWidth) − 1`,
the same wrap arithmetic the counter already uses. The renderer's next
repaint is extended to start K rows higher, and everything from there down
is erased (`ESC[J`, erase below). Nothing above the frame's re-wrapped top
is touched, so history and the pictures in it stay where they are. The
counter is set so the new frame occupies exactly those rows: `lastTotal = n
+ K` (n the drawn frame's line count, capped at height − 1) and `printed =
height − 1 − lastTotal`. This is the operator's suggestion — "erase only the
rows the frame can occupy after reflow" — with the rows found relative to
the cursor instead of as the bottom K rows of the screen: the cursor is the
only position this runtime knows without asking the terminal, and the bottom
of the screen is the same place only if question 3 comes out as assumed.

How C reaches the terminal is part of the design, because both obvious
routes fail:

- **Not through `tea.Println`.** A queued line is flushed from the top of the
  renderer's region and always followed by `\r\n`, so a sweep printed that
  way costs one row of history — a blank row in the scrollback per size
  report, which is the defect in a smaller dose.
- **Not in `View()`.** Bubble Tea flushes only the latest view each tick and
  re-sends every line after a repaint, so a one-shot prefix is either lost
  (a second view replaced it before the tick) or repeated (a later repaint
  sent it again, moving up another K rows over real history).

So C is delivered by a writer between the renderer and the terminal
(`tui.SweepWriter`, passed with `tea.WithOutput`). Between flushes the
renderer leaves the cursor at column 0 of its region's last line, and every
flush of a frame taller than one line begins with `CSI n A` (cursor up
n = linesRendered − 1). The writer rewrites the first such flush after a
shrink to `\r CSI n+K A CSI J`. That flush is atomic with the erase, and it
comes from the renderer's own bookkeeping — which is also why the writer,
not the model, tracks which frame was last drawn: the model sees views the
renderer may never have flushed. When two size reports arrive with no flush
between them, the second replaces the first's K rather than adding to it —
the screen still holds the frame drawn before both, re-wrapped to the latest
width — which is question 4, and the probe reports how often it happens.

This couples C to bubbletea v1.3.10's flush format. A test drives the real
renderer through the writer and fails if the flush stops beginning with the
cursor-up the rewrite depends on, so an upgrade that changes it cannot pass
silently.

## Measurement

`make resizeprobe` (`tools/resizeprobe`) runs one arm in the operator's own
terminal. As pinprobe does, it builds the real model (`tui.New`), runs it
under the real inline program, and prints through the real emit path
(`tui.Output`, `tui.Image`) — so `wrapForScrollback`, `physicalRows`, the
bottom hold and the declared image box are production code. It:

1. clears that tab's screen and scrollback (after the operator presses Enter,
   so a run is read from a known start), fills the screen past its height
   with numbered lines — some near the width, so history reflows too — and
   types a long draft into the input box, so the frame's own lines reflow;
2. where the terminal draws, prints two pictures through the `/show` path,
   bracketed by marker lines: one pushed up into the scrollback, one left on
   the visible screen;
3. asks for a narrowing, waits for it and for the drag to settle, prints
   numbered lines until everything on the screen at the time of the resize
   has scrolled into the scrollback, then asks for a widening and does the
   same;
4. after exit, prints the size reports it saw, how many flushes ran and how
   many the writer rewrote, and the checklist the operator reads by eye.

What each reading means:

| Reading | Where | Meaning |
|---|---|---|
| Stale frames | footer or draft sentinels, or any unnumbered text, above `PROBE-END` | the sweep missed rows |
| Black space | blank rows outside the picture markers | ED 2 or padding put empty rows into history |
| Missing numbers | a gap in a numbered series | the sweep erased history — worse than a stale frame |
| Pictures | the eye: each picture kept, black, or gone | question 2 (arm B) and the purpose of the change (arm C) |

The text readings are counted by `resizeprobe -analyze` from a copy of the
tab's text (iTerm2: Select All and Copy, then `pbpaste | …`; on kitty, where
the operator can export the scrollback as text), so they come from the
terminal, not from what the probe meant to draw. Pictures are text-less and are read by eye, per picture.

`resizeprobe -drive` runs all three arms under tmux, resizing the pane
itself, and prints the text readings. tmux reflows but does not draw iTerm2
or kitty pictures, so it answers the text questions for tmux only — a
control that the arms and the analyzer work, not a reading of either
terminal.

### Readings so far

tmux 3.7c, `resizeprobe -drive`, a 120×30 pane narrowed to 80 and widened
back, 2026-09-29 (the captures are `tools/resizeprobe/testdata`, replayed by
its test):

| Arm | Stale frame rows after the narrowing | Black space | Missing lines |
|---|---|---|---|
| A clear | 2 — the draft and the footer, in the history | 0 | 0 |
| B none | 1 — the draft's first wrapped row | 0 | 0 |
| C erase | 0 | 0 | 0 |

tmux keeps a cleared screen in its history, so today's clear leaves a whole
copy of the frame there; B confirms, for tmux, question 3's assumption —
the stale row lies directly above where the renderer resumed — and C removed
it without touching a line. Watched on the screen, A also drew the frame one
row above its pinned place, with two empty rows below it; C left one empty
row more than usual between history and the frame — the gap the counter
holds, ADR-0024 — and the capture shows no empty row in the history after
it, so the lines printed next took it back rather than pushing it up. None of
this is a reading of iTerm2 or kitty.

## Decision

**Pending the operator's readings.** The criteria are set now, so the
readings decide rather than justify:

- C is adopted if, on both terminals, it leaves no stale frame, no black
  space outside picture boxes, no missing number, and the picture that was
  on the visible screen is kept.
- If B shows that a terminal drops the picture on reflow alone, no sweep
  keeps it; the decision is then between C and A on the remaining readings,
  and the limitation is recorded against the terminal.
- If C leaves stale rows because a terminal does not grow the frame upward
  from the cursor, that terminal's behaviour is written here and the design
  returns to this section, not to a margin added to K: an over-estimated K
  erases real history on the screen, which is worse than the staircase.
- Growth is measured in the same run and changes nothing until its readings
  exist; ADR-0021 §9's rejection of a clear on growth stands.

## Consequences

- Until decided, the product behaves as before: `Options.Shrink` defaults to
  the clear, and the writer is used only by the probe.
- If C is adopted: `cmd` passes the writer with `tea.WithOutput` and the
  model arms it; the clear, arm B and the seam are removed; the CHANGELOG's
  known limitation and ADR-0092 §4 are updated to point here; AGENTS.md's
  resize gotcha ("a genuine shrink additionally returns tea.ClearScreen
  once") is rewritten.
- ADR-0089's accounting is unchanged in every arm: `emitSegments`, the
  declared rows and `physicalRows` are not modified, and C only sets the
  counter to the rows it erased.
- lagent has the same shrink clear (its `internal/tui/model.go`). The TUI's
  scrollback accounting is a shared mechanism (AGENTS.md, "The sibling
  runtime"), so the decision is ported there in the same piece of work.

## Alternatives considered

- **Redraw the pictures after a resize.** A picture already in history would
  be drawn a second time, and pictures in the scrollback already keep the box
  they were emitted with (ADR-0092 §4). Rejected.
- **Ask the terminal where the cursor is (`ESC[6n`) and sweep absolute
  rows.** A terminal query while Bubble Tea owns stdin; the reply arrives as
  keystrokes (AGENTS.md, ADR-0089 §7). Rejected.
- **The alternate screen.** Gives up the inline transcript the TUI is built
  on (ADR-0002). Rejected.
- **Sweep once, after the drag settles.** K would have to cover frames drawn
  at every intermediate width and re-wrapped again; sweeping at each report
  keeps K to one reflow of one drawn frame. Kept per report.
- **Bubble Tea v2's renderer.** A migration, not a fix, and out of scope here.

## References

- `internal/tui/model.go` (`Update` on `tea.WindowSizeMsg`, `View`, `bottomHold`)
- bubbletea v1.3.10 `standard_renderer.go` (`flush`, `clearScreen`, `repaint`)
- `tools/pinprobe`, `tools/rowprobe` (the method this probe follows)

# ADR-0094: a narrowing sweeps the frame's rows, not the screen

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-29) — measured on iTerm2 and kitty, by hand and driven; C (erase the frame's rows), D (frame rows as short as their text) and E (narrow while resizing) proposed, awaiting the operator's acceptance |
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

**iTerm2 and kitty, by hand** (the operator, 2026-09-29, one run per arm on
each, narrowing by a drag; iTerm2 219×117 → about 140, kitty 269×77 → about
176; stale copies counted per copy, so several glued on one row count
separately):

| Terminal | Arm | Pictures (by eye) | Stale frame copies | Black space | Lost text |
|---|---|---|---|---|---|
| iTerm2 | A clear | both kept | 7 (one per shrinking report) | 565 rows | none |
| iTerm2 | B none | both kept | 4 drafts and fragments, glued into one row | 0 | none |
| iTerm2 | C erase | both kept | 2 drafts glued into one row (in each of two runs) | 0 | none |
| kitty | A clear | the on-screen one erased with its markers; the one above drawn a picture's height below its markers | 0 | 0 | 49 lines — everything on the screen |
| kitty | B none | both kept, in place | 1 draft | 0 | none |
| kitty | C erase | both kept, in place | 1 draft | 0 | none |

**Driven** (`resizeprobe -auto`, a new window at 160×45, nobody at the
keyboard, 2026-09-29): one step to 106 — kitty A lost 23 lines, PIC-2 and its
markers exactly as by hand, B left 1 draft, C left nothing; iTerm2 C left
nothing at the width equal to the drawn input line (158) and one column wider;
eight steps 160 → 106 at 200 ms (iTerm2's own report interval) left nothing
on either; at 30 ms kitty coalesced them into one report and left nothing,
iTerm2 reported all eight and left one empty row in the history but no stale
copy; fifty-four one-column steps at 10 ms left nothing on either.

What the readings answer:

1. **The black space is the clear.** iTerm2 moves the screen into the
   scrollback on `ESC[2J`, so every shrinking report in a drag (iTerm2 sends
   one about every 200 ms) pushes one nearly empty screen with a frame copy
   into it. kitty erases in place, so it piles nothing — and loses whatever
   text and pictures were on the screen instead. Neither B nor C put black
   space anywhere.
2. **A reflow alone keeps the pictures**, on both terminals, in place. Every
   picture lost or moved was lost or moved by the clear.
3. **The stale rows lie directly above where the renderer resumes**, as
   assumed: B's leftovers sit there on both terminals, and C removed them in
   every driven run.
4. **iTerm2 reports a drag about every 200 ms; kitty sends one report per
   drag.** At 200 ms a flush runs between reports, so a sweep describes one
   reflow.

Not explained by these readings: by hand, C still left one or two stale
drafts on both terminals, where no atomic driven resize did. The first guess,
that iTerm2 gives a line exactly as wide as the screen two rows, was refuted
by driven runs at exactly that width. The next section finds the cause.

### The cause: a repaint at a width already gone

The probe was extended to drag the window's edge with the mouse
(`-auto -drag`, synthesized events, refused unless the window is topmost at
the drag point) and to trace the writer: every view, every flush with the
cursor-up it began with, every arm and sweep, and the counter the model set.

- The accounting was consistent at every step: each sweep's flush began
  with the cursor-up of the frame last drawn, and K matched the frame.
- A mouse drag on kitty left nothing; on iTerm2 it left no stale copy but
  two empty rows in the history, twice. iTerm2 reports a drag about every
  200 ms while it reflows continuously; kitty reports once, at the end.
- Holding reports back and delivering only the last, as kitty does, made
  iTerm2 **worse**: three stale drafts glued into one row — the by-hand
  picture. The trace shows why: while the reports were held, the renderer
  still flushed every ~530 ms — the input box's cursor blink — at the width
  last reported, into a terminal already narrower. The row was as wide as
  that old width, the terminal wrapped it, and the renderer counts rows it
  wrote, not rows the terminal made.

So the stale rows come from any repaint that lands while the terminal is
narrower than the renderer was last told, and the reason one lands wide is
that **every row of the input box is drawn padded with spaces to the full
width**, whatever was typed. A hand's drag lasts long enough for several
blinks.

**D. Keep frame rows as short as their text.** Every frame row ends at its
last visible cell (`shortRows`, after the width clip). Padding drawn on a
background — the input line's highlight — becomes an erase to the end of the
line under that background (terminals fill erased cells with it) followed by
a move to the last column, so the renderer's own erase, which comes after
the row's reset, takes only that cell: the highlight still spans the window
and now follows its width. The input box's cursor, a reversed blank cell, is
kept (the first prototype dropped it; kitty showed the bar in reverse and no
cursor, and the fix was measured again). Screenshots of both terminals, with
and without D, show the same frame.

Readings with D (driven, a new window at 160×45; "short" is a few words, as
an idle session has; "long" fills most of the width):

| Case | iTerm2 | kitty |
|---|---|---|
| short, C, eight steps at 30 ms (iTerm2 left an empty row without D) | clean | — |
| short, C, reports held back (three drafts without D) | clean | — |
| short, **B** — nothing swept — eight steps at 30 ms | clean | — |
| long, C, eight steps at 30 ms | clean | clean |
| short, C, one step | — | clean |
| short, C, **mouse drag** | clean | clean |
| short, **B**, mouse drag | clean | clean |
| long, C, mouse drag | **one stale draft** | clean |

"Clean" is no stale copy, no empty row in the history and no lost line. The
one left: the long draft was, at each report, exactly as wide as the width
reported (152 at 152, 146 at 146), so K was 0 — and by the time the report
arrived the terminal was already a column or two narrower and had wrapped
it. K describes the width reported; a row near that width is where a lag
shows.

### The lag, and a fifth candidate

The one row left comes from the lag itself, so reading a fresher width was
tried first. The probe polled the kernel's window size (`TIOCGWINSZ`, an
ioctl, every 2 ms) through a mouse drag on iTerm2: it changed only when the
terminal reported, about every 200 ms, never between. iTerm2 moves its own
screen continuously and tells the program — report and kernel alike — late.
No width the program can read describes the screen while a drag is under
way, so no K computed from one can be exact then.

**E. Draw the frame narrow while a resize is underway.** From a size report
until none has come for 400 ms (twice iTerm2's interval), every frame row is
clipped to the narrowest width the model lays out (`minWidth − 1`, 19
cells); the settling tick of the last report draws the full frame, at a
width that has stopped moving. A row that short cannot wrap on any screen
the model lays out for, however far ahead of its report the terminal is. It
is visible: while the edge moves, the input line and the footer are cut to
19 cells (a screenshot taken mid-drag shows it), and they come back when the
hand stops.

With C, D and E, by mouse drag (a new window at 160×45, dragged to two
thirds over 1.5 s): iTerm2 with a draft as wide as the window, three runs —
the case that left a row without E — clean; iTerm2 with a short draft
clean; kitty with a long draft clean. Driven runs at 30 ms and 200 ms were
clean on both, narrowing and widening. "Clean" is as above.

## Decision

**Proposed: C, D and E together; awaiting the operator's acceptance.** D keeps
frame rows as short as their text, so an ordinary draft never wraps; E keeps
every row short while the terminal is ahead of what it has reported; C
erases what a long row still gained. Against the criteria, set before
measuring — no stale frame, no black space outside pictures, no missing
line, the on-screen picture kept — every measured case met all four, on
both terminals. An earlier proposal (C with D alone) had left one row under
a mouse drag on iTerm2 and would have relaxed the first criterion; the
operator declined that, and E closed it instead.

Not measured: a turn streaming into the scrollback during a drag. Lines
emitted then are wrapped for the width last reported, and a line the
terminal wraps again is a row the counter did not count — the pin drifts,
as it did before this ADR. No stale frame follows from it, since the frame
is narrow while it happens.

The criteria, as set before measuring:

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

- D and E are in the product already, as the prototypes this ADR measured:
  the view is clipped, then shortened, and clipped narrow while a resize is
  underway. The clear is still the product's shrink until this ADR is
  accepted.
- If C is adopted: `cmd` passes the writer with `tea.WithOutput` and the
  model arms it; the clear, arm B and the seam are removed; the CHANGELOG's
  known limitation and ADR-0092 §4 are updated to point here; AGENTS.md's
  resize gotcha ("a genuine shrink additionally returns tea.ClearScreen
  once") is rewritten.
- ADR-0089's accounting is unchanged in every arm: `emitSegments`, the
  declared rows and `physicalRows` are not modified, and C only sets the
  counter to the rows it erased. D shortens the managed view only; nothing
  printed into the scrollback passes through it.
- While the window is being resized, the input line and the footer are
  drawn cut to 19 cells, for 400 ms after the last size report.
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

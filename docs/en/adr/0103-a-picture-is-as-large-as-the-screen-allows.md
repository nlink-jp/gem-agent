# ADR-0103: a picture is as large as the screen allows, and no larger than itself

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-02) |
| Date | 2026-11-02 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: an image drawn inline (`show_image`, `/show`) is too small for the terminal window to see well |
| Amends | ADR-0090 §3 (the ceiling and the width a picture's box is chosen against) |
| Relates to | ADR-0089 (the declared box), ADR-0092 §4 (the cell read with `TIOCGWINSZ`) |

## Context

`termimg.BoxFor` chose a picture's box against two fixed ceilings: at most
**40 columns**, whatever the terminal's width, and at most **a third of the
screen's height** (`maxImageRows`). The rows came from a constant cell aspect
of 2.25. On a 200×50 window a square photo was drawn 16 rows by 36 columns —
a fifth of the window's width. The 40-column box had already been measured
unreadable for diagrams (mermaid-render RFP, stage 0; ADR-0092 B5), and the
operator now finds it too small for photos and screenshots as well.

Two facts measured for ADR-0092 bear on the replacement:

- The terminal reports the cell's size in pixels through `TIOCGWINSZ`, an
  ioctl rather than a query, so it is safe while Bubble Tea owns stdin. On the
  operator's Retina screens iTerm2 reported 8×18 and kitty 7×13 — the
  screen's points, not its device pixels (stage 0).
- The cell aspect differs by terminal (2.25 and 1.86). The stage-0 record
  noted that if kitty stretches a picture to the declared box, the constant
  2.25 squashes a photo vertically by about 17% there. That was not measured
  for photos; it is a reason to shape the box by the cell that was read
  rather than by a constant, whichever way kitty behaves.

## Decision

### 1. The box is as large as the screen allows

The width ceiling is the terminal's width less one column (ADR-0089 §2's
margin, applied by `Fit`), not 40. The height ceiling is the screen above the
frame: the terminal's height, less the rows the frame occupies at the moment
the picture is drawn, less the one row the pad keeps free. A picture that
lands is therefore wholly on screen, and the conversation before it scrolls
away — the operator's choice, over half the screen. The frame is measured
the way the view lays it out (`viewContent`, clipped and trimmed), and never
by calling `view`, which writes the hold accounting.

A picture is shrunk to fit both ceilings and keeps its shape. An unknown
terminal width keeps the old 40 columns, an unknown height the old 10 rows:
a guess would be worse than a modest number.

### 2. No larger than the picture itself

A picture's native size, in cells, is its pixels over the cell's reported
pixels — one pixel of the picture to one pixel the terminal reports. The box
is never larger than that, so an icon is not blown up to fill the screen.
Since iTerm2 and kitty report points on a Retina screen, a picture is drawn
at most at the size a browser shows it at 100%. A terminal that reported
device pixels would draw it smaller, never larger. The box is rounded to
whole cells, so it can exceed the native size by less than half a cell.

A terminal that reports no pixel size gets iTerm2's measured 8×18, whose
aspect is the 2.25 the box was shaped by before. A reported cell whose aspect
falls outside 1–4 is treated the same way, as `DiagramBox` already does.

### 3. The cell is read once per size report

`Options.CellAspect` becomes `Options.CellSize`: the same ioctl, returning
the cell's width and height. The aspect the diagram box uses (ADR-0092 §4)
is derived from it, so one reading serves both lanes and a report without
pixels still leaves the last reading in place.

## Consequences

- A large picture now fills the window's width or the screen above the
  frame, whichever binds first. The reply it belongs to may scroll off the
  top; it is in the scrollback, as a long reply's beginning is.
- `maxImageRows` depends on the frame, which grows while a turn runs (the
  live tail, the thought view). A picture shown mid-turn is smaller than the
  same picture shown with `/show` at an idle prompt. That is the price of
  keeping it whole on screen.
- Kitty re-encodes a non-PNG picture scaled to the box (`kittyPNG`); the box
  is larger now, so a JPEG photo re-encodes to a larger PNG. `MaxBytes` still
  bounds it, and the picture is refused, not shrunk further, past it.
- ADR-0089 A2 rejected deriving the box from the cell's pixel size because it
  needed an `ESC[16t` query that can go unanswered. The size is now read with
  the ioctl ADR-0092 introduced, which needs no answer; A2's objection does
  not apply to it.

## Alternatives considered

**A1. Half the screen, as a diagram band.** Leaves the reply above visible,
but halves the picture on every terminal. The operator chose the whole screen.

**A2. A `[tui]` setting for the ceiling.** A knob with one sensible value is
a knob nobody needs; it can be added on evidence.

**A3. Enlarge small pictures to the ceiling as well.** The old box did
(every picture was 40 columns). Rejected by the operator: an icon drawn
across the screen shows nothing more than the icon.

# ADR-0024: Bottom-hold — a stable frame once the screen is full

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: once history reaches the bottom of the screen, the footer jumps up and down — visibly around MCP calls and MITL dialogs |

## Context

ADR-0003 pins the input block to the window bottom by padding the view:
`pad = height − printed − core − 1`. While the screen is not yet full,
the pad absorbs every change in the view's own height, so the footer
sits glued to the bottom row. Once printed content fills the screen the
pad clamps to zero and the design said "degrade to plain inline" — which
means the frame's bottom edge now moves with the frame's height. The
frame height changes constantly: the live tail grows from 0 toward 12
lines while streaming and resets to zero at every flush (each tool-call
boundary — MCP calls foremost), and the approval dialog adds and removes
an 8–14 line box. Result: the footer bounces by up to a dozen rows
exactly when a tool call or an approval intervenes.

## Decision

**Hold the frame's total height steady in the full-screen regime.** The
model keeps a small render-state (`lastTotal`, behind a pointer so it
survives Bubble Tea's model copying):

1. While the pad is positive (screen not full), behaviour is unchanged
   and `lastTotal` stays disarmed — the pad is the absorber.
2. Once the pad reaches zero, each frame renders at
   `total = clamp(max(lastTotal, core), core, height−1)` lines, the
   difference `total − core` as blank lines at the top. A shrink
   (flush reset, dialog closing) leaves the frame bottom — the footer —
   exactly where it was, with the vacated rows blank inside the frame.
3. Every line printed to scrollback decrements `lastTotal`: the printed
   line scrolls history up by one row, and giving that row back shrinks
   the blank region by one, so history flows seamlessly into the gap
   instead of a hole persisting above the live area.
4. The existing invariants stay on top: every line is width-clipped,
   the frame never exceeds `height − 1` (ADR-0021's clamp), and any
   resize resets the whole accounting through the established
   clear-and-recount path.

Ground truth for tests is the frame-height invariant: across a scripted
full-regime sequence — stream, flush, approval open, approval close —
the rendered frame's line count must not decrease except by exactly the
number of lines printed to scrollback in between.

## Consequences

- The footer stops moving once it reaches the bottom row; MCP-call
  flushes and MITL dialogs no longer bounce it.
- Transiently vacated rows show as blank space between history and the
  live area, consumed one row per subsequent scrollback line — the
  visual cost of a stable footer, and short-lived in any active turn.
- ADR-0003's "degrades to plain inline" clause is superseded by this
  bottom-hold; everything else in ADR-0003 stands.

## Alternatives considered

- **Fixed-height live tail** (pad the streaming region to 12 lines) —
  removes only the flush bounce, not the dialog bounce, and wastes the
  rows while idle.
- **Constant full-height frames** (always render height−1 lines) —
  kills the bounce but permanently separates history from the live area
  with a screen-sized blank region.
- **Compensating scrollback filler** (print blank lines on shrink) —
  pollutes the scrollback record to fix a rendering concern.

## References

- ADR-0003 (the pinning design this amends in its full-screen regime)
- ADR-0021 (the height−1 clamp this composes with)

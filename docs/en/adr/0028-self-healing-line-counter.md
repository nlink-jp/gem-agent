# ADR-0028: A self-healing printed-line counter

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: opening /settings and closing with ESC left the footer around mid-screen instead of the bottom |

## Context

The bottom pinning (ADR-0003) positions each frame against `printed`,
the count of rows above the frame's top. The settings panel budgets
itself to `height−1` rows — taller than the `height−1−printed` rows
actually left below already-printed content — so rendering it scrolls
the terminal by the overflow and moves the frame anchor up, while
`printed` stays put. The next, smaller frame (ESC back to the input
view) is then padded against rows that scrolled away: the footer lands
mid-screen. ADR-0024's bottom-hold masked the shrink but not the stale
anchor.

## Decision

**`printed` follows reality.** It moves into the pointer render state
beside the ADR-0024 hold, and View applies scroll accounting before the
pad math: when a frame's height exceeds the available rows
(`height−1−printed`), the terminal will scroll by the overflow as the
frame renders, so `printed` is reduced to `height−1−core` (floored at
zero) in the same pass. Every regime then stays consistent — the pad
formula, the bottom-hold arming condition, and the emit increments all
operate on a counter that means what it says: rows above the frame.

Ground truth for tests: after any render, `printed + frame ≤ height−1`,
and closing an over-tall panel keeps `printed + frame = height−1` — the
footer's bottom row.

## Consequences

- /settings (and any over-tall frame: big approval boxes on small
  terminals) closes back to a bottom-pinned footer.
- **The counter's convention is one row short of the physical anchor
  after an overflow, by design.** Measured 2026-09-10 in a 100x30 tmux
  pane: five printed rows and a 29-row frame scroll the terminal by
  four (a frame of N rows from row printed+1 scrolls printed+N-height),
  so the frame then occupies the bottom row too and one printed row
  stays above it — while the heal reads printed = 0, as if the bottom
  row were still free. That is consistent for everything positioned
  relatively (the renderer repaints from the previous frame, and the
  pad/hold arithmetic keeps the free-row convention; ADR-0024's test
  pins it), so the formula stays. It fails only where absolute rows
  matter: the settings panel, budgeted one row short of the screen,
  left exactly that surviving row standing alone above the input after
  ESC (operator report, v0.75.0; their diagnosis was the panel's
  bottom alignment, which also put a band of blank rows between the
  conversation and the title).
- The settings panel is therefore laid out by `settingsFrame`: while
  at least `settingsFitRows` rows remain below the conversation it is
  **top-aligned in those rows** — title right under the printed
  content, footer on the bottom row, its row window budgeted against
  those rows — so opening it scrolls nothing and the close gives the
  screen back as it was; otherwise the frame is **the terminal's full
  height**, the one frame allowed past height-1, so every printed row
  scrolls out, the heal lands at zero, and the close leaves a clean
  screen. **The choice is made once, at open, and held** (`settingsPlan`
  → `settingsTotal`): the renderer paints only the latest View per
  tick, so a choice re-derived from the counter on every View flipped
  to the fitted frame right after the full frame healed the counter,
  and the fitted frame — one row short — was what reached the terminal
  (raw bytes under `script`, same measurement).
- ADR-0003's "printed = lines emitted since the clear" definition is
  amended to "rows above the frame top, self-healed on overflow";
  ADR-0024 composes unchanged.

## References

- ADR-0003 (the pinning arithmetic this repairs)
- ADR-0024 (the hold state the counter now lives beside)

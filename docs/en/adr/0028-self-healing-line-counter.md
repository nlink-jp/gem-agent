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
- ADR-0003's "printed = lines emitted since the clear" definition is
  amended to "rows above the frame top, self-healed on overflow";
  ADR-0024 composes unchanged.

## References

- ADR-0003 (the pinning arithmetic this repairs)
- ADR-0024 (the hold state the counter now lives beside)

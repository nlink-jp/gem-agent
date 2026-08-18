# ADR-0003: Bottom-pinned inline layout

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator comparison with current Claude Code: its input box and status line pin to the window bottom, while gem-agent's trailed the conversation |

## Context

ADR-0002 chose inline rendering so native scrollback and copy/paste keep
working. Its side effect: the input block sits directly under the last
conversation line, floating high on an empty screen. Current Claude Code
pins the input to the window bottom **without** alt-screen — the layout
gem-agent's operator expects.

## Decision

Keep inline rendering; add bottom-pinning on top of it:

- **Clear the screen at TUI startup** and print the banner through the
  TUI, establishing a known cursor row. (Claude Code does the same; the
  pre-launch viewport content survives only in scrollback.)
- **Count every physical line the TUI prints** (`emit` wraps
  `tea.Println`; per line: `ceil(displayWidth / termWidth)`, ANSI-aware).
  All managed-view lines are already width-clipped (ADR-0002 resize fix),
  so view height is exact.
- **Pad the view's top** with `height − printed − viewHeight − 1` blank
  lines. Once the conversation fills the screen the padding floors at 0
  and behavior degrades exactly to the previous layout.
- **Resets**: the shrink-triggered ClearScreen zeroes the counter (the
  viewport is empty again). On growth the counter is kept — re-wrapped
  history may make the pin sit slightly above the bottom until the next
  clear; cosmetic and self-correcting.

## Consequences

- The input box and status line stay at the window bottom from the first
  frame, matching current Claude Code; scrollback/copy still work.
- Line counting is an estimate of terminal state; every known drift path
  (our wraps: prevented by clipping; glamour wraps: pre-wrapped ≤ width;
  resize: cleared or conservatively unpadded) degrades to "input not
  quite at the bottom", never to rendering artifacts.
- Startup clear removes the pre-launch viewport contents from view (they
  remain in scrollback on most terminals). Accepted — identical to
  Claude Code's behavior.

## Alternatives considered

- **Alt-screen** — true pinning for free, but destroys native scrollback
  and copy/paste; rejected in ADR-0002 and the rejection stands.
- **Cursor-position query (CSI 6n)** instead of counting — a runtime
  terminal query is the OSC-leak class of bug (phantom input); rejected.
- **Status quo** (conversation-trailing input) — what ADR-0002 shipped;
  rejected by operator feedback with current Claude Code as the bar.

## References

- ADR-0002 (inline rendering; width-clipping discipline this builds on)
- Operator screenshots, 2026-08-18

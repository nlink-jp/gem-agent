# ADR-0036: ask_user — a structured choice the model can put to the operator

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the model should be able to present options and ask the operator to choose, like Claude Code's Ask tool |

## Context

When the model needs a decision mid-turn — which of three approaches,
which file is the real target — its only move was to END the turn and
ask in prose. That costs a full round-trip, loses the momentum of the
turn (queued work, loaded context), and puts the burden of
enumerating the options back on the operator's reply. The approval
dialog already proves the mechanism: a tool call that blocks on a
channel while the TUI collects one keypress, IME-safe.

## Decision

### 1. One read-only tool: `ask_user(question, options[])`

The model supplies a question and 2–8 option labels; the operator
picks one; the tool result names the choice. `Mutating: false` and
never approval-gated — a gate on a question would be a dialog to
permit a dialog. Bounds: question clipped at 500 runes, options at
100 each, more than 8 options refused (a menu that long is a design
smell in the caller).

### 2. The dialog is the approval dialog's sibling

Same interaction grammar the operator already knows: ←→/Tab to move,
Enter to confirm, digits 1–9 select-and-confirm in one press, Esc
declines. The selection route exists for the same reason as in the
approval dialog: letters vanish into a composing Japanese IME; arrows
and digits do not. Esc returns a distinct result telling the model
the operator declined to choose — ask in plain text or proceed with
stated judgment — so declining is information, not an error.

A dialog arriving after Ctrl+C is auto-declined, exactly like an
approval request (ADR-0034): no dialogs on behalf of dead turns.

### 3. Every mode answers honestly

- **TUI**: the dialog.
- **Plain REPL**: numbered options on stderr, a number read from
  stdin (the approve.Gate pattern; EOF declines).
- **One-shot `-p`**: there is nobody to ask — the tool returns
  "no interactive operator in one-shot mode; decide yourself and
  state the choice you made" instead of hanging a pipeline.

### 4. No free-text option, deliberately

Claude Code's Ask tool carries an "Other" text field; this one does
not. gem-agent already has a free-text channel: the model ends its
turn and asks — the input box is the text field. The tool exists for
the structured case; duplicating the unstructured one inside a modal
dialog buys a second way to do the same thing at real TUI complexity.
Recorded here so the omission reads as a decision, not a gap.

## Consequences

- Mid-turn decisions cost one keypress instead of a round-trip.
- The transcript records the question and the choice as an ordinary
  tool call; telemetry (ADR-0035) audits it the same way.
- A model that over-asks becomes annoying rather than dangerous; the
  operator's lever is prose feedback (or Esc), and the 8-option cap
  keeps menus honest.

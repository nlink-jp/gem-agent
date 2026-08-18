# ADR-0007: Input typed while a turn is running

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | First monthly drill: typing during a run is discarded with no feedback |

## Context

While a turn runs, the TUI hands key events to nothing but two handlers:
Ctrl+C interrupts, shift+tab toggles auto-approve. Everything else is
dropped on the floor. The operator types a follow-up, sees no characters
appear, and has to retype it once the prompt comes back.

This is not a rare state. An agent turn with a few tool rounds runs for
tens of seconds, which is exactly when the next instruction occurs to
you — often *because* of what is scrolling past ("no, not that file").
Claude Code queues what you type; muscle memory carried over from it
produces lost text here, and drop-in familiarity is a stated goal of this
tool (RFP §1).

The dropping is silent, which is the worse half. A visible refusal would
at least be a decision the operator could see.

## Decision

The input box stays live while a turn runs, and **Enter queues one
message** instead of submitting it.

1. **Typing works during a run.** Characters, backspace, Ctrl+J newlines,
   paste — all reach the textarea exactly as at the prompt. The operator
   sees what they are writing.
2. **Enter queues.** The text leaves the box, is echoed into scrollback
   as a queued line, and waits. It is not sent mid-turn: the agent loop
   owns the conversation until it returns, and injecting a user message
   into a half-finished tool round would either corrupt the
   call/response pairing Gemini requires or race with the history the
   loop is writing.
3. **One message, not a queue.** A second Enter with something already
   queued replaces nothing and drops nothing — it appends as a new line
   to the pending message. Multiple independent queued turns would need
   the agent loop to chain turns unattended, which is a bigger change
   than the problem justifies, and the scope of this tool is deliberately
   the core 20%.
4. **Auto-send only on a clean finish.** When the turn ends normally, the
   queued message is submitted as the next turn. When it ends in an error
   or an interrupt, the text is **restored into the input box unsent**,
   with a note. This is the asymmetry that matters: a message typed
   during a turn that then failed was written against a world that no
   longer exists, and firing it into a broken state is precisely the
   surprise that makes people distrust queueing.
5. **Ctrl+C still interrupts, never clears.** During a run it is the
   escape hatch; overloading it to clear a draft would make the escape
   hatch conditional on the input box being empty.
6. **The approval dialog is unaffected.** While it is open, keys answer
   the dialog — a queued keystroke there would be an approval nobody
   meant to give.

## Consequences

- The common case — noticing a correction mid-turn and typing it — now
  keeps the text. The uncommon case, a turn that fails after you queued
  something, hands it back rather than sending it.
- Auto-send means a message can leave without a second Enter. That is the
  point (it is what makes queueing worth having), but it is why the clean-
  finish condition is a hard rule rather than a nicety.
- The status line gains no new indicator; the queued line in scrollback
  and the emptied input box are the feedback. One more permanent widget
  for a transient state is not a good trade.

## Alternatives considered

- **Leave it as is, add a hint line** — rejected: it makes the loss
  visible without making it stop. The text is still gone.
- **Send the queued message into the running turn** — rejected: the agent
  loop owns the history while it runs, and Gemini requires every function
  call to be paired with its response in one request. There is no safe
  place to splice a user message into a tool round.
- **Queue several messages and chain turns** — rejected for now: it moves
  the agent from "one turn per instruction" to an unattended queue
  runner, which changes what an interrupt means and what auto-approve
  covers. Revisit if one pending message proves insufficient.
- **Auto-send unconditionally** — rejected: see decision 4. A queued
  message sent into a failed turn is worse than a queued message handed
  back.

## References

- ADR-0002 (inline TUI; phases and the managed live region)
- ADR-0004 (auto-approve; the approval dialog's key handling is untouched)
- Monthly drill, first run (`reference/drill.md`) — where this surfaced

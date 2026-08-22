# ADR-0041: Whole-code review round 3 — findings and fixes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the codebase changed extensively (ADR-0032–0040), the agent loop included — review the whole of it again, looking for newly introduced defects |

## Context

Ten releases landed between the last whole-code review (ADR-0031) and
this one, several of them inside the turn loop: the child search agent
(ADR-0037), instruction context for the risk tier (ADR-0038), in-session
reload (ADR-0039), and the round-limit intervention ladder (ADR-0040).
The review combined a maintainer pass over the loop with three
independent reviewers (agent/llm, cmd wiring, TUI), `go vet`, and the
race detector across every package; every reviewer claim was re-verified
against the code or a probe before it counted. Sixteen findings, three
of them high.

## Fixed

### 1. The child search agent expanded `@`-references in a model-authored question (high)

`agent.Run` applied the `@` grammar to every input, and that grammar
grants out-of-project reads — images, documents, media by absolute or
`~` path — on the explicit premise that an `@` is always operator-typed.
ADR-0037 made the question model-authored without revisiting the
premise: a poisoned file could steer the main model into
`agentic_file_search(question: "… @~/Documents/contract.pdf …")`, the
child would attach the PDF natively, and its report would carry the
contents back — invisibly, since the child has no `onAttach`. Fixed with
`Options.NoMentions`, set for the child; the question's `@` tokens are
now plain text. Lesson, recorded for the knowledge base: a permission
justified by "only a human writes this input" becomes a hole the moment
a model can write that input — delegation must re-audit every input
channel's trust premise.

### 2. The live region never expanded tabs (high)

`emit()` expands tabs before the scrollback width clip, but `liveView()`
returned raw streamed text, and `ansi.Truncate` counts `\t` as zero
cells while the terminal advances to the next stop. A tab-indented code
line passed the clip and soft-wrapped the managed view — the exact
renderer-desync class fixed in v0.34.1, re-entering from the other
side. `liveView` expands tabs now; the probe that measured the gap
(3 tabs + 90 chars: clipped to 79 counted cells, 103 rendered) is a
test.

### 3. A second `bufio.Reader` over stdin (high)

The ask_user path (ADR-0036), now also carrying the round-limit dialog,
wrapped `os.Stdin` in its own reader while the plain REPL and the
approval gate share one — the configuration AGENTS.md forbids by name:
typed-ahead input strands in whichever buffer filled first, and a piped
driver can hang the session. The asker now reads the shared reader; a
source-level test pins the absence of a second reader.

### 4. The ask dialog truncated what it asked (medium)

The question rendered on one line, clipped at 200 runes and then cut
at the terminal width with no marker (measured on the round-limit
dialog: the reviewer's reason vanished mid-word). The box now wraps the
question and the options to its inner width, budgets the height, and
discloses hidden lines — the approval dialog's discipline, applied to
its sibling.

### 5. The stall detector had three holes (medium)

`toolRunning` was set by a tool call and cleared by **any** stream
chunk — but during a tool the only streams are side-calls (the risk
evaluation, a progress review), so an auto-approved long tool produced
a false stall warning and the evaluator's thoughts rendered as the
model's; `!command` armed the heartbeat and warned about a connection
it never had; and the flag survived an interrupted turn, disarming the
next turn's detector. Now the agent emits `OnToolDone` after every call
(executed, denied, or skipped), the TUI re-arms on that and only that,
side-call thoughts are ignored while a tool runs, turn boundaries reset
the flag, and the shell path holds it for the command's duration.

### 6. The thought line showed the oldest words (medium)

Front-trimmed to two lines' worth of runes and then clipped to one
width — the freshest thought text was never visible. It now wraps and
keeps the last two physical lines.

### 7. Checkpoints after Ctrl+C, and audit gaps (medium)

`roundIntervention` and `plainAsk` ignored a cancelled context, so a
plain-REPL turn interrupted just before a checkpoint still blocked on
a prompt; both check first now. Automatic compaction never reached the
audit stream — only `/compact` emitted the event; the emission moved
into `Agent.Compact`, so both paths record. Non-interactive extensions
were silent; they announce themselves now.

### 8. Smaller fixes (low)

The child detects the round-limit error with `errors.As` on a typed
`RoundLimitError` instead of matching the wording; its report
truncation is rune-safe; the loop trigger's evidence includes the
triggering call, and loop-guard-skipped calls are audited as
`outcome=skipped`; the dialog's reason is clipped; `/usage` labels the
reviews honestly ("risk & progress reviews"); the startup-notes tee
freezes after the banner instead of recording the whole session;
`releaseTurn` drains a pending approval as well as a pending ask; an
`AskRequest` with no options cannot panic the UI.

## Deliberately not changed

- `…` (U+2026) is East Asian Ambiguous; terminals set to render
  ambiguous glyphs wide could put a line clipped to width-1 onto the
  last column. Environmental, bounded to the last column's pending-wrap
  state, and not reproducible on the development terminals; noted.
- `/mcp reload` still blocks its surface while servers connect
  (ADR-0039 accepted this); a wedged server looks like a hang for up to
  the connect timeout. Future refinement: run it asynchronously with a
  progress line.
- Ctrl+C within the dialogs' 300 ms typed-ahead grace is swallowed like
  any key (one extra press); the grace protects against answering a
  dialog with an in-flight keystroke and is worth that price.

## Consequences

- The child search agent's confinement is now actual, not assumed.
- The inline renderer's width invariant holds on both sides — managed
  view and scrollback — with tabs covered on both.
- The heartbeat, the dialogs, and the plain REPL's prompts behave the
  same under interruption, auto mode, and side-calls.
- The review's method (maintainer pass + independent reviewers + race
  detector, every claim re-verified) is the template for the next round.

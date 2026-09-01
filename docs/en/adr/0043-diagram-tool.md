# ADR-0043: diagrams are drawn by a tool, not by rewriting what the model wrote

| Field | Value |
|-------|-------|
| Status | **Superseded by [ADR-0063](0063-diagram-fences-render-in-place.md)** |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Amends | [ADR-0042](0042-terminal-diagrams.md) |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the runtime inspects the chat and rewrites it, so when the model's mermaid is malformed the model never finds out; and the console's dimensions, which decide whether a diagram draws at all, are never told to it |

## Context

ADR-0042 draws mermaid blocks by rewriting the assistant's Markdown at
flush time. `diagram.Rewrite` is called from exactly one place — the
Markdown renderer in the TUI — and its outcome reaches the screen and
nothing else. The model's conversation keeps the original fence. It is
never told that its diagram was refused, or why.

That is the failure this project spent v0.38.0–v0.39.0 removing
elsewhere, one level deeper. v0.38.0 taught the model the dialect that
draws, and compliance was measured clean; but a model that still gets
it wrong has no way to learn, because the correction is invisible to
it. The three legitimate responses to imperfect model output are teach,
verify-and-reject, and surface to the human. Rejecting is honest —
**rejecting without telling the author is not.**

The second half is arithmetic the model cannot do: whether a diagram
draws depends on the terminal's width, which nothing in the prompt or
the tool set reports.

## Decision

### 1. A tool draws; the runtime stops rewriting

`render_diagram(source, ...)` renders one mermaid source. On success
the terminal draws the art and the tool returns a short status; on
failure it returns the reason. Either way **the model is told what
happened**, in the same turn, and can correct and call again before the
operator ever sees a broken diagram.

The chat rewrite is **removed**. A ```` ```mermaid ```` fence in a chat
reply is displayed as source, unchanged — as the operator put it, *do
not process what the model produced*. This keeps one path instead of
two, and the two paths were not equal: the rewrite was the silent one.
A fence shown as source is visible, and the operator can tell the model
to use the tool, which is the same standard the ER complexity cap was
reverted to in v0.37.4. Files are untouched, as before.

### 2. The art is a side effect, not something the model repeats

The tool result the model receives is a status line, never the art.
Handing back sixty lines of box drawing would double the tokens and
invite the model to reproduce it — badly. The TUI prints the art
through the same raw-output path shell results use (never through the
Markdown renderer), on a dedicated message.

It cannot be delivered as a separate conversation message either:
ADR-0012 §5 records the measurement that the content following a
function-call turn must consist of exactly its responses, or the next
round is a 400.

### 3. The render budget lives in `agent_info`, not the prompt

The model should shape a diagram to the space available *before*
composing it, not discover the constraint by being refused. The
budget therefore joins `agent_info`, whose stated admission rule
(ADR-0030) is that a field earns its place by changing what the model
should do — this one does. The snapshot is already a call-time
closure and the TUI already tracks width on every resize, so this is
the ADR-0039 live-variable pattern with no new machinery.

It is **the budget, not the console's dimensions**. Reporting a raw
`150x50` would mislead in both directions: the inline TUI scrolls, so
the terminal's height is not the bound — a fixed 80-line cap is — and
the usable width is the terminal minus the Markdown renderer's margin.
A model told "50 rows" would shrink a diagram that had 80 lines
available; one on a 200-row terminal would write art that is refused.

Putting it in the prompt instead was rejected: the prompt is
byte-stable so that ADR-0018's cache prefix survives (measured 81–95%
hits), and a number that changes on every resize would either break
that or go stale within the session.

### 4. The guidance fails soft

The prompt asks the model to check the budget before composing and to
call the tool rather than write a fence. Neither instruction is load
bearing: a model that skips `agent_info` still gets the exact budget in
the tool's refusal, and corrects from there. This matters because
instructions demonstrably do not always fire — the memory trigger of
ADR-0020 §5 was measured firing zero times in 39 sessions before it was
written as a trigger. A check that is only an optimisation of the first
attempt cannot break the feature by not firing.

## Consequences

- One diagram costs one extra round (two if the model checks the
  budget first). Accepted: a diagram that draws correctly the first
  time is cheaper than one shown as source and re-explained in prose.
- The model can iterate privately — refused, corrected, drawn — so the
  operator sees the finished diagram rather than the attempts.
- `render_diagram` is registered only under the TUI, like ADR-0042's
  prompt section: the plain REPL and one-shot mode never advertise a
  capability their surface lacks.
- The tool is read-only and never approval-gated; it writes nothing and
  reaches no network.
- ADR-0042's three rules (translate / fit / verify) are unchanged and
  keep doing the work — they are now the tool's engine, and their
  refusal reasons become the model's feedback rather than a silent
  fallback.

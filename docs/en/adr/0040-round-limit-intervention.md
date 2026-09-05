# ADR-0040: the round limit becomes an intervention ladder, not a guillotine

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, from a session log: a healthy 50-round research turn was killed mid-pipeline by max_turns, and the error's advice (/clear) would have destroyed the recoverable work; a bare counter spoils capable agentic models — combine loop detection and a third-party progress review |

*Amended by ADR-0075: the loop guard gains a sibling detector — the
same error text returned by one MCP tool for different arguments, which
the identical-call signature cannot see. It speaks in the function
response and never stops the turn; the round ladder stays the ceiling.*

## Context

`[agent].max_turns` was a guillotine: round 50 arrived and the turn
died, whatever it was doing. The session log that triggered this ADR
is the perfect specimen: an incident-research skill run — 15 searches,
12 fetches, then report sections written and schema-validated one by
one — was killed at round 50 **while monotonically progressing**, six
sections into the report, with zero repeated calls. The operator typed
"continue"-equivalent and it finished in 10 more rounds; the only
damage was the round-trip and the error's advice, which recommended
`/clear` — the one action that would have destroyed the recoverable
state — and never mentioned that continuing was possible at all.

Meanwhile a genuine runaway loop pays the opposite cost: it wastes
every round up to the limit before anything intervenes.

The codebase already owns the answer's shape: the auto-approve ladder
(ADR-0004) — a deterministic rule tier, a model tier for judgment, a
human floor, and a Block ceiling nothing below it can lift.

## Decision

### 1. Rule tier: a loop detector, always on, immediate

The turn tracks consecutive identical calls (tool name + canonical
argument JSON). Three in a row triggers the intervention below **now**
— a real loop no longer gets 40 free rounds. Because legitimate
repetition exists (polling an async MCP job is identical calls by
design), detection never kills by itself: it escalates, and a
"continue" verdict **whitelists that call signature for the rest of
the turn**, so polling asks once, not every three polls.

### 2. At the limit: a progress review, then a decision

Reaching the round limit (and each extension boundary after it) runs
one side-call: the reviewer sees the operator's instruction and the
turn's recent tool-call trace — evidence-wrapped exactly like the risk
evaluation (ADR-0038), no tools — and answers
`{progressing, confidence, reason}`. Monotonic movement through
distinct work is progress; identical calls with no state change is
stuck; polling is named as possibly-legitimate waiting.

The decision then follows the mode:

| Mode | Decision |
|---|---|
| Interactive, auto-approve ON | progressing + confident → **auto-continue** with a visible notice (the operator turned auto on precisely to reduce interruptions); anything less → the dialog |
| Interactive, auto OFF | always the dialog, verdict shown as evidence |
| Non-interactive (`-p`) | progressing + confident → continue; anything less → stop (fail-closed) |

The dialog rides the ask machinery (ADR-0036): two options, digits or
Esc, auto-declined after Ctrl+C. An extension grants half of
`max_turns` more rounds.

### 3. The ceiling no verdict can lift

The absolute cap is **3 × max_turns**. At the cap the turn stops
regardless of verdicts or answers — a fooled reviewer bounds the
damage at a known spend, the Block-floor principle applied to rounds.
(The `agentic_file_search` child keeps its plain hard limit of 10:
ADR-0037 decided a child that runs dry needs a narrower question, not
a bigger budget — the extension machinery is main-loop only.)

### 4. The stop message tells the truth

When the turn does stop, the message now says what the log proved:
progress so far is saved in the conversation, saying "continue"
resumes where it left off, and `[agent].max_turns` raises the limit
permanently. The `/clear` recommendation — destroy the one thing that
makes resumption work — is gone.

## Consequences

- A capable model doing long legitimate work is no longer spoiled: in
  auto mode it sails through the checkpoint with one visible notice;
  the worst case is one keypress per `max_turns/2` rounds.
- A genuine runaway is caught in ~3 rounds instead of 50, and can
  never spend more than 3 × max_turns.
- Cost: one side-call per checkpoint — at most a handful per long
  turn, nothing on turns that never reach the limit.
- The intervention is recorded in the transcript
  (`round_intervention`: trigger, rounds, verdict, decision), and the
  extension is visible in the audit stream as `turn.end` rounds above
  `max_turns`.
- Review-side accounting joins the risk-review counters in `/usage` —
  both are model-tier reviews of the agent's own behaviour.

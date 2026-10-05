# ADR-0097: The date the runtime states is captured once per session, and a resumed session says when its conversation began

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-10-06) — implemented |
| Date | 2026-10-06 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator behind ADR-0096 resumed a production session from 10/2 on 10/5 and asked for new work: in 2 of 4 runs the model named the case folder with the original session's date (ADR-0096 §6.2) |
| Amends | ADR-0032 §2 (the session-start date in the system prompt) |
| Relates to | ADR-0084 (the prompt says what is true), ADR-0005 (resume), ADR-0071 §2 (`/clear` starts a new session), ADR-0018 (a stable prefix) |

## Context

The model's only statement of the date is one line of the system prompt
(ADR-0032 §2): `Session started: 2026-10-05 (Monday, JST)`, pointing it at
the `datetime` tool for the live moment. Read against the code, the line
has two faults:

- **On resume it says something untrue.** The line is computed when the
  process starts, so a session resumed on 10/5 says it *started* on 10/5,
  while the conversation the model is reading began on 10/2 and is full of
  10/2 — folder names, timestamps in tool results. The runtime's own words
  contradict the record in front of the model, and the model resolved the
  contradiction toward the conversation in 2 of the operator's 4 runs.
- **It is not captured once.** `composeSystem` rebuilds the prompt on a
  skills reload, an MCP inventory change and a `/clear`, and each rebuild
  calls `time.Now()` again; a reload after midnight moves "session started"
  to the next day.

Whether *where* the date is stated changes what the model does — a note at
the resume point, near the work, instead of the system prompt — is a
separate claim about behaviour, the kind ADR-0096 measured before
deciding. lagent already states its facts in a message at the resume
point, so the comparison is cheap to set up; it is measured separately and
is not decided here.

## Decision

1. **The date is captured once per session.** It is taken when the
   session begins — at process start for a fresh or resumed session, and
   again when `/clear` starts a new one — and every rebuild of the system
   prompt uses it. A rebuild never moves it.
2. **A resumed session states both facts, truthfully.** The line becomes
   `Resumed: 2026-10-05 (Monday, JST); the conversation above began on
   2026-10-02.` The resume date is called the resume date, not "today":
   a session that runs past midnight would otherwise state a false
   "today", and the `datetime` pointer that follows covers the live
   moment. The original date is the transcript's first record (the
   session list's "started"). A fresh session keeps `Session started:`,
   and `/clear` returns to it.
3. **Nothing else moves.** The line stays in the system prompt; the
   pointer to `datetime` is unchanged.

The system prompt was already rebuilt by every resume, so the prefix
cache (ADR-0018) loses nothing it had.

## Consequences

- On resume the model reads one statement that agrees with the
  conversation: it began on 10/2 and was resumed on 10/5.
- The date in the prompt no longer moves on a reload.
- Whether the operator's observation changes is not claimed here; that is
  the measurement of where the date is stated.
- lagent states the same fact in its session-facts message and has the
  same fault on resume (a second "session started" line with today's
  date beside the original one); it ports this as its own record.

## Alternatives considered

- **Say "Today:" on resume.** Rejected: true only until midnight.
- **Restate the date every turn.** Rejected for ADR-0018's reason: the
  system prompt would change on every request.
- **Also move the date to a note at the resume point now.** Not decided:
  a claim about behaviour, measured first.

## References

- `cmd/prompt.go` — `buildSystemPrompt`, the date line
- `cmd/root.go` — `composeSystem`, resume, `/clear`
- `internal/session` — `Meta.Started`

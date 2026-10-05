# ADR-0097: The date the runtime states is captured once per session, and a resumed session says when its conversation began

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-10-06) — implemented; revised the same day after measuring where the fact is stated (§4) |
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
deciding. It was measured the same day (§4).

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
3. **The line stays in the system prompt**, with the pointer to
   `datetime` unchanged.
4. **The same fact is also stated at the resume point.** On resume the
   runtime appends its own message after the restored history:
   `gem-agent: this session was resumed on 2026-10-06 (Tuesday, JST); the
   conversation above began on 2026-09-04.` It is recorded in the
   transcript like any message, so a later resume reads an earlier note
   as the history it is, and adds its own. Its prefix is the session
   package's `ResumeNotePrefix`, and the session list never shows it as a
   session's opening line.

   *Measured (2026-10-06).* A real 2026-09-04 investigation session
   (128 messages, about 128k tokens, its conversation dated 2026-09-04
   sixteen times) was copied into an isolated home and resumed on
   2026-10-06 with the request to save three follow-up points "in a memo
   with a dated filename" — no date named, as in the operator's case.
   Five runs per build, `gemini-3.8-flash`, `thinking = "high"`:

   | Build | Memo dated 2026-10-06 | Called `datetime` |
   |---|---|---|
   | v0.91.0 ("Session started: 2026-10-06") | 0/5 — all 2026-09-04 | 0/5 |
   | §1–§3 only (the truthful line) | 0/5 — all 2026-09-04 | 0/5 |
   | §1–§4 (plus the note at the resume point) | **5/5** | 4/5 |

   Correcting the system prompt's line did not move the model at all; the
   same fact at the resume point moved every run (Fisher p = 0.008 for
   the last two rows). Where two sources of the date disagree, the model
   took the one next to the work. The runs cost $2.61 in all.

The system prompt was already rebuilt by every resume, so the prefix
cache (ADR-0018) loses nothing it had.

## Consequences

- On resume the model reads one statement that agrees with the
  conversation: it began on 10/2 and was resumed on 10/5.
- The date in the prompt no longer moves on a reload.
- Resumed runs that named a date took today's in 5 of 5 (§4); with the
  line alone they took the conversation's in 5 of 5.
- A resumed session's history carries one runtime note per resume —
  also a resume the operator leaves without sending anything. Accepted:
  each note stays true as history.
- When `/clear` cannot open a new transcript and clears the history in
  place, the dates start over too: "the conversation above began on"
  would name a conversation that is gone.
- "Began on" is the transcript header's time. A transcript whose header
  line is unreadable falls back to its last-modified time, as the session
  list already does; rare, and accepted.
- lagent states the same fact in its session-facts message and has the
  same fault on resume (a second "session started" line with today's
  date beside the original one); it ports this as its own record.

## Alternatives considered

- **Say "Today:" on resume.** Rejected: true only until midnight.
- **Restate the date every turn.** Rejected for ADR-0018's reason: the
  system prompt would change on every request.
- **Correct the system prompt's line only.** Measured: 0 of 5 (§4). It is
  kept because it is true, not because it is enough.

## References

- `cmd/prompt.go` — `buildSystemPrompt`, the date line
- `cmd/root.go` — `composeSystem`, resume, `/clear`
- `internal/session` — `Meta.Started`

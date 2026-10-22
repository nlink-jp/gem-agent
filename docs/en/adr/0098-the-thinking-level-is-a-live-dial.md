# ADR-0098: The thinking level is a live dial for the session

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-10-23) |
| Date | 2026-10-23 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: wants to switch the thinking level during a session |
| Amends | ADR-0025 §3 (the level is read-only in /settings; live editing deferred) |
| Relates to | ADR-0082 (the model tier's slot), ADR-0083 (the librarian's slot), ADR-0009 (the settings panel), ADR-0005 (resume) |

## Context

ADR-0025 made `[model].thinking` a startup setting and deferred live
editing: it "would add the first mutable field to the shared Vertex client
for a setting that changes rarely". `--thinking` (2026-08-21) made it a
per-run choice, but the operator still has to end the session to change
it — and the reason to change it arises inside a session: a quick lookup
after a long refactor, or a hard question in the middle of routine work.
Restarting costs the live state (the scrollback, the session's approvals,
`/auto`, the read-only ceiling); `--resume` restores the conversation and
nothing else.

The level is a per-request field (`GenerateContentConfig.ThinkingConfig`),
not a property of the client or of the conversation, so nothing has to be
rebuilt. The one premise that could break it is the thought-signature
replay (ADR-0009): earlier rounds produced at one level are sent back with
their signatures under another. **Measured before deciding**
(`internal/llm/levelswitch_live_test.go`, the operator's configured model):
a tool call made at `high`, replayed with its signature at `low` —
accepted; that conversation replayed at `high` and at the model's default
— accepted. `minimal` was rejected with a 400 naming the level, as
ADR-0025 §4 recorded for another model: support is per model, not per
switch.

## Decision

1. **`/thinking [minimal|low|medium|high|default]`.** Bare, it shows the
   level in force and where it came from. With a level it sets it;
   `default` clears it to the model's own default — the word `--thinking`
   already uses for the same thing. Anything else, including trailing
   words, is a usage error and changes nothing (the `/auto` and
   `/readonly` grammar). Both surfaces (TUI and the plain REPL) take it.
2. **The `model.thinking` row in /settings becomes editable**, cycling
   `default, minimal, low, medium, high`. The row and the command change
   the same value; the row's source reads `session` while the value
   differs from the one the session started with.
3. **Session-only.** Nothing is written to any file. A new process starts
   from config and flags again, and `--resume` restores the conversation
   at the configured level — the footing `/auto` stands on. `/clear` keeps
   the level: it starts a new conversation, not a new process.
4. **It takes effect at the next model request.** The level is read once
   when a request is built, and that one reading is what the request and
   its 400 hint use. The operator cannot change it mid-turn — the TUI
   refuses slash commands while a turn runs and /settings opens only at
   the prompt — but a tool still running after Ctrl+C can be mid-request,
   and that request keeps the level it was sent at.
5. **Its reach is every call that holds the main backend itself**, the
   paths that already run at `[model].thinking` today:
   - the conversation, compaction, and the `agentic_file_search` child;
   - the model tier (risk evaluation, progress review, the read-only
     watcher) and the librarian while neither has a slot of its own —
     what the "(follows …)" labels in /settings already say;
   - with `[model].summary` unset, `summarize_file` and
     `/riskbook learn`, which then ride the main backend.

   It does not reach a named slot (`[model].risk`, `risk_thinking`,
   `librarian`, `librarian_thinking`) or a named summary model, which keep
   their own level (ADR-0025 §2, ADR-0082 §3): `WithModel` and
   `WithThinking` give each copy a dial of its own, never a shared one.
   Grounded search and URL fetch send no thinking configuration and are
   unaffected. (ADR-0025 §2's "the summary model keeps its own default"
   holds only for a named summary model; that was already so.)
6. **One value, held by the main backend, safe to read and write from
   any goroutine.** The panel, `/thinking`, `agent_info`, and every
   request read it there, so there is no second copy to drift; side calls
   run concurrently with the UI, so the value is atomic. `cfg` keeps the
   startup value, which is what the source column compares against.
7. **The 400 hint names `/thinking` once the level was set there.** An
   unsupported level is still the API's call (ADR-0025 §4): the next
   request fails loudly and names the level and the way back, and the
   operator's next `/thinking` fixes it without a restart.

## Not taken

- **A transcript record of each change.** `mode_change` records exist
  for the approval mode and the read-only ceiling — what the gate did and
  why. The thinking level is a cost dial, and its effect is already
  recorded where cost is: each usage record carries the round's thought
  tokens.
- **Writing the choice back to config.** A dial turned for one stretch of
  work would silently become every later session's default; the file
  stays the operator's hand-written statement of the default.
- **Validating a level against the model before accepting it.** There is
  no metadata field for supported levels, and a probe request would cost
  money on every switch to answer a question the next real request
  answers for free.
- **A live model switch (`/model`).** Different scope: the context
  window, the usage pricing and the replayed signatures all depend on the
  model; nothing here does.
- **Showing the level in the footer.** `/thinking` and /settings answer
  the question when asked; a cost dial does not need to be on screen at
  all times the way the approval mode does.

## Consequences

- The first mutable field on the Vertex backend (ADR-0025 §3's concern):
  one atomic value, copied per slot rather than shared.
- An operator can lower the level for cheap work and raise it for hard
  work without losing the session.

## References

- ADR-0025 (the level and its model-dependent support)
- ADR-0082, ADR-0083 (the slots that keep their own level)
- ADR-0009 (editable rows act on the live session)

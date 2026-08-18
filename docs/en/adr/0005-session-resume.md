# ADR-0005: Session resume from the transcript log

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator request: implement resume |

## Context

The RFP put session resume out of scope for v1, on the reasoning that a
backup tool should stay minimal. Use has argued the other way. gem-agent
is reached for exactly when something else has failed, and the work it is
reached for is long: a session that ends — a crash, a `Ctrl+D`, a laptop
lid, a filter block that killed the turn — currently ends the context
with it, and the operator retypes the situation from memory. The same
argument that justified reading `AGENTS.md` (do not make the fallback
cost a re-setup) applies here.

Something close to a transcript already exists. `~/.local/state/gem-agent/
sessions/<ts>.jsonl` records every user message, assistant turn and tool
result. It cannot be replayed, though, for two reasons: tool results are
clipped to 2000 characters for readability, and Gemini 3 **thought
signatures are not recorded at all** — and those are a hard API
requirement on replay, not an optimisation (a function-call part sent
without its signature fails the request with 400).

## Decision

**The session log becomes the resume source of truth** — one file per
session lineage, not a log plus a parallel transcript. Two files holding
the same conversation would drift, and the one that drifts is always the
one nobody reads.

1. **Full fidelity for the history-bearing records.** The three kinds
   that reconstitute a conversation (`message` records for user,
   assistant and tool roles) are written as the complete `llm.Message`,
   including tool-call arguments, attachments, and thought signatures
   (base64). No clipping. Diagnostic kinds — `auto_decision`, `notice`,
   `assistant_empty` — keep their summarised form and are ignored on
   load.
2. **A header record** (`session`, first line) carries the schema
   version, the gem-agent version, the model, and the project directory.
   Listing reads only headers.
3. **Resume appends to the same file** and records a `resumed` marker.
   One file is one conversation, however many processes it took.
4. **`compaction` records are replayed as compactions** (ADR-0006): the
   loader applies them exactly as the running agent did, so resuming a
   compacted session restores the compacted history rather than
   re-inflating a conversation that was deliberately shrunk.
5. **Binding rules, both refusals rather than warnings:**
   - **Project.** A session resumes only into the directory it was
     recorded in. Its history is full of that project's file contents
     and paths; replaying it elsewhere is confusing at best and leaks
     one project's contents into another's context at worst.
   - **Model.** A session resumes only under the model that produced it.
     Thought signatures are opaque model-bound continuation tokens; there
     is no basis for assuming one model's signatures are valid input to
     another, and the failure mode (400 on the first request after
     resume) would land after the operator thought they were back at
     work. The error names the recorded model, so the way forward is a
     copy-paste.

**Verified before shipping**, because the whole design rests on it:
signatures recorded by one process replay successfully in another. A
one-shot run that read a file was resumed in a second process, which
answered from the restored tool result without re-reading anything
(gemini-3.7-flash, 2026-08-19). Had that failed, verbatim replay would
have had to be abandoned for a summary-based resume.
6. **Ids are validated, not interpreted.** `--resume <id>` accepts the
   session's base name only (`20260819-150102`, optionally `-2`), and
   resolves it inside the sessions directory. A path is never accepted,
   so no traversal and no reading of an arbitrary attacker-supplied
   transcript.
7. **Surface**: `--continue` / `-c` (the most recent session for this
   project), `--resume <id>`, and `gem-agent sessions` to list.

Restored tool output is re-wrapped in the new turn's nonce tag by the
existing send-time wrapping (`wrapToolMessages`) — the isolation
guarantee of ADR-0001 survives a resume automatically, because the
transcript stores raw content and the tag is regenerated per call.

## Consequences

- Session files grow: full tool output instead of a 2000-character
  excerpt. The tools layer already caps individual outputs, the files
  are `0600`, and the state directory is the operator's own. Worth
  saying plainly: **the transcript now holds every file the agent read**.
  It always half-did; now it does so completely.
- Sessions are per-project in practice, which is what makes `--continue`
  unambiguous with no picker.
- The model binding will chafe when someone wants to carry a
  conversation to a different model. That is the honest position until
  cross-model signature replay is measured; ADR-0006's compaction
  produces a signature-free summary, so a cross-model resume built on it
  is the obvious future refinement if the need appears.
- A transcript from a much older gem-agent may not load. The header's
  schema version makes that a clear message rather than a mystery.

## Alternatives considered

- **A separate transcript file next to the log** — rejected: two records
  of one conversation, and the resume path would be the untested one.
- **Resuming from the clipped log as-is** — rejected: silently lossy.
  A resumed session that has forgotten the second half of a file it read
  is worse than no resume, because nothing announces the gap.
- **Stripping thought signatures on load** (which would make resume
  model-independent) — rejected: Gemini rejects a function-call part that
  arrives without its signature, so this trades a refusal we can explain
  for a 400 we cannot.
- **Automatic resume of the last session on startup** — rejected: the
  operator must be able to start clean without an argument. Resume is
  opt-in, like every other context-carrying feature here.

## References

- ADR-0001 (nonce isolation of tool output; unchanged and reused here)
- ADR-0006 (compaction; its records are replayed by the loader)
- shell-agent-v2 ADR-0009 (thought-signature capture/replay)

# Sessions, compaction, usage, and memory

Everything that persists across a conversation or between them: the
transcript, resume, context compaction, the token statement, and agent
memory.

## Transcripts and resume (ADR-0005)

```sh
gem-agent sessions        # ids, age, size, model, and the opening question
gem-agent sessions --all  # every project, not just this one
gem-agent -c              # the most recent session in this directory
gem-agent --resume 20260819-150102
```

A resumed session continues its own transcript — one file is one
conversation however many processes it took — and comes back exactly as
it was, tool results included. Two refusals are deliberate: a session
resumes only in the directory it was recorded in, and only under the
model that produced it (the replayed reasoning tokens are model-bound).
Each message names what to do instead. A session that was compacted
resumes compacted, rather than re-inflating to the size it was shrunk
from.

The JSONL transcript is both the log and the resume source, recorded in
full fidelity (Gemini reasoning tokens included, which the API requires
on replay). It therefore holds the full text of every file the agent
read.

## Where state lives (ADR-0022)

Transcripts live under
`~/.local/state/gem-agent/sessions/projects/<escaped path>/`, mode
`0600` — one subdirectory per project, the same convention as memory,
so a listing reads one directory and a cleanup in one project's
directory cannot touch another's. A `.project` marker records the
owner, so two projects whose paths escape to the same name refuse
loudly instead of mixing. Transcripts recorded by older versions in
the flat `sessions/` directory keep working in place: listed and
resumed where they are, never moved.

Parallel launches are safe: session ids are timestamps with an atomic
suffix on collision, and each transcript is held under an exclusive
lock — a second `--resume` of the same session says "in use" instead
of interleaving writes.

The `GEMAGENT_STATE_DIR` environment variable relocates the whole
state root (sessions and memory) — its purpose is isolation for tests
and drills.

## Context compaction (ADR-0006)

At `[agent].compact_at_pct` of the model's window (80% by default), the
older part of the conversation is replaced by a summary of it and the
recent part is kept verbatim; `/compact` does the same on demand. The
notice says how many messages were summarised, because detail from that
half is second-hand afterwards and a model that has forgotten something
must not look like one that never knew it.

If the summarisation call fails — an error, a content filter — the
history is left exactly as it was and the turn continues on a full
context. `auto_compact = false` turns the automatic path off;
`/compact` still works.

## The token statement: `/usage` (ADR-0019)

Main-loop rounds with the cache hit rate, risk-check and compaction
side-calls, and per-tool lines (summaries, web, the file-search agent)
naming the model that spent the tokens. The footer carries the live numbers (context
occupancy, cumulative consumption, `cache NN%`); the model can read
the same figures through `agent_info`.

## Agent memory (ADR-0020)

The agent persists short facts across sessions: decisions, preferences,
environment quirks — things worth knowing next time that no project
file states. The prompt also says **when**: as a piece of work
finishes, the agent asks whether it learned something that would have
saved work had it known at the start, and proposes a memory then
without being asked (ADR-0020 §5). Before that trigger existed the
model proposed a memory zero times in 39 sessions — the wording granted
a capability and spent its concrete sentences on prohibitions.

| Scope | Recalled in | Stored at |
|---|---|---|
| `global` | every project | `~/.local/state/gem-agent/memory/global/<name>.md` |
| `project` | that project only | `~/.local/state/gem-agent/memory/projects/<escaped path>/<name>.md` |

- One memory = one small markdown file; saving an existing name updates
  it. Everything is loaded into the system prompt at session start
  (global first, then project) under a fixed budget, with any clipping
  reported rather than silent.
- **Writes are approval-gated** (`save_memory` / `delete_memory`): a
  persisted memory reappears in every later session's prompt, so
  memory is a persistence vector for injected instructions — the human
  reviews each write. Auto-approve cannot stand in for that review:
  memory writes are excluded from the ladder and always escalate, since
  the model evaluating the write is the one that proposed it
  (ADR-0020 §6). The [per-tool policy](approval.md) is the one way to
  relax it, deliberately and per tool.
- The injected section is framed as background knowledge the agent
  recorded — explicitly below the standing of your own instruction
  files, and possibly stale.
- Nothing is ever written into the repository, and `~/.claude` is
  never read. The files are plain markdown: audit, edit, or delete
  them by hand whenever you like.
- `/memory` lists what is stored right now, where the files live, and
  the two ways to remove one — ask the agent to forget it, or delete
  the file. It takes no arguments. A new save takes effect from the
  next session.

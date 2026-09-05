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

Beside a project's transcripts sits `persistent.json` (ADR-0074 §3/§4):
the digests of the persistent files under the project — the write
lane's protected names at any depth, nested repositories' hooks
included — as the last session left them. The next start compares and
notes what changed since ("changed since your previous session: …"),
advisory only; it is rewritten at startup, `/clear` and exit.

## Context compaction (ADR-0006)

At `[agent].compact_at_pct` of the model's window (80% by default), the
older part of the conversation is replaced by a summary of it and the
recent part is kept verbatim; `/compact` does the same on demand. The
notice says how many messages were summarised, because detail from that
half is second-hand afterwards and a model that has forgotten something
must not look like one that never knew it.

The stand-in message also warns the model that file contents read
before the compaction are no longer verbatim in context: re-read
before editing or quoting exactly, never rewrite a file from the
summary alone (ADR-0051). The warning is gem-agent's own framing —
deterministic, not dependent on the summariser — and it lands at
exactly the moment a mid-task compaction would otherwise turn a later
whole-file write into a write-from-summary.

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

One reading note: the cache percentage is a **cost and latency**
saving — cached prompt tokens are billed less and stream faster. It
does not free context-window space; the window is what compaction
manages.

## Accounting records in the transcript (ADR-0057)

The API reports tokens, never money, so a session's cost is token
counts × catalog price — and the counts have to be written down as they
happen. `/usage` lives in memory and leaves with the process.

Every model call therefore writes one `usage` record:

    {"kind":"usage","data":{"source":"risk","model":"gemini-…",
     "prompt":4183,"output":42,"thoughts":81,"cached":0,"tool_prompt":0,
     "total":4306}}

`source` is one of `main`, `risk`, `progress_review`, `compact`,
`summarize_file`, `web_search`, `web_fetch`, `agentic_file_search`,
`riskbook_learn` — sum by source, price by `model`, check against
`total`. The session
header records the region alongside the model, because prices are
resolved per SKU per region.

Three facts the arithmetic depends on: thinking tokens are a
**separate bucket** from output (and bill as output), `cached` is a
discounted **share of** `prompt`, not an addition to it, and
`tool_prompt` is the results of the provider's built-in tools (search
grounding, URL context) fed back to the model as input — billed as
input, never cached, and non-zero only on `web_search` and `web_fetch`
(ADR-0066). `total` is the API's own count, so
`prompt + output + thoughts + tool_prompt == total` catches a sum that
forgot any of them, instead of undercounting quietly.

Transcripts written before this keep their older `usage` records — no
`source`, main loop only — and their risk-evaluation and compaction
spend was never written at all. An aggregator should count them and
report those files as partial. A record without the `tool_prompt` key
predates ADR-0066: derive the bucket as the non-negative remainder of
the checksum, rather than treating the record as broken.

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

## Work directories: `gem-agent workdirs` (ADR-0058, ADR-0059)

Every session gets a work directory under the state root (exported
as `GEMAGENT_WORK_DIR`); oversized MCP results and scratch output land
there. At startup a note counts the earlier sessions' directories
(`N earlier session work directories hold 12 MB here`); nothing is
deleted automatically. `gem-agent workdirs` lists them with age, file
count and size, and `gem-agent workdirs clean [id…]` deletes after a
typed confirmation (`--yes` without a terminal), never a running
session's. The scans are bounded: the listing stops at 10,000
sessions and says so, a directory's count stops at 50,000 files and
shows as `N+`, and the startup note shows `N+` / `12 MB+` when its
numbers are lower bounds. `workdirs clean <id>` finds a session the
cut listing did not reach.

## Session ids and `/clear` (ADR-0071)

A session id is a UUID v4, unique on the machine; the listing shows its
first eight characters beside the start time, and `--resume` accepts a
full id or any unambiguous prefix. Transcripts recorded with the older
timestamp ids still list and resume. `/clear` starts a new session: the
old transcript is closed where the conversation ended and stays
resumable by its id; a new id, transcript and work directory take over
and are exported to children; the session hooks see a `session_end`
(`clear`) then a `session_start` (`clear`). Everything that reads the
work directory follows the new one — the sandbox profile, the file
tools' second root, the MCP intake and the system prompt — and the
agent's own per-session state (queued attachments, the dead-transcript
mark) is dropped with the old session (ADR-0072 §2). The MCP servers
are reconnected (the same report `/mcp reload` prints), so a server
that keeps per-session state sees the new id, and telemetry is
re-resourced with it: the old session's `session.end` and the new
one's `session.start` carry their own ids (ADR-0071 addendum).

Before the old transcript closes — at `/clear` and at exit — the
persistent files under the project are compared with the set the
session started from; a difference is noted and written to the
transcript as a `persistent_changes` record (`reason` of `clear` or
`exit`, and the `added`, `changed` and `removed` names) — the nested
file a directory swap can plant, made visible (ADR-0074 §3). The new
session also re-checks the content pins (ADR-0074): an instruction
file, `.mcp.json` or project skill that changed during the old session
is left out and named.

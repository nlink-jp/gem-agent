# ADR-0071: the session identity contract — what gem-agent tells the world about a session, aligned with Claude Code

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a per-session consumer (agent-board) found that gem-agent's session id is unique only within a project, and that the identity elements the two runtimes expose differ; "settle the runtime's specification first, and make the adopted elements match" |
| Amends | ADR-0005 (session id format), ADR-0022 (per-project state: the id changes, the layout does not), ADR-0044 / ADR-0069 (hook events), ADR-0058 / ADR-0069 addendum 2 (exports to children) |

## Context

gem-agent names a session by its start time, `YYYYMMDD-HHMMSS`, with a
numeric suffix on a collision inside the same project directory
(ADR-0005). Sessions are filed per project (ADR-0022), so the same id
can exist in two projects at once, and nothing outside gem-agent can
tell them apart. That was harmless while the id only served resume.
It stopped being harmless when the id was exported to children
(`GEMAGENT_SESSION_ID`, ADR-0069 addendum 2): a consumer keyed on it
merged two sessions started in the same second in different projects.

The consumer that surfaced this — agent-board, a machine-local board
shared by gem-agent and Claude Code sessions — has to treat both
runtimes identically. Claude Code's identity elements are fixed and
measured (2.1.226): a UUID v4 session id unique on the machine, a new
id on `/clear`, the project as a directory (`cwd`, and
`CLAUDE_PROJECT_DIR` for children), `CLAUDE_CODE_SESSION_ID` for
children, and four hook events including `SessionEnd`. Where gem-agent
exposes a different element, every consumer must special-case it, and
the operator's direction is to remove the difference at the source
rather than compensate for it downstream.

Two constraints from the same discussion bound the design: nothing is
written into the operator's project on gem-agent's behalf, and nothing
depends on the project being under git.

## Decision

### 1. The session id is a UUID v4

`session.Open` generates a UUID v4; `ValidID` accepts both the new form
and the timestamp form, so every existing transcript still lists and
resumes. `--resume` accepts an unambiguous prefix (the listing shows
the first eight characters beside the start time), so the operator
types as little as before. The start time stays where it always was —
the transcript's `session` header and file mtime — and the listing
sorts by it; the id stops carrying it. The id is unique on the machine
and, for practical purposes, everywhere.

### 2. `/clear` starts a new session

A cleared conversation is a new session: a new id, a new transcript,
the old one closed and resumable. Today `/clear` empties the history
inside the same transcript (recorded as a `clear` event, ADR-0021).
Claude Code starts a new session on `/clear`; a per-session consumer
that saw the same id continue with an empty history could not tell
"cleared" from "compacted". The work directory (ADR-0058) follows the
session: a new one is created, the old one swept like any other.

### 3. The project is a directory, exported as `GEMAGENT_PROJECT_DIR`

The project is the directory gem-agent was started in (its root, as
`projectDir` already is). It is exported to children beside
`GEMAGENT_SESSION_ID` and `GEMAGENT_WORK_DIR`, so a child sees the same
three facts a Claude Code child sees (`CLAUDE_PROJECT_DIR`,
`CLAUDE_CODE_SESSION_ID`, and its own cwd). No project identifier is
minted, stored in the project, or derived from git: a consumer that
needs a stable key derives it from the directory in its own state.

### 4. `SessionEnd` joins the hook events

`[[hooks.session_end]]` runs when the session ends — a normal exit,
`/quit`, EOF, a signal, or the end of a one-shot run — with Claude
Code's payload (`session_id`, `transcript_path`, `cwd`, `reason`:
`clear` | `exit` | `other`) and a short timeout, non-blocking (a session
end cannot be refused; measured on Claude Code, ADR-0069 context). `/clear`
fires `SessionEnd` for the old session, then `SessionStart` (`source`
`clear`) for the new one — the same sequence Claude Code produces.

*Amended by ADR-0072 §2.5: this section originally wrote `startup` for
the source; the config template, the reference document and Claude
Code's matcher vocabulary say `clear`, and v0.66.0 shipped with the
code following this section — a `matcher = "clear"` hook never fired
on `/clear`. The code now follows the documents.*

### 4a. Addendum (2026-09-05): `/clear` restarts what carries the identity

Measured after v0.67.0: the MCP server spawned at startup with
`${GEMAGENT_SESSION_ID}` in its arguments kept `--session <old id>`
across `/clear`, so its claims were attributed to the old session
while the hooks reported the new one; telemetry likewise kept the
first id (stated then as a known limit). Operator decision: a cleared
session is a new session, so everything that carries its identity
restarts — the MCP servers are reconnected as `/mcp reload` does (new
environment, new argument expansion), and the telemetry sink is
re-resourced in place (`Sink.Restart`; holders of the pointer,
`Sub` sinks included, follow). Order on `/clear`: `session_end` hook →
`session.end` audit event → transcript, work directory and roots →
`Sink.Restart` → MCP reconnect → `session.start` audit event →
`session_start` hook. The consequence "telemetry keeps the first id"
below no longer holds.

### 5. What does not change

The per-project state layout (ADR-0022: escaped path directories,
`.project` marker, zero file motion), the transcript format (ADR-0005),
the hook payload shapes (ADR-0044 / ADR-0069 and its addenda), and
`GEMAGENT_WORK_DIR` (ADR-0058).

## Consequences

- A session is identified the same way whichever runtime started it;
  a per-session consumer registers one line per runtime and no
  special cases.
- Operators lose the readable timestamp in the id and gain it back in
  the listing; resume typing stays short via prefixes.
- `/clear` becomes slightly heavier (a new transcript and work
  directory) and slightly more honest (the old conversation remains
  resumable by its own id).
- The cli-series stability contract applies: the id format and the
  `/clear` semantics change for new sessions only; nothing existing
  stops working. Ships as a minor version.

## Alternatives considered

- **Timestamp plus a random suffix** (`20260905-001059-7f3a`) — keeps
  the readable time in the id, but keeps gem-agent a special case
  every consumer must know about. Rejected on the operator's
  direction that the adopted elements match Claude Code's.
- **Keep `/clear` in-session** — rejected: a consumer cannot separate
  a cleared session from a compacted one, and Claude Code's semantics
  are the ones consumers already handle.
- **A project identifier minted by gem-agent** (stored in the
  project, in state, or derived from git) — rejected: the operator's
  project is not a place for gem-agent's bookkeeping, git is not a
  precondition, and a per-runtime identifier would differ from Claude
  Code's, which has none. A directory is what both runtimes have.
- **An organisation-wide ADR** — rejected: this is gem-agent's own
  specification; agent-board's follows from it.

## References

- ADR-0005 (transcripts and resume), ADR-0021 (`/clear` as a recorded event), ADR-0022 (per-project state), ADR-0044 / ADR-0069 (hooks), ADR-0058 (work directory)
- agent-board ADR-0003 / ADR-0007 (the consumer's side; to be revised once this is accepted)

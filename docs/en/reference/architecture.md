# Architecture

Current behaviour of gem-agent, written to be readable cold. Why a given
decision was made lives in the [ADRs](../INDEX.md#adrs); this document
describes what the code does today.

## Shape

One binary, one process, one conversation. `main.go` hands off to
`cmd.Execute`, which builds five things and wires them together:

```
cmd/            flags, config load, project resolution, wiring, REPL/TUI
  ├── internal/config      strict-decode TOML + env/flag precedence
  ├── internal/llm         Backend interface + Vertex AI Gemini (stream observer)
  ├── internal/tools       the eleven file/shell/calendar built-ins + Register
  ├── internal/agent       the turn loop, approval dispatch, compaction
  └── internal/tui         Bubble Tea inline UI (or internal/repl, non-TTY)
```

The tools package holds the eleven built-ins that need only the project
directory; `cmd/` registers the rest through the same `Register` —
model-backed and wiring-dependent tools (`summarize_file`, `web_search`
/`web_fetch`, `ask_user`, `agent_info`, `agentic_file_search`,
`save_memory`/`delete_memory`, `load_skill`) plus every MCP tool.

Supporting packages: `internal/sandbox` (Seatbelt profile generation),
`internal/approve` (plain-REPL gate), `internal/risk` (auto-approve rule
tier), `internal/policy` (per-tool approval policy, plus the
per-command vocabulary of ADR-0045 — parsed for file compatibility but
not applied since ADR-0049), `internal/riskbook` (the layered risk
rulebook and its learning tool — ADR-0050), `internal/mcp`
(stdio JSON-RPC client), `internal/mention` (`@`-references),
`internal/instructions` (`AGENTS.md` discovery), `internal/ignore`
(ignore-aware enumeration: builtin dir list + gitignore matcher —
ADR-0052), `internal/session`
(transcript: logger + resume loader), `internal/statedir` (per-project
state layout), `internal/memory` (agent memory), `internal/skills`
(skill discovery/loading), `internal/docext` (Office text extraction),
`internal/mediastore` (GCS media uploads), `internal/uitext` (ja/en UI
string catalogs), `internal/telemetry` (audit-event export),
`internal/diagram` (terminal mermaid rendering — ADR-0042/0063; a
view-layer rewrite hooked into the TUI's Markdown renderer. A mermaid
fence that draws faithfully becomes box art in place; one that does
not stays source with a one-line reader-facing note. The model is
told nothing about diagrams, and the transcript keeps its source
verbatim).

The agent core knows nothing about the UI. It receives an `Approver`
interface, a set of callbacks (`OnToolCall`, `OnToolDone`, `OnUsage`, `OnNotice`,
`OnAutoDecision`, `OnAttach`, `OnRoundLimit`), and a telemetry sink whose nil value
disables auditing; the TUI implements the callbacks by sending Bubble
Tea messages, and the plain REPL by writing to stderr. That is what lets
the same loop run under a pty, a pipe, and `-p`.

## The project directory

The current working directory at startup is the project, resolved through
`sandbox.ResolveWriteDir` so it is a real path with symlinks already
followed. Everything else derives from it: which files tools may touch,
where the sandbox permits writes, which `.mcp.json` is read, which
instruction files are collected, and which sessions `--continue` will
consider.

Two independent boundaries enforce it, and they fail differently on
purpose:

- **Go-level path confinement** (`internal/tools`). `resolvePath`
  converts to an absolute path under the project, cleans it, checks
  containment, then resolves symlinks and checks containment *again* —
  the second check is what stops a symlink inside the project pointing
  out of it. For a path that does not exist yet, symlinks are resolved on
  the deepest existing ancestor before the remaining tail is re-attached.
  Every path-taking built-in routes through it (the navigation tools' tree walks additionally refuse to follow symlinks — a walk that follows links can leave the project through a link the per-path checks never see).
- **Process-level containment** (`internal/sandbox`). `shell_exec` takes
  no path — it takes a command — so it is wrapped in `sandbox-exec` with
  a generated SBPL profile. Since ADR-0073 the profile is the lane the
  model declares: `read` denies every write outside scratch, the
  network, preference writes, foreign signals and the IPC-capable
  programs, and runs unasked; `write` allows the project and work
  directory but denies the persistent files; `operator` is the ADR-0001
  profile. The rule tier no longer reads shell text beyond a Block
  floor — the kernel decides. `cmd.Dir` is the project. `--no-sandbox`
  removes this layer (and the read lane) and says so in the banner.

## One turn

```
input ──▶ @-references expanded ──▶ history append + transcript record
            │
            ▼
      ┌─────────────────────────────────────────────┐
      │ round: compaction check → request → stream  │
      │   ├── text ──▶ UI (and scrollback at flush) │
      │   └── tool calls                            │
      │         ├── auto-approve ladder (if on)     │
      │         ├── human gate (if not approved)    │
      │         └── execute ──▶ result into history │
      └───────────────┬─────────────────────────────┘
                      │ tool calls present? loop.  text only? done.
```

Per-round details that matter:

- **Thought signatures** are captured from every response Part and
  replayed on the next request, in the order thoughts → text → function
  calls. Gemini 3 rejects a function-call part sent without its
  signature, so this is a hard requirement rather than an optimisation.
- **Function responses are coalesced**: when one assistant turn issued
  several calls, all their responses travel in a single user Content.
- **Untrusted content is wrapped at send time**, not at store time. Tool
  results and `@`-attachments are stored raw and enclosed in the
  **session-scoped** nonce tag (nlk/guard) on each request; the system
  prompt carries the matching `{{DATA_TAG}}`. Session scope keeps the
  request prefix byte-identical so implicit caching fires (ADR-0018) —
  sound because Wrap refuses content containing the tag name. Side-calls
  (risk eval, compaction, summaries) keep fresh per-call tags.
- **A response with neither text nor tool calls is never stored.** An
  empty part in the history makes every later request fail with 400. A
  content-filter block retries once, then reports the reason.
- **Round budget**: `[agent].max_turns` is an intervention checkpoint
  (ADR-0040), not a guillotine — a loop detector escalates three
  identical consecutive calls immediately; the limit runs a progress
  review and asks (or, in auto mode with a confident "progressing",
  continues with a notice); extensions stop at the hard cap of
  3× max_turns. The file-search child keeps its plain hard bound.
- **The stream reports itself** (ADR-0033): the backend feeds an
  observer with chunk liveness, backoff retries, and thought summaries;
  the TUI renders them as the heartbeat, the stall warning, and the
  thought stream. Display-only — none of it enters history or the
  transcript.
- **One tool runs a nested turn loop**: `agentic_file_search`
  (ADR-0037) builds a fresh child agent per call — own history and
  nonce tag, a read-only tool subset, a deny-all approver, no
  transcript — and its report re-enters this loop as an ordinary
  nonce-wrapped tool result. Its audit events carry an `agent` label.

Images (ADR-0012) enter as attachments: `@` routes for the operator
(project paths; absolute and `~` paths for the attachment extensions —
images, documents, audio and video — because `@` is parsed from typed
input alone; `@clipboard` via osascript) and the
project-confined `view_image` tool for the model — a Gemini function
response cannot carry pixels, so the agent follows the tool result with
a user-role message bearing the image part. Bytes are MIME-sniffed,
capped, stored in the transcript, and shown to the compaction summariser
only as placeholders.

## Approval

Mutating tools (`write_file`, `edit_file`, `shell_exec`,
`save_memory`/`delete_memory`, `web_search`/`web_fetch` — the query and
the URL are egress — and every MCP tool) pass the gate. `y`
allows once, `a` allows that tool for the session, `n`/Esc denies; a
denial is returned to the model as a result, never as silence. Answers
are selectable with ←→/Tab + Enter because a Japanese IME swallows
letter keys. The `ask_user` dialog (ADR-0036) shares this grammar but
is not an approval: the tool is read-only and never gated.

The operator can override this per tool (ADR-0008). `[approval.tools]`
in the global config, and `<project>/.gem-agent.toml` for the project,
map a tool name — or a `mcp__server__*` prefix — to `always` (gate in
every mode, ladder skipped) or `never` (no gate in any mode). `never`
does not lift the rule tier's Block floor, so a `shell_exec` command
matching a blocked pattern still asks. The project scope may tighten
anywhere and may loosen only in a directory listed in
`[approval].trusted_projects`; ignored entries are named at startup.

With auto-approve on (opt-in; `shift+tab` toggles it, mid-run included),
each mutating call first passes a pure rule classifier: *safe* runs,
*blocked* always asks, *uncertain* goes to a model evaluation that must
both approve and be confident. Memory writes are the exception to the
third branch: they are Review-tier but skip the evaluation entirely and
always ask, because the evaluator is the party that proposed the write
(ADR-0020 §6). Every failure path asks. The blocked tier
is a floor the model cannot lift, and the sandbox applies in all modes.
For a turn's first rounds the model evaluation also sees the operator's
typed request as wrapped evidence (ADR-0038) — misalignment escalates;
later rounds keep the call-only view byte-identically. MCP calls
additionally carry the tool's server-published self-description as
wrapped evidence (ADR-0046) — a claim, never a fact; a self-arguing
description escalates.

`/settings` shows every setting with its provenance and edits what can
take effect now: the approval policy, auto-approve and auto-compaction.
Rows that cannot take effect until restart — the theme among them — are
shown read-only rather than offered as edits that would do nothing. Persisted policy goes to `~/.config/gem-agent/policy.toml`, a
machine-owned file that wins collisions with hand-written `config.toml`
(ADR-0009), which is never rewritten so its comments survive.

## Context

The status line shows occupancy against the model's input window — from
`[model].context_window` if set, else model metadata, else the 1M family
estimate marked `~` (Vertex does not report `inputTokenLimit`). The same
value drives auto-compaction, which is why it is resolved in every mode
including `-p`.

At `[agent].compact_at_pct` of the window, between rounds, the older part
of the conversation is replaced by a summary and the recent part kept
verbatim. The cut never lands on a tool result, so no function response
is orphaned. The summariser gets no tools and reads the transcript as
nonce-wrapped data; the summary comes back as an attachment, quoted as
data. Any failure leaves history untouched.

## Persistence

One JSONL file per session under
`~/.local/state/gem-agent/sessions/projects/<escaped path>/` (per
project, the memory convention — ADR-0022; legacy flat files under
`sessions/` are read in place, never moved), mode `0600`, named by
timestamp. `GEMAGENT_STATE_DIR` relocates the whole state root for
test/drill isolation. It is both the diagnostic log and the
resume source, so records that carry conversation state are complete —
tool-call arguments, attachments, thought signatures — while diagnostic
records stay summarised and are skipped on load. Compactions are recorded
and replayed, so a compacted session resumes compacted.

`--continue` picks the most recent session **of this project that has a
conversation in it**; `--resume <id>` names one. Resume refuses a
different project directory or a different model rather than warning.
Ids are validated as ids and never interpreted as paths.

Memory (ADR-0020) lives beside the sessions:
`~/.local/state/gem-agent/memory/global/` plus
`memory/projects/<escaped path>/` (a `.project` marker guards the lossy
escaping), one markdown file per fact. Everything is injected into the
system prompt at session start under a budget, framed as agent-recorded
background knowledge; `save_memory`/`delete_memory` are approval-gated
and classified Review, never Safe — memory is a persistence vector for
injected instructions, so the write is where the human reviews.

Telemetry (ADR-0035) is the one outbound channel: with
`[telemetry].enabled`, audit events — sessions, tool calls with
outcomes, approval decisions, token usage — export to Cloud Logging of
the `[gcp]` project (default) or to an OTLP collector. Metadata only,
and only the operator's global config can enable it or aim it; a
project file structurally cannot. Events from the file-search child
agent carry an `agent` label. Export failures warn once and never
block the session; shutdown flushes with a 3s cap.

## Configuration and drop-in behaviour

`~/.config/gem-agent/config.toml`, strict decode (unknown keys are
errors), precedence flags > `GEMAGENT_*` > `GOOGLE_CLOUD_*` > file >
defaults. Model names are always configured, never compiled in.

Nothing per-project is required. `AGENTS.md` / `AGENT.md` / `CLAUDE.md` /
`GEMINI.md` are collected from `~/.config/gem-agent/`, then ancestor
directories outermost-first, then the project — the walk stops at `$HOME`,
because an instruction file is obeyed as instructions. MCP servers come
from `~/.config/gem-agent/mcp.json` and `<project>/.mcp.json` in Claude
Code format; the project wins a name collision. MCP has no cancel, so a
timed-out call kills the server child and the next call respawns it.
`/mcp reload` and `/skills reload` (ADR-0039) re-run the startup paths
mid-session under the startup trust verdict — tool declarations and
the system prompt's skill section follow, and the reload is audited;
`--mcp on|off` overrides `[mcp].enabled` per run.

Skills (ADR-0010/0011) follow the same arrangement as MCP:
`~/.config/gem-agent/skills` (gem-agent's own; sharing with Claude Code
is an operator-made symlink, which discovery follows) plus
`<project>/.claude/skills` (shared), in Claude Code's format. One
description line each in the system prompt, bodies loaded on demand via
the read-only `load_skill` tool or injected directly by `/skill <name>`; either
opens with Claude Code's line `Base directory for this skill: <dir>` so the
skill's own scripts can be run (ADR-0070). `load_skill`
results are the one tool output *not* nonce-wrapped — skill bodies are
operator-installed instructions, and the exemption is bounded by reads
confined to discovered skill directories.

## Failure behaviour, in one place

| Situation | What happens |
|---|---|
| Content filter blocks a response | announced, retried once, then reported by name |
| Model returns nothing | reported with the finish/block reason, never stored |
| Summarisation fails | history untouched, turn continues; twice disables auto-compaction |
| Tool denied or errors | result string to the model, so it can react |
| MCP call times out | server killed, respawned lazily on the next call |
| 429 / 5xx before any chunk | exponential backoff retry; never after output has been consumed |
| Session log unwritable | warning, session continues (a broken log must not stop the agent) |
| Resume target unreadable | fatal — the operator asked for that history |
| Telemetry export fails | one warning, then silent degradation — never blocks the session |
| Stream silent for 90s | the status line becomes a stall warning naming Ctrl+C; no automatic timeout — long thinking is legitimate, and a big file write is measured minutes of silence (ADR-0056) |
| A tool ignores cancellation | the file walks stop on their own within one syscall and return a labelled partial result; any other call is abandoned by the agent's floor 1 s after the cancel (`outcome = abandoned`; a late return is recorded, and a mutating one announced next turn) — the turn ends either way (ADR-0065). If even that is not enough, second Ctrl+C warns, third quits the process — in the TUI, the plain REPL and `-p` alike; the transcript is written per event, so everything up to the wedged call is on disk |
| The file-search child agent fails | error result to the model; the spend is tallied anyway |
| Round limit reached | progress review + dialog (auto mode may continue itself); extensions up to 3× max_turns; the stop message teaches "continue", never /clear (ADR-0040) |
| SIGINT | cancels the turn, not the process (escalation: see the stuck-tool row) |

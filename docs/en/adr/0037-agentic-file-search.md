# ADR-0037: agentic_file_search — delegated project search in an isolated context

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: reduce context load — an agentic file search, distinct from the literal search_files, beats a general-purpose sub-agent for this goal |

## Context

Everything the model reads is replayed on every later round. Answering
"where is X handled?" costs several rounds of `list_tree` /
`search_files` / `read_file`, and the dead ends — the files that turned
out irrelevant — stay in the history for the rest of the session,
re-sent with every request. `search_files` finds strings the model
already knows; it cannot answer a question.

ADR-0014 already established the cure for one file: return a summary
instead of the bytes. This ADR generalises it from one file to one
question.

A general-purpose sub-agent ("run this task in a child loop") was
considered first and deliberately narrowed. Three reasons: a mutating
child call would hit the approval gate with a conversation the operator
cannot see — approval without context is not approval (the same
structure made background agents with write tasks fail in practice); a
general `task` argument is an open instruction channel with the
injection surface that implies; and a delegate-anything tool invites
delegating everything, doubling token spend. A search-only agent keeps
the isolation benefit and loses every one of those problems. The
general tool, if ever wanted, is a strict superset of this machinery
and gets its own ADR.

## Decision

### 1. One read-only tool: `agentic_file_search(question)`

A child agent loop on the **main model** (multi-round tool judgment is
the job; the summary model is too weak for it), with a **positive
allowlist** of read-only tools: `list_files`, `list_tree`,
`search_files`, `read_file`, `file_info`, `read_document`,
`summarize_file` — the last on `[model].summary`, the same construction
as the main loop. The allowlist is built by `Registry.Subset`, which
errors on an unknown name: a typo in a security-relevant allowlist must
not silently drop a tool.

Excluded, each deliberately: every mutating tool (the approval-context
problem above), `web_search`/`web_fetch` (egress stays under the main
loop's gate), MCP tools (semantics unknown to gem-agent), `ask_user`
(the interaction budget belongs to the main conversation),
`view_image` (a text report cannot carry pixels), `load_skill`
(instruction-grade results are an ADR-0010 hole to keep narrow),
memory tools (mutating), and **itself** — recursion and fan-out are
structurally impossible, not policed by prompt.

### 2. Isolation, bounded

Each call builds a fresh agent: own history, own nonce tag, `MaxTurns`
10, no auto-compaction, no transcript — the child's internals are
ephemeral exactly like ADR-0014's side-calls; one usage record is
logged. The report re-enters the main conversation as an ordinary tool
result and is therefore nonce-wrapped: it is derived from file
contents, so it is untrusted data, and the existing machinery already
treats it as such. The child gets a deny-all approver even though its
registry holds nothing mutating — if composition ever changes that,
the call fails closed instead of prompting the operator about a
context they cannot see.

### 3. The output contract names its negative space

The child's system prompt fixes the report shape: direct answer;
evidence as `path:line-range` with short verbatim quotes; **an explicit
statement of what was not found or not verified** — an absent negative
result reads as "nothing to report", which is how search results lie;
and at most one `Note:` line as the destination for out-of-scope
observations (a destination, not a ban — banning loses the
information, per the knowledge-base lesson on sub-agent output
contracts). The report answers in the question's language. The result
header names the model, the round count, and that quotes are lossy —
verify with `read_file` before editing.

### 4. Delegation is observable and audited

- The child shares the main backend, so the ADR-0033 stream heartbeat
  keeps ticking during delegation for free — a delegated minute must
  not look like a hang.
- The child's tool calls render live as `↳ tool` lines in the TUI and
  plain REPL, so the operator watches the search happen.
- Telemetry (ADR-0035) gains `Sink.Sub(label)`: every child event —
  `tool.call`, `model.usage`, `turn.end` — carries
  `agent="agentic_file_search"`. An audit trail that loses what a
  delegate read is not an audit trail.
- Token spend lands in the `/usage` tally like `summarize_file`
  (per-category accounting, ADR-0019): the context gauge tracks the
  main conversation only, and the child never touches it.

Cancellation needs nothing new: the turn's context flows through the
tool call into the child loop, so Ctrl+C (and the ADR-0034 ladder)
interrupts the child exactly as it interrupts everything else.

## Consequences

- Orientation questions cost one call and return one report; the
  exploration, including its dead ends, never enters the main history.
- The model needs the discrimination the descriptions provide: literal
  string → `search_files`; question → `agentic_file_search`. The
  question must say what to find *and* what to report.
- One-shot `-p` mode works unchanged — the tool is read-only.
- A child that exhausts its 10 rounds fails with a clear error naming
  the limit; the answer is a narrower question, not a bigger budget.
- Parallel fan-out and a general-purpose sub-agent remain future ADRs;
  both would reuse this machinery.

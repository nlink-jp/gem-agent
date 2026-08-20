# ADR-0030: A read-only self-information tool for the model

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the model should be able to learn system information and session information (model name, token consumption, …) through a built-in tool |

## Context

The operator sees the runtime everywhere — the footer, `/usage`,
`/settings` — but the model sees none of it. Asked "which model are
you?", it guesses (models do not reliably know their own deployment
name, and this agent swaps models by config). Asked to plan work
against the remaining context, it cannot see the window occupancy that
the footer displays. Asked about the host, it must spend an approval
round on `shell_exec` for what `uname` would print. Everything the
model needs already exists in process; there is simply no tool that
hands it over.

## Decision

### 1. One read-only tool: `agent_info`

A single no-argument tool that reports the agent's own runtime:
version, platform (OS/arch/CPU count/macOS version), main and summary
model, thinking level, context-window occupancy, cumulative token
usage, limits (max turns, shell timeout), approval and sandbox state,
project directory, session id, connected MCP servers, skill count,
memory and media-bucket availability. One tool, not a system/session
pair: every plausible call wants the same page, and the registry stays
small.

`Mutating: false` — it runs on the read-only tier without approval,
like `file_info`. It discloses nothing the operator's own screen does
not already show.

### 2. The numbers are the `/usage` numbers

The tool renders the same `agent.UsageStats` that `/usage` reads —
one accounting source, two views. Totals reflect completed rounds, so
a mid-turn call reports the state as of the previous round; that is
the freshest truth the accounting has.

### 3. What is deliberately withheld

- **GCP project id and bucket name** — environment identifiers with
  no behavioral value to the model; the bucket appears only as
  `configured`/`none` (whether large media can attach is behavioral).
- **Hostname / username** — same reasoning; transcripts travel.

The rule the exclusions follow: a field earns its place by changing
what the model should do, not by being available.

### 4. Output is English

Tool results are model-facing text — exactly the surface ADR-0029 §3
keeps out of the language catalogs.

## Consequences

- "Which model are you", "how much context is left", "what platform is
  this" become one cheap tool call instead of a guess or an approval
  round.
- The snapshot is assembled in `cmd` (the only layer that sees config,
  agent, tally, session, and MCP state together) behind a provider
  closure; rendering is a pure function and tested as one.
- A future field must pass the §3 rule before it is added.

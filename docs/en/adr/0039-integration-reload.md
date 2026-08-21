# ADR-0039: in-session reload of skills and MCP servers, and the --mcp flag

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: adding a skill or fixing an MCP server mid-session requires restarting the whole session; and one-shot `-p` runs should be able to switch MCP on/off from the command line |

## Context

Skills and MCP servers load once, at startup. Installing a skill
mid-session, adding a server to `mcp.json`, or recovering a wedged
server all required ending the session — losing its context — for an
operation that touches nothing about the conversation. (A timed-out
MCP call already kills and lazily respawns its server, but that heals
only the crash case: config changes and new tool lists never arrive.)

Separately, every `-p` one-shot run pays the MCP startup cost —
spawning every configured server's child process — even though MCP
tools are approval-gated and one-shot mode cannot answer a gate, so in
the common configuration those children serve nothing.

## Decision

### 1. `/skills reload` and `/mcp reload` — subcommands, not new surfaces

Reload lives on the commands that already display these integrations.
`/mcp reload` removes every `mcp__*` tool from the registry, closes
the old clients, and **re-runs the same startup connect path** —
global config, project config, merge rules, warnings — one code path,
never a parallel one. `/skills reload` re-runs skill discovery.
Connection warnings land in the command's own output: a reload runs
under the TUI, where a stderr write would corrupt the display.

### 2. The trust decision is startup's, and reload cannot widen it

Reload reuses the trust verdict decided at startup (ADR-0023): an
untrusted project's `.mcp.json` and skills stay unloaded, exactly as
at launch. The trust gate runs once, before anything loads — a reload
that could re-ask (or silently widen) would turn a display command
into a trust surface. Changing the trust answer still takes a restart.

### 3. Declarations and the system prompt follow

The agent re-caches its tool declarations after an MCP reload, and a
skills reload rebuilds the system prompt's skill section. Both
invalidate the byte-identical request prefix, so the implicit cache
(ADR-0018) re-warms on the next round — a deliberate, operator-
initiated cost, the same one a restart would pay anyway.
`load_skill` is registered from the start even when zero skills are
installed (reading the live list through a getter), so a reload can
populate a session that began empty.

No locking was added: a slash command structurally cannot run while a
turn runs (ADR-0007 queues input, and `/` cannot be queued), so
reload shares the turn goroutine's single-writer discipline.

### 4. What deliberately survives a reload

The session approval allowlist (an `a` answer) is keyed by tool name
and survives: the operator approved the *tool*, and a reconnected
server presenting the same name is the same decision. The per-tool
policy, auto-approve state, and the conversation are untouched — that
is the point of reloading instead of restarting.

### 5. `--mcp on|off`

A CLI override for `[mcp].enabled`, at the top of the precedence
order and shown as `flag` in `/settings` provenance. `off` is the
pipeline case: a `-p` run skips every server spawn. `on` is the
mirror: force MCP for one run against a config that disables it.

### 6. Audited

A reload changes what the session can do — an MCP reload spawns child
processes — so it emits an `integration.reload` audit event
(kind, servers, tools) alongside a transcript record. A session whose
tool surface grew mid-way must not look, in the audit log, like the
session that started.

## Consequences

- Adding a skill or repairing a server is now a two-keystroke
  operation that keeps the conversation.
- A wedged MCP server gets a recovery path stronger than the lazy
  respawn: full reconnect, fresh tool list, without losing context.
- `/mcp reload` blocks its surface while servers connect (same
  timeouts as startup) — invoked deliberately, like startup.
- One-shot pipelines with `--mcp off` skip N child spawns per run.
- Removed servers' tools vanish from `/tools` and the declarations;
  the model is told about unknown tools the usual way if it tries one
  from stale context.

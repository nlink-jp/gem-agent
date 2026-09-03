# RFP: gem-agent

> Generated: 2026-08-18
> Status: Draft

## 1. Problem Statement

**gem-agent** is a CLI interactive agent runtime backed by Vertex AI Gemini 3.x.
It provides local file read/write, command execution, and MCP server connectivity,
and interprets an existing project's AGENTS.md / CLAUDE.md / .mcp.json as-is, so
that one project setup serves every runtime that works on it (drop-in) — this is
the single most important requirement. The target user is the nlink-jp operator
themselves. The scope is deliberately minimal (read / edit / shell / MCP /
approval gates) with no analysis or GUI subsystems. macOS-only, defended by two
layers: sandbox-exec plus approval gates.

> **Positioning note (2026-09-01).** This document was originally written for a
> continuity tool — a backup for when Claude Code is unavailable. That charter
> was retired by [ADR-0061](adr/0061-independent-runtime-promotion.md):
> gem-agent is an independent agent runtime, and the backup language that
> remains in the Discussion Log below is history, not the current charter.

## 2. Functional Specification

### Commands / API Surface

| Command | Description |
|---|---|
| `gem-agent` | Start an interactive REPL with the current directory as the project |
| `gem-agent -p "<prompt>"` | One-shot mode (exit after the answer; pipe-friendly) |
| `gem-agent --version` | Print version (must respond — brew test calls it) |

**Flags:**

- `--config <path>` — override config file path
- `--model <name>` — override model name
- `--no-sandbox` — disable the sandbox-exec wrapper (debugging only; prints a warning at startup)
- `--thinking <level>` — override the reasoning level ([ADR-0025](adr/0025-thinking-level.md))
- `--mcp on|off` — enable or disable MCP for this run ([ADR-0039](adr/0039-integration-reload.md))
- `-p, --prompt <text>` — one-shot execution, for pipes
- `-c, --continue` / `--resume <id>` — resume a session ([ADR-0005](adr/0005-session-resume.md))

**REPL slash commands:** the live set is `/help` `/tools` `/mcp` `/memory` `/skills`
`/skill` `/settings` `/usage` `/auto` `/compact` `/clear` `/quit` (`/exit` aliases
`/quit`), with `reload` subcommands on `/mcp` and `/skills`. The
[interface reference](reference/interface.md) carries the current table — this
paragraph names them rather than owning the list, so it cannot fall behind.

**Built-in tools:** v1 shipped five (`list_files`, `read_file`, `write_file`,
`edit_file`, `shell_exec`) plus MCP tools. Each later addition is an ADR (see
*Admitted after v1* below), and the
[tools reference](reference/tools.md) is the authoritative list — a second
enumeration here would be a second thing to keep in step. The gating rule has not
changed:

| Kind | MITL default |
|---|---|
| Read-only tools (navigation, reading, summarising, `agent_info`, `ask_user`) | auto-approved |
| Mutating tools (`write_file`, `edit_file`, `shell_exec`, memory writes, web egress) | per-call approval |
| MCP tools (from external servers) | per-call approval, never auto-approvable to Safe |

The approval prompt offers "always allow in this session", which registers the tool in
a session-scoped allowlist (not persisted).

### Input / Output

- **Input:** multi-line paste-safe reading (structurally prevents leftover pasted lines
  leaking to the shell, a known failure of single-line Scanner reads). Input history
  navigation (ArrowUp/Down) must be implemented with an explicit "navigating history"
  state flag
- **Output:** model responses stream to the terminal; tool executions are shown as event lines
- **Session log:** appended as JSONL, one file per conversation, under
  `~/.local/state/gem-agent/sessions/projects/<escaped-project-path>/`
  ([ADR-0022](adr/0022-per-project-session-layout.md) moved it there from the flat
  directory v1 used). After v1 shipped this became the resume source of truth
  (ADR-0005)

### Configuration

`~/.config/gem-agent/config.toml` (follows the organization-wide Vertex AI config schema):

```toml
[gcp]
project  = "your-project-id"
location = "global"   # Gemini 3: "global", "us", or "eu" only; single regions 404

[model]
name = "<gemini-3.x model id>"

[sandbox]
enabled = true

[agent]
max_turns = 50
```

- **Env precedence:** `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file > built-in defaults
- **Project-side files (core of drop-in compatibility):**
  - `AGENTS.md` / `CLAUDE.md` — injected into the system prompt (AGENTS.md first; both included when both exist)
  - `.mcp.json` — MCP server definitions in Claude Code format interpreted as-is (stdio)

### External Dependencies

- **Vertex AI Gemini 3.x** — `google.golang.org/genai` (`BackendVertexAI`), ADC authentication
- **nlk** (github.com/nlink-jp/nlk) — guard (nonce XML isolation) / jsonfix / backoff
- No other external service dependencies

## 3. Design Decisions

### Language and platform

- **Go** — the organization's CLI standard. Single-binary distribution with an
  established signing/notarization flow. The official Vertex AI Go SDK
  (`google.golang.org/genai`) is available
- **macOS-only** — the design assumes process sandboxing via sandbox-exec (Seatbelt).
  Cross-platform support is explicitly dropped

### Relationship to existing tools

- **gem-cli** (cli-series) — one-shot Gemini CLI. gem-agent differs by having an
  interactive agent loop (complementary roles)
- **agent-skeleton** (lab-series) — design source for the ReAct loop, PathGuard, and
  tool set (Python POC; the design is inherited, not the code)
- **shell-agent-v2** (util-series) — implementation source for Gemini 3 thought
  signature handling (ADR-0009), MITL gates, and the MCP kill-and-respawn pattern
- **mcp-guardian** — can transparently wrap stdio MCP servers and adds an audit trail (opt-in)
- **nlk** — shared library for prompt-injection isolation, JSON repair, and backoff

### Security design (two layers)

1. **Primary defense = MITL approval gates** — write / exec / MCP require per-call
   approval plus a session-scoped allowlist
2. **Defense-in-depth = sandbox-exec** — `shell_exec` is wrapped in an SBPL profile
   restricting file writes to the project directory + scratch. This is the structural
   answer to the agent-skeleton external-review finding that heuristic path validation
   cannot stop dynamic path construction (command substitution, etc.)
3. **Isolation of tool output and file contents** — nonce-tagged XML wrapping via
   nlk/guard enforces "data, not instructions". Defensive instructions are placed at
   the top of the system prompt

### Protocol handling

- **Gemini 3 thought signature echo-back implemented from Phase 1** — the known trap
  where the second round of a tool-call loop fails with 400. Capture/replay follows the
  shell-agent-v2 ADR-0009 pattern
- **MCP cancellation** — the protocol has no cancel notification; interruption is
  implemented as child-process kill-and-respawn

### Out of scope (explicit)

- Data analysis features (DuckDB, etc.)
- GUI
- Linux / Windows support

**Admitted after v1, each by ADR** (use argued against the original
reasoning; both are recorded rather than quietly added):

- Session resume — [ADR-0005](adr/0005-session-resume.md). A fallback
  tool is reached for during long work, and a session that ends took its
  context with it.
- Context compaction — [ADR-0006](adr/0006-context-compaction.md). A
  session that dies at the context window, with `/clear` as the only
  recovery, is not much of a fallback.
- Context caching — [ADR-0018](adr/0018-context-caching.md). A
  session-scoped isolation tag makes the request prefix cacheable;
  measured 0% → 81–95% cached on an identical task.
- Web access — [ADR-0017](adr/0017-web-tools.md). Grounded search
  (first-party; the agentic-web-search freeze is the history) and
  URL-context fetch digested by the lightweight model.
- file_info — [ADR-0016](adr/0016-file-info.md). Type judgement,
  metadata, and the hash trio the org's lookup MCPs consume.
- edit_file v2 — [ADR-0015](adr/0015-edit-file-v2.md). Batched atomic
  edits with diagnosed misses and evidence on success: the write half of
  the same waste.
- Context economy — [ADR-0014](adr/0014-context-economy-tools.md).
  Line-window reads and a summarize_file tool on a configurable
  lightweight model: finding became cheap (ADR-0013); reading was the
  cost left.
- Navigation tools — [ADR-0013](adr/0013-navigation-tools.md). A tree
  listing and a fast dependency-free grep: orientation cost one round
  per directory, and finding things cost reading files wholesale.
- Image input — [ADR-0012](adr/0012-image-input.md). The work most
  often starts from a screenshot, and MCP servers produce images the
  model itself must look at.
- Skills — [ADR-0010](adr/0010-skills.md). Claude Code's skill format
  read as-is: the operator's skills-series encodes procedures that a
  fallback session otherwise loses, which is the same gap this tool
  exists to cover.

The ten above are the additions whose reasoning argued hardest against
this section, kept in full because the argument is the point. Every
later one is recorded the same way and is listed in
[`INDEX.md`](INDEX.md), which is the authoritative catalogue: per-tool
approval policy (0008), settings panel (0009), usage accounting (0019),
**agent memory (0020)** — which this section previously ruled out, and
which is admitted on the same use-argued-against-the-reasoning basis:
facts learned during work died with the session, and the fallback tool
loses exactly that continuity when it is needed most — thinking level
(0025), document reading (0026), audio and video (0027), UI language
(0029), agent self-info (0030), datetime (0032), turn observability
(0033), audit telemetry (0035), `ask_user` (0036), agentic file search
(0037), integration reload (0039), round-limit intervention (0040), and
terminal diagrams (0042). This paragraph names the catalogue rather
than duplicating it: an enumeration maintained in two places falls
behind in one of them, and this one did.

Scope minimization follows the shell-agent v1 lesson (feature accumulation → complexity
→ rewrite). The original yardstick — "a backup tool needs the core 20% of Claude
Code's daily features" — was replaced by a self-defined charter when the backup role
was retired (ADR-0061): a minimal, auditable agent loop — read / edit / shell / MCP /
approval — with no analysis or GUI subsystems. Features still enter by ADR only.

## 4. Development Plan

### Phase 1: Core

- Agent loop (Vertex AI + thought signature capture/replay + streaming)
- Built-in tools (list_files / read_file / write_file / edit_file / shell_exec)
- MITL approval gates + session-scoped allowlist
- Config loader (TOML + env precedence)
- Paste-safe REPL (multi-line reads, history-nav state flag)
- JSONL session log
- sandbox-exec SBPL profile generation
- Unit tests (loop verification with a mock LLM, tool argument validation, SBPL generation)

### Phase 2: Features

- MCP client (stdio, `.mcp.json` compatible)
- Opt-in configuration via mcp-guardian
- AGENTS.md / CLAUDE.md system prompt injection
- nlk/guard nonce isolation applied to all tool output
- One-shot mode (`-p`)
- 429 backoff (nlk/backoff)

### Phase 3: Release

All delivered (2026-08-19):

- docs/{en,ja} three-tier documentation + ADRs — [`INDEX.md`](INDEX.md),
  `reference/`, `adr/` (six ADRs at the time; the catalogue in
  [`INDEX.md`](INDEX.md) is current), with `scripts/docs-mirror-check.sh`
  enforcing the en/ja mirror in `make check`
- E2E on a real project — first drill run against `json-filter` and this
  repository; the real-task step (gem-agent alone) was a read-only review
  of the tool layer's path confinement, verified against the source
- Release — signed + notarized darwin/arm64, Homebrew tap
- [**Monthly drill runbook**](reference/drill.md) — three of its steps
  were rewritten by its own first run
- [**Written promotion criteria**](reference/promotion.md) for cli-series

Each phase is independently reviewable.

## 5. Required API Scopes / Permissions

- GCP Application Default Credentials (`gcloud auth application-default login`)
- `roles/aiplatform.user` on the target GCP project
- No OAuth client, no API keys, no permissions on other services

## 6. Series Placement

**Series: cli-series** (promoted 2026-09-01 by operator decision, ADR-0061)

**Reason:** The original placement was lab-series — experimental during the initial
period, with promotion to cli-series once E2E and drill operations had proven it, and
the tension between "experimental shelf" and "a backup must always work" mitigated by
the monthly drill routine and written promotion criteria. When the backup charter was
retired, the drill-based bar was superseded rather than passed: real-world deployment
already answered the question the new role needs answered
([promotion](reference/promotion.md) records the original bar and the decision).
The cli-series contract applies: interface stability is a promise, and breaking
changes go through the org's breaking-change process.

## 7. External Platform Constraints

- **Gemini 3 thought signature** — failing to echo back each Part's signature in the
  next request causes 400 INVALID_ARGUMENT (protocol constraint, unavoidable)
- **Vertex AI rate limits** — sequential high-volume calls trigger frequent 429s;
  backoff is mandatory and throughput expectations should be conservative
- **Gemini 2.5 retirement (on/after 2026-10-16)** — design for 3.x only; model name is
  config-driven (no hardcoding)
- **function_call argument size** — practical limit is a few hundred KB to 1 MB; pass
  large data via files
- **sandbox-exec status** — officially deprecated by Apple (de facto industry standard).
  Record the risk and alternatives (containers / Containerization framework) in case a
  future macOS removes it

---

## Discussion Log

- **2026-08-18 initial discussion:** corrected the initial premise that "no similar
  tool exists in the org" — agent-skeleton (Python POC with file/shell/MCP tools +
  PathGuard) and shell-agent-v2 (GUI with Vertex + MITL + sandbox + MCP) both exist.
  Confirmed that the combination "CLI, Go, direct Vertex, macOS sandbox" does not
  exist, and framed the new build as "porting design assets from two existing
  projects" rather than starting from zero
- **Requirements derived from the backup role:** (1) drop-in compatibility (reads
  AGENTS.md / CLAUDE.md / .mcp.json as-is) is the most important feature, (2) scope is
  deliberately minimal, (3) an untrained backup rots — monthly drills are an
  operational requirement
- **Tool name:** gem-agent. Rejected gem-code (reads as code-only) and gem-pilot (role
  not readable from the name). Joins the gem-* naming family
- **Series placement:** cli-series was recommended; the user chose lab-series.
  The tension with the backup role is mitigated by written promotion criteria (Phase 3)
- **Approval model:** per-call approval + session-scoped allowlist (Claude Code
  style). Rejected always-ask (too slow in an emergency) and config-side pre-approved
  allowlists (high risk on misconfiguration)
- **Sandbox mechanism:** sandbox-exec as default. Rejected containers (podman) as too
  heavy to start for a backup tool. Recorded the macOS 26 Containerization framework
  as a future alternative
- **Context compaction / memory / resume:** explicitly excluded from v1
  (shell-agent v1 over-scoping lesson). Compaction and resume were
  admitted after v1 shipped, each with an ADR (0006, 0005) — use showed
  that both are load-bearing for the fallback role, not conveniences.
  Memory was admitted later on the same basis, by ADR-0020

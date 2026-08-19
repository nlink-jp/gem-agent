# RFP: gem-agent

> Generated: 2026-08-18
> Status: Draft

## 1. Problem Statement

**gem-agent** is a CLI interactive agent backed by Vertex AI Gemini 3.x, built as a
continuity tool for development work in situations where Claude Code is unavailable
(Anthropic-side outages, contractual or network constraints). It provides local file
read/write, command execution, and MCP server connectivity, and interprets an existing
project's AGENTS.md / CLAUDE.md / .mcp.json as-is, so that switching over during an
outage requires no per-project reconfiguration (drop-in) — this is the single most
important requirement. The target user is the nlink-jp operator themselves. The scope
is deliberately minimal (read / edit / shell / MCP / approval gates) with no memory,
analysis, or GUI subsystems. macOS-only, defended by two layers: sandbox-exec plus
approval gates.

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

**REPL slash commands:** `/help` `/tools` `/mcp` `/clear` `/quit`

**Built-in tools:**

| Tool | MITL default |
|---|---|
| `list_files` | auto-approved |
| `read_file` | auto-approved |
| `write_file` | per-call approval |
| `edit_file` | per-call approval |
| `shell_exec` (wrapped in sandbox-exec) | per-call approval |
| MCP tools (from external servers) | per-call approval |

The approval prompt offers "always allow in this session", which registers the tool in
a session-scoped allowlist (not persisted).

### Input / Output

- **Input:** multi-line paste-safe reading (structurally prevents leftover pasted lines
  leaking to the shell, a known failure of single-line Scanner reads). Input history
  navigation (ArrowUp/Down) must be implemented with an explicit "navigating history"
  state flag
- **Output:** model responses stream to the terminal; tool executions are shown as event lines
- **Session log:** appended as JSONL (`~/.local/state/gem-agent/sessions/`). After v1
  shipped this became the resume source of truth (ADR-0005)

### Configuration

`~/.config/gem-agent/config.toml` (follows the organization-wide Vertex AI config schema):

```toml
[gcp]
project  = "your-project-id"
location = "us-central1"

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

- Memory subsystems (Global/Session Memory, etc.)
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
- Image input — [ADR-0012](adr/0012-image-input.md). The work most
  often starts from a screenshot, and MCP servers produce images the
  model itself must look at.
- Skills — [ADR-0010](adr/0010-skills.md). Claude Code's skill format
  read as-is: the operator's skills-series encodes procedures that a
  fallback session otherwise loses, which is the same gap this tool
  exists to cover.

Scope minimization follows the shell-agent v1 lesson (feature accumulation → complexity
→ rewrite). A backup tool needs the core 20% of Claude Code's daily features.

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
  `reference/`, `adr/` (six ADRs), with `scripts/docs-mirror-check.sh`
  enforcing the en/ja mirror in `make check`
- E2E on a real project — first drill run against `json-filter` and this
  repository; step 7 (a real task with gem-agent alone) was a read-only
  review of the tool layer's path confinement, verified against the source
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

**Series: lab-series**

**Reason:** Place the new build in lab-series as experimental during its initial
period, and consider promotion to cli-series once E2E and drill operations have proven
it. The tension between "experimental shelf" and "a backup must always work" is
mitigated by the monthly drill routine and written promotion criteria.

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
  Memory subsystems remain out

# gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x — a continuity tool for
development work when Claude Code is unavailable.

> **Status: pre-release (development Phase 2 complete).** The agent loop,
> MCP client, and drop-in project compatibility all work end-to-end against
> Vertex AI (verified live with Gemini 3.7). Remaining before release
> (Phase 3): real-project E2E, drill runbook, packaging.
> See the [RFP](docs/en/gem-agent-rfp.md) for the full specification.

[日本語版 README](README.ja.md)

## Why

When Claude Code is unavailable (provider-side outage, contractual or network
constraints), development work should not stop. gem-agent is a deliberately
minimal fallback agent on an independent backend (Vertex AI), designed to be
**drop-in**: it reads an existing project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` as-is, so switching over requires no per-project reconfiguration.

## Features (implemented)

- Interactive REPL with a Gemini agent loop — streaming output, Gemini 3
  thought-signature capture/replay (verified live)
- Built-in tools: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`,
  all confined to the project directory (symlink escapes included)
- Per-call approval gates (MITL) for mutating tools, with a session-scoped
  allowlist (`y` = once, `a` = always this session; deny fails closed)
- `shell_exec` wrapped in macOS sandbox-exec — file writes restricted to the
  project directory + scratch dirs (enforcement covered by a real Seatbelt test)
- Paste-safe input: a multi-line paste becomes one input, never one LLM call per line
- JSONL session log under `~/.local/state/gem-agent/sessions/`
- Slash commands: `/help` `/tools` `/mcp` `/clear` `/quit`
- **Drop-in project compatibility**: the project's `AGENTS.md` / `CLAUDE.md`
  are injected into the system prompt, and its `.mcp.json` (Claude Code
  format, stdio servers) is connected as-is — zero per-project setup
- MCP client: tools appear as `mcp__<server>__<tool>`, always approval-gated;
  timed-out calls kill the server child (MCP has no cancel) and it respawns lazily
- Tool output is isolated with per-call nonce XML tags (nlk/guard) — content
  returned by tools is framed as data, never instructions
- One-shot mode `-p "<prompt>"`: single turn, answer on stdout, mutating
  tools denied (pipe-friendly)
- Transient Vertex failures (429/5xx) retry with exponential backoff

Out of scope by design: memory subsystems, context compaction, data analysis,
GUI, session resume, non-macOS platforms.

## Usage

```sh
cd /path/to/your/project
gem-agent                                  # interactive REPL
gem-agent -p "summarize this repository"   # one-shot, pipe-friendly
```

The current directory becomes the project: file tools cannot leave it, and
sandboxed shell commands cannot write outside it. Mutating tool calls show
an approval prompt before running (`y` once / `n` deny / `a` always this
session). `--no-sandbox` disables the Seatbelt wrapper (debugging only),
`--model` overrides the configured model.

## MCP servers

gem-agent reads the project's `.mcp.json` (Claude Code format; stdio
transport, `${VAR}` expansion):

```json
{
  "mcpServers": {
    "tor-exit": { "command": "tor-exit-lookup", "args": ["mcp"] }
  }
}
```

To add governance and an audit trail, route a server through
[mcp-guardian](https://github.com/nlink-jp/mcp-guardian) — it is itself a
stdio MCP server, so the opt-in is just a `.mcp.json` entry:

```json
{
  "mcpServers": {
    "guarded": { "command": "mcp-guardian", "args": ["--profile", "myserver"] }
  }
}
```

## Requirements

- macOS (Apple Silicon)
- Google Cloud project with Vertex AI enabled
- Application Default Credentials (`gcloud auth application-default login`)
  with `roles/aiplatform.user`

## Build

```sh
make build    # outputs dist/gem-agent
make test
```

## Configuration

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # default; Gemini 3 models are global-endpoint-only

[model]
name = "<gemini model id>"

[sandbox]
enabled = true             # default

[agent]
max_turns = 50             # default
shell_timeout_sec = 120    # default
```

Precedence: flags (`--model`) > `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file
> defaults. Unknown keys in the file are rejected (strict decode).

Note: as of 2026-08, the Gemini 3 family (verified with gemini-3.7-flash and
gemini-3-flash-preview) is served only from the global endpoint — regional
locations return 404. Gemini 2.5 models work from regional endpoints such as
`us-central1`; set `location` accordingly if you use one.

## Documentation

- [RFP (English)](docs/en/gem-agent-rfp.md) / [RFP (日本語)](docs/ja/gem-agent-rfp.ja.md)
- [ADR-0001: Sandbox mechanism](docs/en/adr/0001-sandbox-mechanism.md)

## License

MIT

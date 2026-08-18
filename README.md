# gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x — a continuity tool for
development work when Claude Code is unavailable.

> **Status: pre-release (development Phase 1 complete).** The interactive
> agent loop works end-to-end against Vertex AI (Gemini 2.5 and Gemini 3
> verified live). MCP / `.mcp.json` / AGENTS.md injection arrive in Phase 2.
> See the [RFP](docs/en/gem-agent-rfp.md) for the full specification.

[日本語版 README](README.ja.md)

## Why

When Claude Code is unavailable (provider-side outage, contractual or network
constraints), development work should not stop. gem-agent is a deliberately
minimal fallback agent on an independent backend (Vertex AI), designed to be
**drop-in**: it reads an existing project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` as-is, so switching over requires no per-project reconfiguration.

## Features (Phase 1, implemented)

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
- Slash commands: `/help` `/tools` `/clear` `/quit`

## Planned (Phase 2)

- MCP client (stdio, Claude Code `.mcp.json` compatible) + mcp-guardian opt-in
- AGENTS.md / CLAUDE.md system prompt injection (drop-in compatibility)
- nlk/guard nonce isolation of tool output, one-shot mode (`-p`), 429 backoff

Out of scope by design: memory subsystems, context compaction, data analysis,
GUI, session resume, non-macOS platforms.

## Usage

```sh
cd /path/to/your/project
gem-agent
```

The current directory becomes the project: file tools cannot leave it, and
sandboxed shell commands cannot write outside it. Mutating tool calls show
an approval prompt before running. `--no-sandbox` disables the Seatbelt
wrapper (debugging only), `--model` overrides the configured model.

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

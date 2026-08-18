# gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x — a continuity tool for
development work when Claude Code is unavailable.

> **Status: pre-release (scaffold).** The agent loop is not implemented yet.
> See the [RFP](docs/en/gem-agent-rfp.md) for the full specification.

[日本語版 README](README.ja.md)

## Why

When Claude Code is unavailable (provider-side outage, contractual or network
constraints), development work should not stop. gem-agent is a deliberately
minimal fallback agent on an independent backend (Vertex AI), designed to be
**drop-in**: it reads an existing project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` as-is, so switching over requires no per-project reconfiguration.

## Planned features (v0.1.0)

- Interactive REPL with a Gemini 3.x agent loop (streaming, thought-signature aware)
- Built-in tools: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`
- Per-call approval gates (MITL) with a session-scoped allowlist
- `shell_exec` wrapped in macOS sandbox-exec (writes restricted to the project directory)
- MCP client (stdio, Claude Code `.mcp.json` compatible)
- One-shot mode (`-p "<prompt>"`)

Out of scope by design: memory subsystems, context compaction, data analysis,
GUI, session resume, non-macOS platforms.

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

`~/.config/gem-agent/config.toml` (planned):

```toml
[gcp]
project  = "your-project-id"
location = "us-central1"

[model]
name = "<gemini-3.x model id>"
```

Environment precedence: `GEMAGENT_*` > `GOOGLE_CLOUD_*` > config file > defaults.

## Documentation

- [RFP (English)](docs/en/gem-agent-rfp.md) / [RFP (日本語)](docs/ja/gem-agent-rfp.ja.md)
- [ADR-0001: Sandbox mechanism](docs/en/adr/0001-sandbox-mechanism.md)

## License

MIT

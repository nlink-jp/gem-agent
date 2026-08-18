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

- **Interactive TUI** (Bubble Tea, inline mode — native scrollback and
  copy/paste keep working): streaming output with a spinner/status line,
  input box with ↑↓ history and multi-line editing (Ctrl+J), styled tool
  events, an approval dialog, and glamour-rendered Markdown responses.
  Piped/scripted use falls back to a plain line REPL automatically
- Gemini agent loop with Gemini 3 thought-signature capture/replay
  (verified live)
- Built-in tools: `list_files` / `read_file` / `write_file` / `edit_file` / `shell_exec`,
  all confined to the project directory (symlink escapes included)
- Per-call approval gates (MITL) for mutating tools, with a session-scoped
  allowlist (`y` = once, `a` = always this session; deny fails closed)
- **Auto-approve mode** (opt-in, shift+tab or `/auto`): each mutating call
  passes a two-tier review — a rule-based classifier first, then a model
  risk evaluation for anything uncertain. Safe calls run unattended;
  destructive, out-of-project, credential-touching, or uncertain ones
  still ask. See [ADR-0004](docs/en/adr/0004-auto-approve.md)
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
an approval dialog before running (`y` once / `n` deny / `a` always this
session). `--no-sandbox` disables the Seatbelt wrapper (debugging only),
`--model` overrides the configured model.

TUI keys (also listed in `/help`): Enter sends, Ctrl+J inserts a newline,
↑/↓ navigate input history, Ctrl+C interrupts a running turn (or clears
the input), Ctrl+D quits. Multi-line pastes land in the input box as one
message.

### Auto-approve mode

Off by default. shift+tab (or `/auto`) toggles it; the status line shows
`⚡auto` while on. Each mutating tool call then goes through:

1. **Rule tier** (no model call): *safe* → runs; *blocked* → always asks
   (`rm -rf`, `sudo`, `git push`, download-piped-to-shell, disk writes,
   credential paths, anything outside the project…); *uncertain* → tier 2.
2. **Model tier**: a separate evaluation round judges the proposed call
   (delivered to it as nonce-wrapped untrusted data, with no tools
   available). It must both approve *and* be confident, or the call asks.

Anything that fails — model error, malformed verdict, unknown tool —
asks. The blocked tier is a hard floor the model cannot override, and the
sandbox applies in every mode. Auto-approved calls are printed with their
reason, so you can see what ran unattended.

`!<command>` runs a shell command directly — sandboxed like `shell_exec`
(same timeout and output cap) but without an approval prompt, since you
typed it yourself. The command and its output are added to the model's
context, so `!git status` followed by "fix that" just works.

The input box and its status line pin to the window bottom (like Claude
Code), while the conversation scrolls above with native terminal
scrollback intact (ADR-0003; the screen is cleared once at startup). The
status line shows the model, current context occupancy against the
model's window (auto-detected from model metadata, or
`[model].context_window`), cumulative token consumption, and the project
directory.

## MCP servers

Servers are read from two scopes, both in Claude Code `.mcp.json` format
(stdio transport, `${VAR}` expansion) so entries move between them
verbatim:

| Scope | Path | Use for |
|---|---|---|
| Global | `~/.config/gem-agent/mcp.json` | servers you want in every project |
| Project | `<project>/.mcp.json` | servers specific to one repository |

Both are optional. They are merged, and on a name collision the project
entry wins. `/mcp` lists the connected servers with their scope.

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
# context_window = 1048576  # optional; footer display override (default: auto-detect)

[sandbox]
enabled = true             # default

[agent]
max_turns = 50             # default
shell_timeout_sec = 120    # default
auto_approve = false       # default; start sessions in auto-approve mode

[tui]
theme = "auto"             # auto | dark | light | plain
```

TUI accent colors use the ANSI-16 palette (they follow your terminal
theme); secondary text (footer, hints) uses a mid-gray chosen for the
detected background, keeping a real luminance gap on any theme.
`theme = "auto"` detects dark/light at startup; set `dark`/`light` if
detection picks wrong, or `plain` to disable all styling (errors keep
their `✗` marker, so nothing depends on color alone).

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

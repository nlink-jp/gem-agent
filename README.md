# gem-agent

Interactive CLI agent backed by Vertex AI Gemini 3.x — a continuity tool for
development work when Claude Code is unavailable.

> **Released.** Install with `brew install nlink-jp/tap/gem-agent`
> or from the [releases page](https://github.com/nlink-jp/gem-agent/releases)
> (Developer ID signed, Apple-notarized, macOS arm64) — the releases page
> is the authority on the current version, so this line does not repeat it.
> It is an experimental (lab-series) tool: interfaces may change between
> releases.
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
- **Session resume**: `--continue` picks up this project's most recent
  session, `--resume <id>` a specific one, `gem-agent sessions` lists
  them. The JSONL transcript under `~/.local/state/gem-agent/sessions/`
  is both the log and the resume source, recorded in full fidelity
  (Gemini reasoning tokens included, which the API requires on replay).
  See [ADR-0005](docs/en/adr/0005-session-resume.md)
- **Context compaction**: when the conversation approaches the model's
  window it is summarised instead of failing the turn — the older half
  becomes one summary, the recent half stays verbatim. Automatic at 80%
  (`[agent].compact_at_pct`), or `/compact` at any time. See
  [ADR-0006](docs/en/adr/0006-context-compaction.md)
- Slash commands: `/help` `/tools` `/mcp` `/compact` `/clear` `/quit`
- **Drop-in project compatibility**: the project's agent-instruction files
  are injected into the system prompt — `AGENTS.md`, `AGENT.md`,
  `CLAUDE.md`, `GEMINI.md`, searched up through ancestor directories the
  way other agents do, so workspace-wide rules apply to every repository
  beneath them — and its `.mcp.json` (Claude Code format, stdio servers)
  is connected as-is. Zero per-project setup
- MCP client: tools appear as `mcp__<server>__<tool>`, always approval-gated;
  timed-out calls kill the server child (MCP has no cancel) and it respawns lazily
- Tool output is isolated with per-call nonce XML tags (nlk/guard) — content
  returned by tools is framed as data, never instructions
- One-shot mode `-p "<prompt>"`: single turn, answer on stdout, mutating
  tools denied (pipe-friendly)
- Transient Vertex failures (429/5xx) retry with exponential backoff

Out of scope by design: memory subsystems, data analysis, GUI, non-macOS
platforms.

## Usage

```sh
cd /path/to/your/project
gem-agent                                  # interactive REPL
gem-agent -c                               # continue the last session here
gem-agent sessions                         # list resumable sessions
gem-agent -p "summarize this repository"   # one-shot, pipe-friendly
```

The current directory becomes the project: file tools cannot leave it, and
sandboxed shell commands cannot write outside it. Mutating tool calls show
an approval dialog before running. Answer it either by selection — ←→ or
Tab to move, Enter to confirm — or with the `y` / `n` / `a` shortcuts
(allow once / deny / always this session); Esc denies. The selection route
exists because those letters cannot be typed with a Japanese IME switched
on. The highlight starts on *allow*, except for a call auto-approve
escalated, where it starts on *deny* so a reflexive Enter cannot approve
it. `--no-sandbox` disables the Seatbelt wrapper (debugging only),
`--model` overrides the configured model.

TUI keys (also listed in `/help`): Enter sends, ↑/↓ navigate input
history, Ctrl+C interrupts a running turn (or clears the input), Ctrl+D
quits.

**Multi-line input**:

| Route | Availability |
|---|---|
| `Ctrl+J` | always — the reliable one |
| trailing `\` then `Enter` | always (the shell convention) |
| `Option`/`Alt` + `Enter` | only if your terminal sends Meta for Option |

Modifier+Enter combinations are a terminal limitation, not an
application choice: unless the terminal is configured to send an escape
prefix, `Option+Enter` and `Shift+Enter` arrive as an ordinary carriage
return, indistinguishable from submit — so they send the message. To
enable `Option+Enter`:

- **Terminal.app** — Settings → Profiles → Keyboard → *Use Option as Meta key*
- **iTerm2** — Settings → Profiles → Keys → Left Option key → *Esc+*

Pasting multi-line text always works regardless: the whole paste lands
in the input box as one message, never one LLM call per line.

### Auto-approve mode

Off by default. shift+tab (or `/auto`) toggles it; the status line shows
`⚡auto` while on. **shift+tab also works while a turn is running**, so a
long agent loop that started in manual mode can be switched over without
waiting for it to finish — the change applies from the next tool call (a
call already waiting at the dialog still needs its answer). Each mutating tool call then goes through:

1. **Rule tier** (no model call): *safe* → runs; *blocked* → always asks
   (`rm -rf`, `sudo`, `git push`, download-piped-to-shell, disk writes,
   credential paths, anything outside the project…); *uncertain* → tier 2.
2. **Model tier**: a separate evaluation round judges the proposed call
   (delivered to it as nonce-wrapped untrusted data, with no tools
   available). It must both approve *and* be confident, or the call asks.

Anything that fails — model error, malformed verdict, unknown tool —
asks. The blocked tier is a hard floor the model cannot override, and the
sandbox applies in every mode.

Both outcomes are explained: auto-approved calls print their reason, and
an escalated call's approval dialog carries a `⚠` line naming the tier
that objected and why — `blocked by rule (always asks): …` for the
deterministic floor, `escalated by risk review: …` for a model judgment.

`@<path>` attaches a project file or directory to your message —
`@src/main.go これ直して` sends the file with the instruction, and
`@docs/` sends a directory listing. **Tab completes** the path (common
prefix first, then a candidate list). References resolve inside the
project only, symlinks included; anything that cannot be attached is
reported rather than silently dropped, and attached content reaches the
model isolated as untrusted data, exactly like tool output.

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

### Resuming a session

```sh
gem-agent sessions        # ids, age, model, and the opening question
gem-agent -c              # the most recent session in this directory
gem-agent --resume 20260819-150102
```

A resumed session continues its own transcript — one file is one
conversation however many processes it took — and comes back exactly as
it was, tool results included. Two refusals are deliberate: a session
resumes only in the directory it was recorded in, and only under the
model that produced it (the replayed reasoning tokens are model-bound).
Each message names what to do instead. A session that was compacted
resumes compacted, rather than re-inflating to the size it was shrunk
from.

The transcript therefore holds the full text of every file the agent
read. It lives under `~/.local/state/gem-agent/sessions/`, mode `0600`.

### Compacting the conversation

At `[agent].compact_at_pct` of the model's window (80% by default), the
older part of the conversation is replaced by a summary of it and the
recent part is kept verbatim; `/compact` does the same on demand. The
notice says how many messages were summarised, because detail from that
half is second-hand afterwards and a model that has forgotten something
must not look like one that never knew it.

If the summarisation call fails — an error, a content filter — the
history is left exactly as it was and the turn continues on a full
context. `auto_compact = false` turns the automatic path off; `/compact`
still works.

## Project instructions

gem-agent reads the instruction files a repository already carries, in
this order per directory:

| File | Convention |
|---|---|
| `AGENTS.md` | the cross-vendor standard |
| `AGENT.md` | its singular variant |
| `CLAUDE.md` | Claude Code |
| `GEMINI.md` | Gemini CLI |

They are collected from `~/.config/gem-agent/` (your own defaults for
every project), then from ancestor directories outermost-first, then
from the project itself — so workspace-wide rules apply to sibling
repositories and the nearest file is read last, as the most specific.
Files with identical content are injected once. The startup banner lists
what was loaded.

The ancestor walk stops at your home directory: an instruction file is
obeyed as instructions, so gem-agent will not pick one up from a shared
location like `/tmp` that you do not own.

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

Start from the template in this repository:

```sh
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml
cp mcp.example.json    ~/.config/gem-agent/mcp.json   # optional, MCP servers
```

`~/.config/gem-agent/config.toml`:

```toml
[gcp]
project  = "your-project-id"
location = "global"        # default; Gemini 3 models are global-endpoint-only

[model]
name = "<gemini model id>"
# context_window = 1048576  # optional; exact window for the footer and compaction

[sandbox]
enabled = true             # default

[agent]
max_turns = 50             # default
shell_timeout_sec = 120    # default
auto_approve = false       # default; start sessions in auto-approve mode
auto_compact = true        # default; summarise older history near the window
compact_at_pct = 80        # default; share of the window that triggers it

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

### Content filters

Vertex applies content filters to both the request and the response. When
one fires, gem-agent says so explicitly — naming the reason the API
reported — rather than showing an empty answer, and **retries once**,
because the filter is not deterministic: what gets rated is the text that
attempt happened to generate, so the same request usually goes through on
the next try. Measured on ordinary security material (an incident-response
runbook in context): identical requests were blocked on some attempts and
passed on others, at every `[model].safety` setting.

If the retry is blocked too, the error says so; narrowing the request, or
`/clear` to drop large documents from the context, is what helps.

`[model].safety` adjusts the four configurable harm categories — useful
for `SAFETY`-category blocks, but note that `PROHIBITED_CONTENT` comes
from a filter these settings do not cover:

| Value | Effect |
|---|---|
| `default` | the provider's own thresholds |
| `relaxed` | block only high-confidence hits |
| `off` | do not block on those categories |

Loosening it is a deliberate choice, so the default is left alone.

Note: as of 2026-08, the Gemini 3 family (verified with gemini-3.7-flash and
gemini-3-flash-preview) is served only from the global endpoint — regional
locations return 404. Gemini 2.5 models work from regional endpoints such as
`us-central1`; set `location` accordingly if you use one.

## Documentation

[**docs/en/INDEX.md**](docs/en/INDEX.md) is the entry point
([日本語](docs/ja/INDEX.ja.md)) — one catalog rather than a list
maintained in several places. It covers:

- the [RFP](docs/en/gem-agent-rfp.md), which is the canonical spec
- [architecture](docs/en/reference/architecture.md) — current behaviour,
  the two confinement boundaries, failure behaviour in one table
- the [monthly drill](docs/en/reference/drill.md) — a backup that is not
  exercised is not a backup
- [promotion criteria](docs/en/reference/promotion.md) for leaving
  lab-series
- six ADRs, from the sandbox mechanism to context compaction

## License

MIT

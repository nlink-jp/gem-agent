# gem-agent

Interactive CLI agent runtime backed by Vertex AI Gemini 3.x — deliberately
minimal, sandboxed, and usable both interactively and headlessly.

> **Released.** Install with `brew install nlink-jp/tap/gem-agent`
> or from the [releases page](https://github.com/nlink-jp/gem-agent/releases)
> (Developer ID signed, Apple-notarized, macOS arm64) — the releases page
> is the authority on the current version, so this line does not repeat it.
> It is a cli-series tool: interface stability is a promise, and breaking
> changes go through the org's breaking-change process (ADR-0061).
> See the [RFP](docs/en/gem-agent-rfp.md) for the full specification.

[日本語版 README](README.ja.md)

## Why

gem-agent is an independent agent runtime: a minimal, auditable agent loop
(read / edit / shell / MCP / approval) on Vertex AI Gemini, defended by two
layers (sandbox-exec containment plus human-in-the-loop approval), with no
analysis or GUI subsystems. It is **drop-in** compatible with the project
ecosystem: it reads an existing project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` (and Claude Code-format skills) as-is, so one project setup
serves every runtime that works on it. It began life as a Claude Code
backup and was repositioned as an independent runtime once real-world use
outgrew that role
([ADR-0061](docs/en/adr/0061-independent-runtime-promotion.md)).

## Quickstart

```sh
brew install nlink-jp/tap/gem-agent
mkdir -p ~/.config/gem-agent
cp config.example.toml ~/.config/gem-agent/config.toml   # set [gcp].project and [model].name
```

```sh
cd /path/to/your/project
gem-agent                                  # interactive REPL
gem-agent "run the tests"                  # interactive, with this as turn 1
gem-agent -c                               # continue the last session here
gem-agent sessions                         # list resumable sessions
gem-agent trust                            # this project's trust state and trusted files
gem-agent -p "summarize this repository"   # one-shot, pipe-friendly
```

The current directory becomes the project: file tools cannot leave it,
sandboxed shell commands cannot write outside it, and mutating tool
calls ask for approval before running. Each session also gets a work
directory of its own for anything that is not part of the project —
intermediate data, an oversized tool result, a screenshot a server
returned — so the working copy stays clean. `/status` names it, and
nothing in it is ever deleted for you — `gem-agent workdirs` lists what
earlier sessions left behind, and `workdirs clean` removes it after
showing you exactly what and asking first. Requirements: macOS (Apple
Silicon), a Google Cloud project with Vertex AI enabled, and ADC
(`gcloud auth application-default login`) — details in
[configuration](docs/en/reference/configuration.md).

## What it does

Each line below is expanded in a focused document under
[docs/en/reference/](docs/en/INDEX.md); the ADRs hold the reasoning.

**[Interface](docs/en/reference/interface.md)** — an inline Bubble Tea
TUI with native scrollback, a bottom-pinned input box that stays live
during a running turn (Enter queues the next message), a live turn
status — stream heartbeat, stall warning, visible retries, and the
model's thought summaries streaming as it thinks — a Ctrl+C that
always comes back (file walks stop within one syscall, anything else
is abandoned after a bounded grace — ADR-0065), IME-friendly
approval dialogs,
Tab completion for `@`-paths, `/`-commands, and skill
names, `!command` shell escape, mermaid fences drawn in place in the
reply (flowchart / ASCII-label sequence / ER; anything the terminal
cannot draw faithfully stays source), thirteen slash commands (`/help`
`/tools` `/mcp` `/auto` `/compact` `/settings` `/usage` `/memory`
`/skills` `/skill` `/version` `/clear` `/quit`), a provenance-first `/settings`
panel, theme control, and a fully bilingual chrome
(`[tui].language = auto|ja|en`). A positional argument is the first
interactive turn — `gem-agent "…"` runs it and hands you the keyboard
(ADR-0064). Pipes fall back to a plain REPL;
`-p` runs one-shot (mutating tools denied; `--allow` grants named
tools per run, `--auto` arms the risk ladder — ADR-0053), and
`data | gem-agent -p "…"` attaches piped stdin as isolated data,
never as prompt text (ADR-0055). The pipe is read to EOF; if it is
still open after 2 s, a stderr line says so and names the remedy —
launch with `< /dev/null` when nothing is meant to be attached
(ADR-0067).

**[Built-in tools](docs/en/reference/tools.md)** — orientation
(`list_files`/`list_tree`/`search_files`, ignore-aware: dependency and
build directories and `.gitignore`'d content are skipped with every
skip reported), windowed reads and
summaries (`read_file`/`summarize_file`), delegated project search in
an isolated child context (`agentic_file_search`), atomic batched edits with
diagnosed misses (`edit_file`/`write_file`, with a shrink guard so a
whole-file rewrite cannot silently summarize a document away), file identification with
hashes (`file_info`), images and documents for the model
(`view_image`/`read_document`), a sandboxed shell whose read, write and
operator lanes the kernel enforces (`shell_exec`), a
deterministic clock/calendar (`datetime`), the model's own runtime
picture (`agent_info`), structured mid-turn choices (`ask_user`), and grounded web access
(`web_search`/`web_fetch`).

**[Attachments](docs/en/reference/attachments.md)** — `@file`,
`@dir/`, screenshots (`@~/Desktop/…`, `@clipboard`), PDFs and Office
documents, and audio/video — routed through your GCS bucket when
configured, inline otherwise.

**[Approval and safety](docs/en/reference/approval.md)** — per-call
MITL gates that show the model's own declared purpose for the call
alongside its arguments, a deny-with-reason answer (`N`) that carries
your one-line "do this instead" to the model inside the denial itself,
a session allowlist that never covers
Block-tier calls, an opt-in two-tier auto-approve (rules first, model
review second — edits to instruction and configuration files such as
`AGENTS.md` and `.mcp.json` always ask you, and a trusted project's
instruction and configuration files are pinned by content so a changed
one asks again before it is loaded), shell commands judged by
the Seatbelt lane they declare rather than by their text (a read-lane
command runs unasked; the write lane cannot touch `AGENTS.md` or
`.git/config`; the operator lane is yours alone), a runtime note when an
MCP server answers three calls in a row with the same error — the model
is told whose words the error is and to report to you rather than
investigate — a per-tool approval policy with scope-aware resolution and
trust-gated project loosening, a layered risk rulebook the auto-mode
reviewer reads — hand-written or drafted from your own recorded
answers, never skipping a gate — the Seatbelt sandbox, operator pre-tool
hooks that run the same guard scripts Claude Code does and refuse a
call before the ladder sees it, session-start and prompt-submit hooks
on the same contract whose output reaches the model as labelled data,
startup gates for
broad roots and first-seen projects, and nonce-tag isolation of all
tool output — which also keeps the request prefix byte-stable for
81–95% measured context-cache hits.

**[Sessions](docs/en/reference/sessions.md)** — full-fidelity JSONL
transcripts, per-project state layout, `--continue`/`--resume` (UUID session ids, resumable by prefix) with
deliberate refusals (wrong directory, wrong model), automatic context
compaction with an honest notice, the `/usage` token statement, and
approval-gated agent memory across sessions.

**[Integration](docs/en/reference/integration.md)** — drop-in reading
of `AGENTS.md`/`AGENT.md`/`CLAUDE.md`/`GEMINI.md` up the directory
tree, Claude Code-format `.mcp.json` MCP servers (global + project),
and Claude Code-format skills with progressive disclosure (a loaded
skill names its directory, as in Claude Code, so its own scripts run)
— both reloadable mid-session (`/mcp reload`, `/skills reload`), with
`--mcp on|off` to switch MCP per run.

**[Configuration](docs/en/reference/configuration.md)** — the full
config reference, precedence, CLI flags, content-filter behaviour,
endpoint notes, and opt-in audit logging to Cloud Logging (default) or
your OTLP collector (metadata only, never conversation content, and
nothing added to startup).

Out of scope by design: RAG or vector memory, data analysis, GUI,
non-macOS platforms.

## Supported platform

gem-agent is built, signed and tested for macOS on Apple Silicon only,
and the protections described above rest on macOS's kernel sandbox
(`sandbox-exec`). At every start gem-agent measures that sandbox with
real probes; the result decides how commands are treated:

- **verified** — the startup banner shows no sandbox warning: read-lane
  commands run unasked, write-lane commands ask, and the kernel bounds
  both.
- **unverified** — the banner says `sandbox unverified: …` or
  `sandbox: DISABLED`: every shell command asks you before it runs, and
  nothing else stands between the command and your machine. `/status`
  shows which state you are in.

A copy of the source rebuilt for another platform (Linux, WSL, Windows)
has no kernel sandbox behind it. If it starts without the warning above,
its sandbox check was altered and its behaviour is not the one this
README describes — treat it as unverified whatever it prints. Such builds
are not supported here; the tests under `internal/sandbox` are the bar
any port has to meet.

## Build

```sh
make build    # outputs dist/gem-agent
make test
```

## Documentation

[**docs/en/INDEX.md**](docs/en/INDEX.md) is the entry point
([日本語](docs/ja/INDEX.ja.md)) — one catalog rather than a list
maintained in several places. It covers the
[RFP](docs/en/gem-agent-rfp.md) (the canonical spec), the seven
feature references linked above,
[architecture](docs/en/reference/architecture.md), the
[monthly drill](docs/en/reference/drill.md),
[promotion criteria](docs/en/reference/promotion.md), and every ADR.

## License

MIT

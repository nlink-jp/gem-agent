# Changelog

## [0.1.0] - Unreleased

### Added — persistent footer (operator feedback)

- Footer line across all TUI phases: model name, context occupancy vs
  the model's input token limit (auto-detected via model metadata,
  overridable with `[model].context_window`), cumulative token
  consumption, and the ~-abbreviated project directory

### Fixed

- MCP kill-and-respawn race: a stale read loop no longer closes the
  successor incarnation's pending calls ("server exited during
  initialize" right after a timeout)

### Changed — TUI readability (operator feedback)

- Errors stand out: unknown /commands and turn failures render bold red
  with a `✗` marker instead of blending into dim meta text
- Theme-safe colors: accents use the ANSI-16 palette (follows the
  terminal theme); dim text (footer/hints) uses a background-aware
  256-palette mid-gray (245 on dark / 240 on light) — both the Faint
  attribute and ANSI color 8 render near-invisible on real themes
- New `[tui].theme` config: auto (default) / dark / light / plain —
  plain disables all styling for terminal themes that fight any colors

### Added — interactive TUI (ADR-0002)

- Bubble Tea inline TUI: completed conversation flushes to native
  scrollback; the managed live region carries streaming text, a
  spinner/status line, and the input box. Textarea input with
  paste-flagged newlines (a paste can never submit), Ctrl+J manual
  newline, ↑↓ history behind an explicit navigation-state flag, approval
  dialog (y/n/a) answering the gate over a channel, glamour Markdown
  rendering per segment (text/tool-event order preserved). Non-TTY use
  falls back to the plain line REPL; `-p` unchanged. Verified through a
  pty-driven E2E (expect) against live Vertex AI

### Added — development Phase 2 (Integration)

- Stdio MCP client with Claude Code `.mcp.json` compatibility (stdio
  entries, `${VAR}` expansion); tools register as `mcp__<server>__<tool>`,
  always approval-gated; kill-and-lazy-respawn on timeout (MCP has no
  cancel); verified live against tor-exit-lookup with gemini-3.7-flash
- mcp-guardian opt-in documented as a plain `.mcp.json` entry (guardian is
  itself a stdio MCP server — no gem-agent-side wiring needed)
- AGENTS.md / CLAUDE.md injection into the system prompt (32KB/file cap),
  verified live (injected rule observed in responses)
- Nonce-tag isolation of tool output via nlk/guard — fresh tag per LLM
  call, expanded into the system prompt and wrapped at send time
- One-shot mode `-p` (single turn, stdout answer, mutating tools denied
  with a visible reason, pipe-friendly)
- Exponential backoff (nlk/backoff) for 429/5xx stream starts, retrying
  only before any chunk is consumed

### Added — development Phase 1 (Core)

- Interactive REPL with a streaming Vertex AI Gemini agent loop; Gemini 3
  thought-signature capture/replay (ported from shell-agent-v2 ADR-0009,
  verified live against gemini-3-flash-preview via the global endpoint)
- Built-in tools (`list_files` / `read_file` / `write_file` / `edit_file` /
  `shell_exec`) with project-dir confinement including symlink-escape checks,
  output caps, and non-zero exit status / timeout surfacing
- MITL approval gate (y/n/a) with session-scoped allowlist, failing closed
- macOS sandbox-exec SBPL profile generation for shell_exec (deny file-write*
  outside project + scratch dirs; real Seatbelt enforcement test)
- Paste-safe REPL input (multi-line paste aggregates into one input)
- Strict-decode TOML config with flags > `GEMAGENT_*` > `GOOGLE_CLOUD_*` >
  file > defaults precedence
- JSONL session logging under `~/.local/state/gem-agent/sessions/`

### Fixed

- Backend errors are no longer misreported as "(interrupted)"
  (signal.NotifyContext stop() ordering bug, found in live smoke testing)

### Added — scaffold

- Project scaffold (CONVENTIONS.md Phase 2): Go module, cobra root command
  answering `--version` (with a pinning test), Makefile (`dist/` output,
  darwin-arm64 only, codesign/notarize wiring), org-standard `.gitignore`,
  MIT LICENSE, bilingual docs structure
- RFP (`docs/ja/gem-agent-rfp.ja.md` / `docs/en/gem-agent-rfp.md`)
- ADR-0001: sandbox mechanism — sandbox-exec + MITL two-layer defense

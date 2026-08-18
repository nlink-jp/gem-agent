# AGENTS.md — gem-agent

Interactive CLI agent backed by Vertex AI Gemini. Continuity (backup) tool
for when Claude Code is unavailable. macOS-only. Pre-release: development
Phase 1 (Core) implemented and verified live; Phase 2 (MCP, .mcp.json,
AGENTS.md injection, one-shot mode) pending.

- **Module:** `github.com/nlink-jp/gem-agent`
- **Series:** lab-series (promotion to cli-series considered after E2E + drill
  operations prove it — criteria to be written in development Phase 3)
- **Spec:** `docs/en/gem-agent-rfp.md` / `docs/ja/gem-agent-rfp.ja.md` (canonical)

## Build / test

| Task | Command |
|------|---------|
| Build | `make build` → `dist/gem-agent` (never `go build` directly) |
| Test | `make test` (or `go test ./...`) |
| Vet + test + build | `make check` |
| Release archive | `make package` → `dist/gem-agent-vX.Y.Z-darwin-arm64.zip` |

Version is injected via `-X main.version` from `git describe` — never edit the
`version` var default.

## Structure

```
main.go            entry point (package main, calls cmd.Execute(version))
cmd/               cobra root command, REPL loop, wiring, system prompt
internal/config/   strict-decode TOML + env/flag precedence
internal/llm/      Backend interface + Vertex AI impl (thought signatures)
internal/agent/    tool-calling loop, approval dispatch, history
internal/tools/    built-in tools, path confinement, ExecFunc injection
internal/sandbox/  SBPL profile generation, sandbox-exec wrapping
internal/approve/  MITL gate (y/n/a + session allowlist)
internal/session/  JSONL session logger
internal/repl/     paste-safe input reader
scripts/           codesign-darwin.sh / notarize-darwin.sh (org templates, verbatim)
docs/en/, docs/ja/ RFP, ADRs (en: no suffix; ja: .ja.md)
```

## Gotchas

- **macOS-only by design** — isolation is built on sandbox-exec (ADR-0001).
  Do not add linux/windows targets to the Makefile.
- **Gemini 3 thought signatures** — every response Part carries an opaque
  `ThoughtSignature` that must be echoed back on the next request; dropping it
  fails the second tool-call round with 400 INVALID_ARGUMENT.
- **`--version` must always answer** (pinned by `cmd/root_test.go`) — a future
  Homebrew formula's `brew test` runs it.
- **Drop-in compatibility is the point** — gem-agent reads the *target*
  project's AGENTS.md / CLAUDE.md / .mcp.json. Changes that require per-project
  setup in target repos defeat the tool's purpose.
- Config: `~/.config/gem-agent/config.toml`, org-standard schema
  (`[gcp]` project/location, `[model]` name), precedence
  flags > `GEMAGENT_*` > `GOOGLE_CLOUD_*` > file > defaults. Strict decode —
  unknown keys are errors.
- **The Gemini 3 family is global-endpoint-only** — `location = "global"`
  (the default). Regional endpoints 404 them (2026-08, verified live with
  gemini-3.7-flash and gemini-3-flash-preview); Gemini 2.5 works regionally.
- **stdout is model text only** — banner, prompts, tool events, and approval
  prompts go to stderr. Keep it that way; Phase 2's one-shot mode depends on it.
- **REPL and approval gate share ONE bufio.Reader** (bufio.NewReader returns
  an existing *bufio.Reader unchanged). Wrapping os.Stdin twice strands
  buffered input — don't "simplify" this.
- **signal.NotifyContext's stop() cancels the context** — capture ctx.Err()
  before stop() or every backend error reads as a user interrupt (regression
  test in cmd/turn_test.go).

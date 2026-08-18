# AGENTS.md — gem-agent

Interactive CLI agent backed by Vertex AI Gemini. Continuity (backup) tool
for when Claude Code is unavailable. macOS-only. Pre-release: development
Phases 1–2 implemented and verified live (agent loop, MCP client,
drop-in AGENTS.md/CLAUDE.md/.mcp.json, one-shot mode, nonce isolation,
backoff); Phase 3 (real-project E2E, drill runbook, release) pending.

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
internal/llm/      Backend interface + Vertex AI impl (thought signatures, backoff)
internal/agent/    tool-calling loop, approval dispatch, nonce wrapping, history
internal/tools/    built-in tools, path confinement, ExecFunc injection, Register
internal/mcp/      .mcp.json parsing + stdio JSON-RPC client (kill-and-respawn)
internal/sandbox/  SBPL profile generation, sandbox-exec wrapping
internal/approve/  MITL gate (y/n/a + session allowlist)
internal/session/  JSONL session logger
internal/repl/     paste-safe input reader (plain REPL, non-TTY fallback)
internal/tui/      Bubble Tea inline TUI (ADR-0002): model, approval gate
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
- **Never write from the MCP read loop** — a blocking write while the peer
  is not reading deadlocks both directions (internal/mcp refuses server
  requests from a goroutine; caught by the pipe-based tests).
- **nlk/guard tags are per-LLM-call** — history stores raw tool results and
  the agent wraps them at send time; storing wrapped results would freeze
  the tag and break the guard contract.
- **.mcp.json is a foreign format** — unknown keys are tolerated there
  (Claude Code owns it); strict decode applies to our own config.toml only.
- **TUI is inline, never alt-screen** (ADR-0002) — completed content goes
  through tea.Println into native scrollback; only the live region is
  managed. Switching to alt-screen would break scrollback/copy.
- **Rendering happens once per segment** — the live region shows raw
  streamed text; glamour renders at flush time (tool-call boundary or
  turn end). Rendering the live region per frame would duplicate work
  and flicker.
- **TUI E2E runs through a pty** — see the expect pattern in the Phase 2
  history (scratchpad tui_e2e.exp); piped stdin exercises the plain-REPL
  fallback, not the TUI.
- **No managed-view line may reach the terminal width** — a soft-wrapped
  line desyncs the inline renderer's height math and stale frames stack
  up (the resize staircase). View() clips every line to width-1; a
  genuine shrink additionally returns tea.ClearScreen once. Keep both
  when touching View().
- **Never query the terminal after Bubble Tea starts** — OSC queries
  (glamour WithAutoStyle, termenv/lipgloss HasDarkBackground) get their
  "rgb:..." reply delivered as phantom keystrokes once raw mode owns
  stdin. Theme is detected once in cmd before the program runs; renderer
  rebuilds go through a factory that never touches the terminal
  (TestResizeNeverQueriesTerminal). Note: expect-based pty E2E cannot
  catch this class — expect answers no OSC queries; only real terminals do.

# AGENTS.md — gem-agent

Interactive CLI agent backed by Vertex AI Gemini. Continuity (backup) tool
for when Claude Code is unavailable. macOS-only. Released (v0.1.x, Homebrew
tap + notarized zip): agent loop, MCP client, drop-in
AGENTS.md/CLAUDE.md/.mcp.json, one-shot mode, nonce isolation, backoff,
inline TUI, auto-approve, session resume, context compaction — all verified
live. Outstanding: monthly drill runbook and written cli-series promotion
criteria (RFP Phase 3).

- **Module:** `github.com/nlink-jp/gem-agent`
- **Series:** lab-series. Promotion criteria are written down in
  `docs/en/reference/promotion.md`; the count restarts if the sandbox ever
  fails a drill.
- **Spec:** `docs/en/gem-agent-rfp.md` / `docs/ja/gem-agent-rfp.ja.md` (canonical)
- **Docs entry point:** `docs/en/INDEX.md` / `docs/ja/INDEX.ja.md` — three
  tiers (reference / adr / history). Add a doc to the INDEX, not to a
  parallel list in this file or the README.

## Build / test

| Task | Command |
|------|---------|
| Build | `make build` → `dist/gem-agent` (never `go build` directly) |
| Test | `make test` (or `go test ./...`) |
| Vet + test + docs mirror + build | `make check` |
| Docs mirror only | `make docs-check` |
| Release archive | `make package` → `dist/gem-agent-vX.Y.Z-darwin-arm64.zip` |

Version is injected via `-X main.version` from `git describe` — never edit the
`version` var default.

## Structure

```
config.example.toml  shipped config template (pinned by a loader test)
gem-agent.example.project.toml  shipped <project>/.gem-agent.toml template
mcp.example.json     shipped MCP server template (pinned by a loader test)
main.go            entry point (package main, calls cmd.Execute(version))
cmd/               cobra root command, REPL loop, wiring, system prompt
internal/config/   strict-decode TOML + env/flag precedence
internal/llm/      Backend interface + Vertex AI impl (thought signatures, backoff)
internal/agent/    tool-calling loop, approval dispatch, nonce wrapping, history,
                   compaction (compact.go, ADR-0006)
internal/tools/    built-in tools, path confinement, ExecFunc injection, Register
internal/mcp/      .mcp.json parsing + stdio JSON-RPC client (kill-and-respawn)
internal/risk/     rule tier of the auto-approve ladder (pure, no model)
internal/policy/   per-tool approval policy (ADR-0008), pure resolver
internal/skills/   Claude Code skill discovery/loading (ADR-0010)
internal/memory/   agent memory across sessions (ADR-0020): two scopes, budgeted injection
internal/docext/   stdlib-only Office XML text extraction (ADR-0026): docx/xlsx/pptx
internal/mediastore/ GCS media uploads (ADR-0027): content-addressed, quota project pinned
internal/statedir/ shared per-project state convention (ADR-0022): root+env override, escape, .project marker
cmd/settings.go    /settings panel content + edits (ADR-0009)
internal/mention/  @-reference parsing, project-confined resolution, completion
internal/instructions/ AGENTS.md / AGENT.md / CLAUDE.md / GEMINI.md discovery
                   (ancestor walk, stops at $HOME)
internal/sandbox/  SBPL profile generation, sandbox-exec wrapping
internal/approve/  MITL gate (y/n/a + session allowlist)
internal/session/  JSONL transcript: logger + resume loader (ADR-0005)
internal/repl/     paste-safe input reader (plain REPL, non-TTY fallback)
internal/tui/      Bubble Tea inline TUI (ADR-0002): model, approval gate
scripts/           codesign-darwin.sh / notarize-darwin.sh (org templates, verbatim)
docs/en/, docs/ja/ INDEX + reference/ + adr/ (en: no suffix; ja: .ja.md)
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
- **A new config key means updating `config.example.toml`** — strict
  decode makes a stale template a hard startup error for users, so the
  loader tests parse the shipped templates and compare their values
  against the built-in defaults. Same for `mcp.example.json`.
- **Modifier+Enter is not a key you can rely on** — Shift+Enter always,
  and Option+Enter unless the terminal sends Meta, arrive as a plain CR
  that is byte-identical to submit. Any "insert newline" affordance must
  have a route that is a distinct key (Ctrl+J) or pure text (trailing
  backslash). Bubble Tea v1 does not decode the kitty/CSI-u protocols
  that would disambiguate them.
- **Every interactive answer needs an IME-free route** — with a Japanese
  IME on, letter keys are swallowed by composition; arrows, Tab, Enter,
  and Esc are not. Any new prompt must be answerable without typing
  letters (the approval dialog's selection model), and the selection must
  be marked with a glyph, not color alone.
- **Auto-approve fails closed, and Block is a floor** (ADR-0004) — when
  changing the ladder, keep: Block never consults the model, model errors
  and malformed verdicts escalate, and confidence alone never approves.
  New dangerous patterns go in `internal/risk`, with a corpus test.
- **nlk/guard tags are per-LLM-call** — history stores raw tool results and
  the agent wraps them at send time; storing wrapped results would freeze
  the tag and break the guard contract.
- **.mcp.json is a foreign format** — unknown keys are tolerated there
  (Claude Code owns it); strict decode applies to our own config.toml only.
- **TUI is inline, never alt-screen** (ADR-0002) — completed content goes
  through tea.Println into native scrollback; only the live region is
  managed. Switching to alt-screen would break scrollback/copy.
- **Two Println commands need tea.Sequence, never tea.Batch** — Batch
  runs commands concurrently, so their output lands in arbitrary order
  (measured: a `⚠` note printed 32 bytes *before* the `📎` line it was
  meant to follow). **Unit tests cannot catch this**: the test printer
  records at command-construction time, so it always sees the intended
  order. Only a pty run reveals it — check byte offsets, not eyeballs.
- **Rendering happens once per segment** — the live region shows raw
  streamed text; glamour renders at flush time (tool-call boundary or
  turn end). Rendering the live region per frame would duplicate work
  and flicker.
- **TUI E2E runs through a pty, and the pty needs an explicit size** —
  `set stty_init "rows 40 columns 120"` before `spawn`. Without it the
  terminal can report 0×0, the input box renders nothing, and every
  typing assertion fails in a way that looks like an app bug. (The app
  now floors the size, but the harness should still be honest.) Piped
  stdin exercises the plain-REPL fallback, not the TUI.
- **No managed view may be taller than the terminal, either** — the same
  failure on the other axis. A view of `height` lines or more scrolls the
  screen, and the inline renderer's line accounting drifts by exactly the
  overflow: closing the settings panel left the input block one row up.
  The bottom-pinning math reserves one line, so the invariant is
  `view lines <= height-1`, and `TestSettingsViewNeverExceedsTheTerminal`
  pins it across sizes and cursor positions. Budget a scrolling list
  against the *real* chrome (headers, "… more" markers, the footer, the
  trailing newline) — a guessed margin was 2-3 lines short — and count a
  window's cost exactly: the first rendered row always prints its section
  heading even when it shares one with the row above.
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
- **The transcript is the resume format** (ADR-0005) — `llm.Message`'s JSON
  tags are a persisted schema, not decoration, and every history append
  goes through `appendMessage` so the conversation and the transcript
  cannot drift. Clipping a `message` record would silently amputate a
  resumed session; clip diagnostic records instead. Bump
  `session.SchemaVersion` on a breaking change.
- **Resume replays thought signatures across processes** — verified live
  (2026-08-19). That is why resume refuses a different model and a
  different project directory rather than warning: the failure would be a
  400 after the operator believed they were back at work.
- **A compaction cut must never land on a tool result** (ADR-0006) —
  Gemini requires every function call to be paired with its response in
  one request. Cutting only at user messages looks safer but cannot
  compact a long agent loop, which contains exactly one user message.
- **Compaction fails safe** — on any summariser error the history is left
  untouched and the turn continues; auto-compaction switches itself off
  after two failures rather than paying for a failing call every round.
- **Auto-compaction needs the context window, and the window resolves
  asynchronously** — `resolveWindow` must run in *every* mode. It
  originally ran only on the interactive paths, so one-shot never knew
  the window and compaction silently never fired (measured, not reasoned).
- **A new docs file needs its mirror in the same commit** — `make check`
  runs `scripts/docs-mirror-check.sh`, because a missing translation is
  invisible in review: it looks exactly like a document nobody has
  written yet.
- **The drill runbook is executable, and its steps were wrong until they
  were run.** Two of them tested nothing (a question the injected
  instruction files already answered; a containment check the model could
  satisfy by politely declining). When editing
  `docs/*/reference/drill*.md`, run the step you changed.
- **Keys during a running turn are not "ignored"** (ADR-0007) — the input
  box stays live, Enter queues one message, and it auto-sends only when
  the turn finished cleanly; on error or interrupt it is handed back
  unsent. Ctrl+C stays the unconditional interrupt while running, and the
  approval dialog still owns every key while it is open.
- **`session.ShellContextPrefix` is shared on purpose** — the `!` shell
  context message is injected as a user-role message, and the session
  listing needs to tell it from something the operator typed. Sniffing
  for the sentence in two places would drift.
- **A project file may tighten the gate, never loosen it unless trusted**
  (ADR-0008) — `<project>/.gem-agent.toml` carries `[approval.tools]` and
  nothing else, and a `"never"` entry is dropped (loudly) unless the
  project path is in `[approval].trusted_projects`. A checked-out
  repository must not be able to disarm the approval gate. When touching
  `internal/policy`, keep that direction rule and the bare-`"*"`
  rejection; both have tests that state why.
- **`"never"` never lifts the rule tier's Block floor** — otherwise
  `shell_exec = "never"` would mean "run anything unattended". The model
  tier *is* skipped under both `never` and `always`: the operator has
  already decided, and paying for a model round could answer it
  differently.
- **The settings panel never writes `config.toml`** (ADR-0009) — the TOML
  encoder does not preserve comments, and that file is hand-written with
  71 lines of them. Persisted policy goes to the machine-owned
  `policy.toml`, which wins collisions so a UI change is never silently
  overridden, and every row shows which file decided it.
- **`cmd.settingsStore` owns the merge; `internal/tui` only renders.**
  Apply returns the refreshed data, so the panel shows what was stored
  rather than what the keypress asked for, and `SetPolicy` hands the
  result to the running agent in the same step — panel and agent cannot
  disagree.
- **Anything the agent reads per tool call and the UI writes needs the
  mutex** — policy, auto-approve, auto-compact all crossed that line when
  the panel arrived (`go test -race` covers it).
- **`load_skill` is the only tool whose results skip the nonce wrap**
  (ADR-0010) — skill bodies are operator-installed instructions, same
  trust tier as AGENTS.md. The exemption is safe only because
  `Skill.Body`/`Skill.File` confine reads to discovered skill
  directories (symlinks resolved and re-checked). Any change that widens
  where `load_skill` can read, or adds another tool to
  `agent.Options.InstructionTools`, must revisit ADR-0010 first.
- **`SKILL.md` frontmatter is someone else's schema** — parse the keys
  we use, ignore the rest (`allowed-tools` deliberately so: honouring a
  foreign permission grant would bypass ADR-0004/0008). Never write to
  skill directories.
- **The session allowlist sits below the Block floor (ADR-0021)** —
  `Approver.Approve` carries `mustPrompt`, set by the agent for
  Block-tier calls and always-policy tools; both gates skip their
  allowlist when it is set. Removing that flag, or consulting the
  allowlist before the risk verdict, reopens the measured hole where
  one 'a' on a benign call waved every later `sudo`/`rm -rf` through.
- **The transcript loader is line-based and tolerant (ADR-0021)** —
  corrupt lines are skipped with a reported count, never treated as
  EOF; a compaction record after skipped lines refuses; `/clear` is a
  recorded event replayed on load. Every history mutation needs a
  matching record kind — Reset was the one that did not, and cleared
  conversations resurrected.
- **Policy resolves scope before specificity (ADR-0021)** — project
  rules beat global rules for the tools they match; sorting one merged
  list by specificity broke "a project may tighten freely".
- **The first-run trust gate covers everything a project provides
  (ADR-0023)** — instruction files, `.mcp.json`, and `.claude/skills`
  load only for trusted projects (persisted in policy.toml `trust`).
  Adding a new project-provided input channel means adding it to
  `probeProject` and gating its load — a channel outside the gate is
  the false-comfort hole the ADR exists to close. Broad roots (/, home,
  ancestors of home) confirm interactively and refuse non-interactively.
- **Sessions are per-project; legacy flat files are read in place
  (ADR-0022)** — Open/Reopen/Find take projectDir, new transcripts go
  under `sessions/projects/<escaped>/`, and flat pre-0.18 files must
  keep listing and resuming where they are. Do not add migration code
  that moves them without revisiting ADR-0022 (the operator explicitly
  chose zero file motion). E2E and drills set `GEMAGENT_STATE_DIR` to a
  scratch tree — never run destructive tests against the real state
  root.
- **Memory writes are the trust boundary (ADR-0020)** — `save_memory` /
  `delete_memory` stay Mutating and `risk.Classify` keeps them at
  Review, never Safe: a persisted memory reappears in every later
  session's prompt, so it is a persistence vector for injected
  instructions. The injected section is framed as agent-recorded
  background, not instructions; do not promote it to the AGENTS.md trust
  tier. Memory lives under `~/.local/state/gem-agent/memory/` — never in
  the project tree, and the lossy path-escape is guarded by the
  `.project` marker (a mismatch skips the directory; misattribution
  would be worse than not loading).

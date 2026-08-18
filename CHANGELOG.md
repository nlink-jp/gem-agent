# Changelog

## [Unreleased]

### Added — `/settings` (ADR-0009, operator request)

- Configuration resolves through four layers — flags, `GEMAGENT_*`,
  `GOOGLE_CLOUD_*`, the file, defaults — and **none of it was visible
  from inside a session**. `/settings` opens a panel where every row
  carries its value *and where that value came from*
- Editable: the approval policy per tool, auto-approve, auto-compaction,
  and the theme. Settings that cannot change mid-session (model, GCP
  project, the sandbox switch) are shown read-only and say why, because a
  menu that offers to change something it cannot is worse than a
  read-only row
- ↑↓ moves, ←→/Enter changes, `s` switches whether a policy change saves
  globally or for this project only, Esc closes. Non-TTY prints the same
  rows read-only
- **Persisted changes go to `~/.config/gem-agent/policy.toml`**, a
  machine-owned file that announces itself in its first line. The
  hand-written `config.toml` is never rewritten, so its comments survive
  — the TOML encoder does not preserve them, and the shipped template is
  71 lines of explanation. `policy.toml` wins a collision, so a UI change
  is never silently overridden, and each row names the file that decided
  it — including `(ignored: untrusted)` for a project entry that was
  dropped
- Project-scoped policy is written **inside** the machine-owned file
  (`[projects."<path>".tools]`), not into the project: adding a file to
  somebody's repository is a surprising side effect of a keypress, and a
  loosening entry there would be inert anyway unless the project were
  trusted
- **The approval dialog gained a fourth answer, 今後聞かない (`p`)** —
  allow this call and never ask about that tool again. It writes the
  policy and reports what it wrote. Deliberately separate from `a`, which
  lasts one session: one is a convenience, the other edits a file on disk

## [0.4.0] - 2026-08-19

### Added — per-tool approval policy (ADR-0008, operator request)

- Every MCP tool asked for approval on **every call**, because gem-agent
  cannot know what a server's tool does. The operator does.
  `[approval.tools]` now maps a tool name — or a `mcp__server__*` prefix —
  to `"always"` or `"never"`:
  - `"never"` skips the gate in every mode, manual included. It is not
    "run anything": for a tool whose effect varies per call
    (`shell_exec`), the rule tier's blocked patterns still ask, and the
    model tier is skipped because the operator already decided
  - `"always"` gates in every mode — an operator-set floor that
    auto-approve cannot lift, the counterpart of ADR-0004's Block tier
  - Exact names beat wildcards, longer wildcards beat shorter ones, and a
    bare `"*"` is a config error: switching off every gate at once must
    not be reachable by a one-character entry
- **Project scope**, in `<project>/.gem-agent.toml` — a file that carries
  policy and nothing else, so a repository cannot reach the model,
  credentials, or the sandbox switch. Direction is asymmetric on purpose:
  a project may **tighten** anywhere, and may **loosen** only where its
  path is listed in `[approval].trusted_projects` in the operator's own
  config. A checked-out repository must not be able to disarm the gate by
  existing. Ignored entries are named at startup, with the line to add
- One consequence: `-p` one-shot mode denies mutating tools because
  nothing can answer a prompt, but a `"never"` tool was never going to
  ask, so it runs — a read-only MCP lookup is now usable in a pipeline
- `gem-agent.example.project.toml` ships alongside `config.example.toml`,
  both pinned against the loader by tests

## [0.3.0] - 2026-08-19

### Added — input stays live during a turn (ADR-0007, first drill finding)

- Typing while a turn ran used to be **discarded silently** — the TUI
  accepted only Ctrl+C and shift+tab, so a follow-up typed while output
  scrolled past simply never appeared, and had to be retyped. This is not
  a rare state: an agent turn runs for tens of seconds, which is exactly
  when the next instruction occurs to you, often because of what you are
  watching
- The input box now stays live, and **Enter queues** the message rather
  than sending it: the agent loop owns the conversation until it returns,
  and splicing a user message into a half-finished tool round would break
  the call/response pairing Gemini requires. A second Enter appends, so
  nothing typed is dropped
- **The queued message is auto-sent only when the turn finished cleanly.**
  On an error or an interrupt it is handed back to the input box unsent,
  with a note — a message written during a turn that then failed was
  written against a world that no longer exists, and firing it into a
  broken state is the surprise that makes queueing untrustworthy
- Ctrl+C stays the unconditional interrupt while running, and the
  approval dialog still owns every key while it is open

### Fixed

- A session whose only input was a `!` command listed in
  `gem-agent sessions` as `I ran this shell command myself:` — the wrapper
  sentence the agent injects around the command. It now shows the command
  itself, and a message the operator actually typed always wins the
  preview, whatever the order

### Added — development Phase 3 (Release), the last of the RFP plan

- **[Monthly drill runbook](docs/en/reference/drill.md)** — a backup that
  is not exercised is not a backup. Twenty minutes, seven steps, each one
  tied to something that decays without anyone touching gem-agent
  (credentials expiring, a model retiring, an OS update invalidating the
  binary, an MCP server moving). The verdict is pass or issue; there is no
  "mostly worked", and a skipped step is recorded as skipped
- **[Promotion criteria](docs/en/reference/promotion.md)** for leaving
  lab-series — seven checkable facts rather than a judgement call, none of
  them counting features. A sandbox failure in any drill resets the count.
  Current status is stated in the document and is *not met*
- **[Architecture reference](docs/en/reference/architecture.md)** —
  current behaviour written to be read cold: the turn loop, the two
  confinement boundaries and how they fail differently, persistence, and
  every failure mode in one table
- **Three-tier docs structure** with a single catalog
  ([`INDEX.md`](docs/en/INDEX.md) / `INDEX.ja.md`): `reference/` for
  current behaviour, `adr/` for decisions, `history/` for what gets
  superseded. README and AGENTS.md now point at the index instead of
  maintaining parallel lists that drift
- `scripts/docs-mirror-check.sh`, wired into `make check` as
  `make docs-check`: en/ja must be full structural mirrors. A missing
  translation is invisible in review — it looks exactly like a document
  nobody has written yet

### Fixed — in the runbook, by running it

The first drill rewrote three of its own steps, which is the reason to run
a runbook rather than review it:

- The read-only step asked what the project does and what its build
  commands are — answered correctly with **zero tool calls**, because the
  injected `AGENTS.md` already says so. It proved nothing. Now it asks
  something no instruction file can answer
- Auto-approve was on by config, so most of the approval step would have
  run unattended. Step 1 now checks the indicator and turns it off
- The containment check asked the model to write outside the project. The
  model read the confinement out of the system prompt and declined to try
  — a pass that tests nothing — while an earlier run of the same request
  did try and hit the gate first. Replaced with a direct `!echo >
  ../file`, which has no model discretion in it

## [0.2.1] - 2026-08-19

### Fixed

- `--continue` failed with "has no conversation to resume" when the most
  recent transcript held only a header. Such a file is easy to make —
  start gem-agent, run `/help`, quit — and being the newest it shadowed
  the real session `--continue` exists to find. Conversation-less
  transcripts are now left out of the listing and of `--continue`; naming
  one explicitly with `--resume <id>` still reports what is actually
  wrong with it. Found by running the released build, not by reading it

## [0.2.0] - 2026-08-19

Both features were out of scope in the RFP; use argued otherwise, so each
arrives with an ADR rather than being quietly added.

### Added — session resume (ADR-0005, operator request)

- `--continue` resumes this project's most recent session, `--resume <id>`
  a specific one, and `gem-agent sessions` lists them with age, model and
  opening question. A resumed session appends to its own transcript: one
  file is one conversation, however many processes it took
- The JSONL session log became the resume source of truth, which meant
  making it lossless. Conversation records now hold the complete message
  — tool-call arguments, attachments and **Gemini thought signatures**,
  which were not recorded at all and which the API requires on replay.
  Tool results are no longer clipped to 2000 characters: a resumed
  session with half of a file it read is worse than no resume, because
  nothing announces the gap. Diagnostic records stay summarised
- Verified live before shipping, since the whole design rests on it:
  signatures recorded by one process replay in another (a resumed run
  answered from a restored tool result without re-reading anything)
- Two refusals rather than warnings — a session resumes only in the
  directory it was recorded in, and only under the model that produced
  it. Both errors name the recorded value, so the way forward is a
  copy-paste. Ids are validated as ids, never interpreted as paths
- **The transcript now holds the full text of every file the agent read.**
  It always half-did; now it does so completely. `0600`, under
  `~/.local/state/gem-agent/sessions/`

### Added — context compaction (ADR-0006, operator request)

- Nearing the model's window, the older half of the conversation is
  replaced by a summary of it and the recent half is kept verbatim,
  instead of a turn failing with `/clear` as the only recovery.
  Automatic at `[agent].compact_at_pct` (default 80) between rounds —
  where a long tool loop actually runs out of room — and `/compact` on
  demand
- The summariser is offered no tools and receives the transcript
  nonce-wrapped as untrusted data; the summary comes back into the
  conversation as an attachment, quoted as data, because it is
  model-generated text derived from tool output — facts to rely on, not
  new instructions
- Fails safe: any summariser error, filter block or empty answer leaves
  the history exactly as it was and the turn continues. Two failures
  switch automatic compaction off for the session rather than paying for
  a failing call every round
- Each compaction is recorded, so a resumed session comes back compacted
  instead of re-inflating to the size it was deliberately shrunk from
- The cut never lands on a tool result (Gemini pairs every function call
  with its response in one request). Cutting only at user messages was
  the first rule written and was withdrawn during verification: one long
  agent loop contains exactly one user message, at the beginning, so that
  rule could never compact the case the feature exists for

### Fixed

- The context-window lookup now runs in every mode. It ran only on the
  interactive paths, so a one-shot session never learned its window and
  auto-compaction silently never fired — found by measuring a one-shot
  run that reached 17k tokens against a 12k window without compacting

## [0.1.3] - 2026-08-19

### Fixed

- A content-filter block now retries once instead of ending the turn.
  Measured after a report that v0.1.2 still failed with
  `safety = "relaxed"` set: the same request, re-sent, was blocked on
  some attempts and passed on others at **every** safety setting — the
  filter rates the text each attempt happens to generate, so it is not
  deterministic. One retry (never more) turns the common case into a
  completed turn; the retry is announced, and a second block is reported
- v0.1.2's advice was wrong and is corrected: `PROHIBITED_CONTENT` comes
  from a filter that `[model].safety` does not cover, so the message no
  longer tells the operator to change a setting that cannot help. It now
  says what actually works — re-send, narrow the request, or `/clear` to
  drop large documents from the context

## [0.1.2] - 2026-08-19

### Fixed

- A blocked request now says so. v0.1.1 reported "the model returned an
  empty response" for every empty turn, which hid the actual cause: the
  provider's content filter had rejected the conversation
  (`PROHIBITED_CONTENT`), reproduced live with an incident-response
  runbook in context. The finish/block reason is now captured and the
  error names it — filter block, output limit reached while reasoning,
  safety stop, or genuinely empty — with the remedy for each

### Added

- `[model].safety` (`default` / `relaxed` / `off`) adjusts the four
  configurable harm-category thresholds. Security material routinely
  trips the dangerous-content filter; `off` was verified to unblock a
  request that `default` rejected. The default is unchanged — loosening
  a content filter is the operator's decision, and the block message
  points at this setting when it fires
- Empty-turn diagnostics (finish reason, block reason, reasoning tokens,
  usage) are written to the session log

## [0.1.1] - 2026-08-19

### Fixed

- A model response with neither text nor tool calls was stored in the
  conversation, and every later request then carried an empty part —
  Vertex rejects that with `parts[0].data: required oneof field 'data'
  must have one initialized field` (400), so once it happened the
  session failed on every subsequent message until `/clear`. Such a
  response is now reported as an error and never recorded, and the
  request builder drops empty messages as a second line of defence

## [0.1.0] - 2026-08-19

### Added — full instruction-file conventions (operator feedback)

- Instruction files now cover the ecosystem conventions — `AGENTS.md`,
  `AGENT.md`, `CLAUDE.md`, `GEMINI.md` (the native one for a Gemini
  agent) — and are searched up through ancestor directories the way
  other agents do, so a workspace-wide `CLAUDE.md` applies to every
  repository beneath it. `~/.config/gem-agent/` supplies personal
  defaults, duplicates by content are injected once, and the banner
  lists what was loaded
- The ancestor walk stops at `$HOME`: instruction files are obeyed as
  instructions, so one planted in a shared location is never picked up

### Fixed

- Scrollback order could scramble: two Println commands returned in one
  tea.Batch execute concurrently, so a note could print before the line
  it followed (measured in a pty). Ordered emissions now use tea.Sequence
- `@`-references directly after Japanese punctuation ("…直して。@src/main.go")
  were not recognised — the parser required a space or bracket before
  the `@`; it now rejects only genuinely mid-word cases (email
  addresses, module paths)
- A terminal reporting no size (a failed ioctl, some pty harnesses) gave
  the input box a negative width, so nothing typed was drawn; sizes are
  now floored at 20×4

### Added — shipped config templates (operator feedback)

- `config.example.toml` (every key, its default, and why it exists) and
  `mcp.example.json` (both scopes, stdio, `${VAR}` expansion), following
  the org convention. Loader tests parse both and compare the template
  values against the built-in defaults, so a drifted template fails in
  CI-less development rather than in a user's hands; a second test keeps
  environment-specific values and credential-shaped tokens out

### Added — @-references to files and directories (operator feedback)

- `@path` in a message attaches that project file (or a directory
  listing) to the turn, with Tab completion in the input box (common
  prefix, then a candidate list). Resolution is confined to the project
  including symlinks; failures are reported, never silently dropped
- Attached content is nonce-wrapped as untrusted data like tool output:
  the operator chose the file, but not what is inside it. The typed text
  itself is sent unmodified, and the raw attachment stays in history
  (the isolation tag is regenerated per LLM call)

### Changed — auto-approve toggles mid-run (operator feedback)

- shift+tab now works while a turn is running, not only at the prompt:
  a long agent loop started in manual mode no longer forces an approval
  for every remaining step. The agent reads the flag per tool call, so
  the switch lands on the next one; the status indicator updates at
  once and the notice is printed after the streamed text it followed

### Added — discoverable multi-line input (operator feedback)

- Newlines can be entered with `Ctrl+J` or a trailing `\` before `Enter`
  — both always available. `Option`/`Alt`+`Enter` also works, but only
  where the terminal sends Meta for Option (macOS defaults do not; docs
  give the Terminal.app and iTerm2 settings). Shift+Enter is impossible:
  terminals send it as a plain CR, indistinguishable from submit. The
  input placeholder teaches the keys and `/help` lists every route — the
  always-on hint line was removed earlier, leaving them undiscoverable

### Added — IME-safe approval dialog (operator feedback)

- The approval dialog is now selectable: ←→/Tab move, Enter confirms,
  Esc denies, and the `y`/`n`/`a` shortcuts still work. A Japanese IME
  swallows those letters into composition, so an IME-free route was
  required; arrows, Tab, and Enter reach the app untouched. The
  highlight (marked `▶`, not color-only) starts on *allow*, or on *deny*
  when auto-approve escalated the call

### Changed — escalation reasons are shown (operator feedback)

- When auto-approve asks instead of running, the approval dialog now
  carries a marked `⚠` line naming the tier that objected and why
  ("blocked by rule (always asks): …" vs "escalated by risk review: …")
  instead of appending it to the dim argument summary. The Approver
  interface gained a `reason` parameter so the TUI and the plain REPL
  both render it distinctly; ordinary (non-auto) prompts carry none

### Added — global MCP scope (operator feedback)

- MCP servers now load from two scopes: `~/.config/gem-agent/mcp.json`
  (global, every project) and `<project>/.mcp.json` (project). Both use
  the Claude Code format, are merged, and the project entry wins a name
  collision (announced as a note). Banner and `/mcp` label each server
  with its scope. Verified live with two org lookup servers

### Added — auto-approve mode (ADR-0004, operator feedback)

- Opt-in auto-approve with a two-tier review of every mutating call:
  a pure rule classifier (Safe / Review / Block) followed, for Review
  only, by a model risk evaluation. Auto-approval requires approve AND
  confidence ≥ 0.8; Block is a deterministic floor the model cannot
  override; model errors, malformed verdicts, and unknown tools all
  escalate to the human gate carrying the reason
- The evaluator sees the proposed call as nonce-wrapped untrusted data
  and is offered no tools; sandbox and MITL backstop apply in all modes
- `[agent].auto_approve` config default (false), shift+tab and `/auto`
  toggles, `⚡auto` status indicator, auto-approved calls printed with
  tier and reason. Verified live: an in-project file write auto-ran,
  `rm -rf /` escalated

### Added — direct shell mode (operator feedback)

- `!<command>` runs a shell command directly: sandboxed through the
  same shell_exec path (timeout, output cap, exit-status surfacing) but
  with no approval prompt (the user typed it). Command and output are
  injected into the model's context, verified live (the model recalls
  the output on the next turn). Works in the TUI and the plain REPL

### Added — bottom-pinned layout (ADR-0003, operator feedback)

- The input box and status line pin to the window bottom like current
  Claude Code, still without alt-screen: the TUI clears the screen once
  at startup, prints the banner through a physical-line counter, and
  pads the view top by the remaining height. Padding floors at zero
  when the conversation fills the screen; shrink-clears reset the count

### Changed — single status line (operator feedback)

- The always-on key-hint line is gone; key bindings moved into /help.
  The input block is now "input box + one status line", matching the
  status-bar reading of the footer

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

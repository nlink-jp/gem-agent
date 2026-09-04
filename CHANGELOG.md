# Changelog

## [0.65.2] - 2026-09-05

### Added — the session id is exported to children (ADR-0069 addendum 2)

- `GEMAGENT_SESSION_ID` joins `GEMAGENT_WORK_DIR` in the environment of
  everything the session spawns, exported before any MCP server starts.
  `${GEMAGENT_SESSION_ID}` in an `mcp.json` args entry expands to it, so
  a server that keeps per-session state (agent-board) is told its
  session on its registration line

## [0.65.1] - 2026-09-04

### Changed — the PreToolUse hook payload carries the session (ADR-0069 addendum)

- `[[hooks.pre_tool_use]]` commands now receive `session_id` and
  `transcript_path` beside `tool_name` / `tool_input` / `cwd` (empty
  when the session log is disabled). A hook that keeps per-session
  state — agent-board's claims — can tie a call to its session; the
  org guard, which reads only `tool_input.command`, is unaffected

## [0.65.0] - 2026-09-04

### Added — a loaded skill names its directory (ADR-0070 §1)

- The `load_skill(name)` result and the `/skill <name>` turn open with
  Claude Code's own line `Base directory for this skill: <dir>` — the
  symlink-resolved skill directory, the same boundary reads are
  confined to. A `SKILL.md` written to Claude Code's contract
  ("`SKILL_DIR` is the directory containing this SKILL.md", then
  `python3 SKILL_DIR/scripts/…`) now runs its scripts from gem-agent's
  global skill directory as well as from a project. Before, nothing
  told the model where a skill lived; in session `20260904-225330` it
  went looking with `find / -name validate.py`, a walk of every mount
  that only an accident of the redirect rule put in front of the
  operator. The tool description says the line is coming; the
  system-prompt line is unchanged (progressive disclosure holds)

### Changed — a read-only walk outside the roots is Review, not Safe (ADR-0070 §3)

- `find`, `fd`, `du`, `rg`, and `grep` with a recursive flag, starting
  at `/`, `~`, or an absolute path outside the project, the session
  work directory and the sandbox's scratch roots, now land in the
  model tier with the reason "walks the filesystem outside the project
  and session work directories". The sandbox denies writes only, so
  such a walk reaches every mount; that cost is the model tier's to
  weigh. Relative starting points and single reads (`cat /etc/hosts`,
  `grep TODO /etc/hosts`) stay Safe; manual mode is unaffected
  (everything already asks); Block stays the floor for the irreversible

### Fixed — the rule tier's writable places are the sandbox's (ADR-0070 §2)

- `2>/dev/null` was Blocked as "redirects output outside the project
  and session work directories" although the profile has always
  allowed writes under `/dev` — the sandbox and the rule tier each kept
  their own list. `sandbox.ScratchDirs()` (`TMPDIR`, `/private/tmp`,
  `/dev`) is now the one list both read: a redirect into a scratch
  root is not "outside", and `/dev/null` / `2>&1` redirects no longer
  cost a read-only command its Safe verdict. Nothing that can run is
  widened; the classification catches up with Seatbelt

## [0.64.0] - 2026-09-04

### Added — session-start and prompt-submit hooks (ADR-0069)

- `[[hooks.session_start]]` runs when a session starts (`source`
  `startup`, or `resume` under `--continue`/`--resume`) and on
  `/clear` (`clear`); an optional `matcher` selects the source.
  `[[hooks.user_prompt_submit]]` runs before every turn that reaches
  the model and takes no matcher. Both receive Claude Code's measured
  stdin payload (`hook_event_name`, `session_id`, `transcript_path`,
  `cwd`, and `source` / `prompt` — the typed text arrives under
  `prompt`, not the documented `user_input`; checked against Claude
  Code 2.1.226)
- Plain stdout, or `hookSpecificOutput.additionalContext`, is injected
  context. It reaches the model on the next turn as a data attachment
  beside the typed input — the ADR-0055 lane: nonce-wrapped, announced
  as quoted data, never in the system prompt (the cached prefix is
  untouched) and never in the risk reviewer's trusted instruction
  channel (a test pins it, as for piped stdin). Capped at 8000 runes
  per hook with a visible cut; every injection prints one notice line
- A `user_prompt_submit` hook refuses the prompt by exit 2 with the
  reason on stderr or by the JSON block forms; the prompt is erased
  (no history, no transcript, no `turn.end`) and `Run` returns
  `ErrPromptBlocked`. A session start cannot be blocked: a block from
  it is reported as a failed hook. Crashes, timeouts, and unparseable
  output fail open with a notice, as for pre-tool hooks
- Verified live in one-shot mode: both hooks fired with the measured
  payloads, the model echoed the injected markers, and an exit-2
  prompt hook stopped the run before any model call with an empty
  transcript

## [0.63.1] - 2026-09-04

### Fixed — telemetry no longer probes the GCE metadata server at startup (ADR-0068)

- With `[telemetry].backend = "gcp"`, startup intermittently paid
  4.5–7.2 s of silence before the banner or the one-shot stdin read
  (measured 6 of 24 runs; the rest paid 0 ms). Traced to Cloud
  Logging's `client.Logger`, which classifies the host by fetching
  from the GCE metadata server: on a Mac the link-local fetch blocks
  on the kernel's ARP probe, its 2 s dial timeout is retried as
  transient, and the cost depends on whether the kernel's negative
  neighbour entry is still fresh. ADC and `genai.NewClient` were never
  involved
- The exporter now declares the `global` monitored resource labelled
  with `project_id` — exactly what the detection fell back to on this
  platform — so records are unchanged and construction touches no
  network. Measured after the fix: 12 of 12 runs reach the stdin read
  within 16 ms
- No wait notice is added: the wait was a library auto-detection, not
  a contract (ADR-0033 §2 / ADR-0067 announce deliberate waits). A
  test pins the fix by counting hits on a fake `GCE_METADATA_HOST`

## [0.63.0] - 2026-09-04

### Added — a one-shot run waiting on piped stdin says so (ADR-0067)

- `-p` reads a non-terminal stdin to EOF (ADR-0055); a scheduler or
  tool harness that hands the child an idle inherited pipe made it
  wait forever with nothing on either stream (observed: ten minutes,
  read as a hang). The read is unchanged — a slow producer is never
  cut off — but a pipe still open after 2 s now earns one stderr line
  naming both remedies: close the pipe, or launch with `< /dev/null`
  when nothing is meant to be attached
- An announced wait is seen to end: the existing
  `[stdin: N bytes attached as data]` when content arrived, a new
  `[stdin: ended empty — nothing attached]` when the pipe closed
  without data (ADR-0033 §2). `< /dev/null`, here-strings and
  `echo … |` deliver EOF at once and stay silent
- Tests cover the silent fast path, the announced slow producer, and
  the idle pipe released empty; README, the approval reference, and
  AGENTS.md (both languages where mirrored) name the idiom

### Changed — Gemini 3.8 Flash (GA 2026-09-02) verified; SDK refreshed

- No code path keys on the model name, so gemini-3.8-flash works
  unchanged: measured live 2026-09-04 with the released v0.62.0 binary
  and with this build — tool-call rounds, thought-signature replay
  across rounds, and the four-term usage checksum
  (`prompt + output + thoughts + tool_prompt == total`) all hold at
  the default level and at `low` / `high`. `minimal` is rejected by
  3.8 Flash exactly as by 3.7 Flash (a clear 400 on the first turn,
  which gem-agent already annotates with the `[model].thinking` hint);
  the example config's thinking comment now says so for both models
- `google.golang.org/genai` v1.54.0 → v1.71.0. Nothing gem-agent calls
  changed shape; the refresh brings the SDK's newer finish reasons
  (`TOO_MANY_TOOL_CALLS` among them, which the generic
  non-STOP branch already reports by name) and usage-metadata fields
- Endpoint note corrected: the Gemini 3 family is served from `global`
  **and the `us` / `eu` multi-regions** (model page; `global` and `us`
  measured live 2026-09-04 with 3.8 and 3.7 Flash), not from `global`
  alone — single regions such as `us-central1` still 404 (measured).
  `config.example.toml`, `AGENTS.md`, the `defaults()` comment in
  `internal/config`, the configuration reference and health-check
  runbook (both languages), and the RFP's endpoint line say so; the
  example config now names gemini-3.8-flash
- The `-tags live` measurement tests address gemini-3.8-flash

### Fixed

- The output-limit error pointed at `[model].max_output_tokens`, a key
  that does not exist (strict decode would have rejected it). It now
  suggests narrowing the request or lowering `[model].thinking`

## [0.62.0] - 2026-09-03

### Fixed — usage records omitted the tool-use prompt bucket (ADR-0066, issue #1)

- Vertex defines `totalTokenCount` as the sum of four counts — prompt,
  candidates, **tool-use prompt**, thoughts — and gem-agent copied
  three, so a `web_search` / `web_fetch` record whose built-in tool
  returned content had a `total` larger than the sum of its parts and
  failed ADR-0057's own checksum (observed by gem-usage-lens `verify`,
  two records on a 2026-09-01 transcript). The bucket is the results
  of search grounding / URL context fed back to the model as input
- Every `usage` record now carries `tool_prompt` (written always, zero
  included — a missing key is how an aggregator tells a pre-0066
  record from a measured zero), populated on both the streaming and
  the side-call paths; the checksum is restated as
  `prompt + output + thoughts + tool_prompt == total`
- The `model.usage` audit event gains `tool_prompt_tokens`, so a
  figure computed from Cloud Logging keeps the same arithmetic as one
  computed from a transcript
- A `-tags live` test issues one URL-context fetch and asserts the
  four-term checksum with a non-zero fourth term, next to the existing
  main-loop measurement
- `/riskbook learn` wrote no `usage` record — its draft call landed
  four days before ADR-0057 and the sweep missed it (found by the
  ADR-0066 review). It now writes one with source `riskbook_learn`

### Added

- `/usage` per-tool lines show `tool results N` when the bucket is
  non-zero — on a `web_fetch` the fetched page is most of the call
  (measured 953 of 1054 tokens), and a line without it undercounted
  the tool's input by an order of magnitude

## [0.61.1] - 2026-09-02

### Fixed — cancellation ignored by file walks (ADR-0065, operator report)

- Ctrl+C during a file search stayed on "interrupting…" while the
  search ran to the end of the project; on a slow filesystem the wait
  was the whole remaining walk. Root cause: `search_files` and
  `list_tree` received the turn's context and never consulted it, and
  the agent ran every tool synchronously with no floor under it. The
  delegated `agentic_file_search` child wedged the same way. Measured
  on 30k files: the shipped walk returned 1.6 s after a cancel fired
  at 20 ms; the fixed one, 2 ms
- The walks now check the context before every directory and file
  read (and every 1024 lines inside a file) and return what they
  found, labelled `[interrupted after N files scanned — results above
  are partial]` / `[interrupted — the tree above is partial]`;
  reproduced deterministically with a FIFO stalling the walk inside
  `ReadFile`
- A return-guaranteed floor under every tool call: a call that has
  not returned 1 s after the cancel is abandoned — the model sees an
  explicit "abandoned" result, the audit event says `abandoned`, the
  exit receipt counts abandoned calls still running, a late return is
  recorded (`tool_late_return` record and `tool.late_return` event),
  and a late MUTATING return is announced to the model at the start
  of the next turn. `ask_user` is exempt (it waits on you, not a
  filesystem). The shell `WaitDelay` drops from 2 s to 500 ms so a
  cancelled shell call's output is never discarded by the floor — and
  a command that exited normally but left a background child on the
  output pipe now returns its output with a note instead of an error
  (`ErrWaitDelay`, previously reported as a failure). The abandoned
  count is shared with the `agentic_file_search` child, so its
  abandoned calls appear on the receipt too
- The three-press escape ladder now exists outside the TUI too: in
  the plain REPL and `-p`, the first Ctrl+C cancels and says
  "interrupting…", the second warns, the third exits 130.
  Previously every SIGINT after the first was swallowed
- The exit says `sending audit events… (up to 3s)` before its bounded
  flush instead of pausing silently — that pause was the wait behind
  the "third Ctrl+C took a while" report
- The Homebrew formula description drops the retired "Claude Code
  fallback" wording (ADR-0061 follow-up; the tap README already said
  "agent runtime")

## [0.61.0] - 2026-09-02

### Fixed

- **`--help` still pitched the retired positioning** (ADR-0061
  follow-up). The cobra `Short`/`Long` text called gem-agent a
  "Claude Code fallback" / "continuity tool" — the one user-facing
  surface the ADR-0061 repositioning sweep missed. It now states the
  current charter: an independent, deliberately minimal agent runtime
  with drop-in ecosystem compatibility. Code comments that justified
  fail-open behavior with "a backup tool must start" now give the
  behavior's real, still-standing rationale (degrading beats refusing
  to start); the behaviors themselves are unchanged.

### Added

- **A positional argument is the first interactive turn** (ADR-0064).
  `gem-agent "run the tests"` starts the ordinary interactive session
  and submits the message as turn 1 once the banner has printed — the
  shape "the opening move is decided, the rest is interactive" that
  neither `-p` (answers and exits) nor plain interactive (turn 1 must
  be typed) covered. The message travels the exact typed path — `!`
  shell escape, slash commands, `/skill` expansion, `@` mentions, the
  `> line` echo, input history — and fires exactly once (cleared when
  the first size report queues it, so a resize cannot resubmit).
  Composes with `--continue`/`--resume`/`--auto` unchanged. Combining
  it with `-p` is refused with both meanings named. In the piped
  plain-REPL fallback the argument runs before the first stdin line;
  ADR-0055's boundary (piped stdin is data, never prompt text) is
  untouched.

## [0.60.0] - 2026-09-02

### Changed

- **Mermaid fences render in place again, and the runtime says nothing
  about diagrams** (ADR-0063, supersedes ADR-0043). Two months of
  sessions measured the tool design failing in the field:
  `render_diagram` fired once in 76 sessions, while the model — told
  "do NOT write a mermaid fence" — generalized the prohibition and
  hand-drew box-art diagrams in replies and Markdown files instead
  (seven occurrences), unverified and wrong for files. The fence path
  returns as a pure view-layer rewrite in the TUI's Markdown renderer:
  a fence that draws faithfully becomes box art in place, one that
  does not stays source with a one-line reader-facing note
  (`*diagram shown as source: …*`), and unsupported diagram types pass
  through silently — as does a ```` ```mermaid ```` line that is
  content of an enclosing fence (a quoted example is data, not a
  diagram). The system prompt carries no diagram wording at
  all — no tool, no format preference, no prohibition (pinned by
  test): the model's natural prior, a mermaid fence in Markdown, is
  already the wanted behavior everywhere. The FIT gate is deleted with
  the tool: no width or height bound. Art segments bypass glamour and
  reach the terminal verbatim (the independent review measured glamour
  word-wrapping code-block lines at spaces, which sheared wide art;
  the terminal's own wrap splits overflowing rows in order and loses
  nothing). The wrongness guards are
  unchanged: label fidelity, edge count vs arrowheads, and the
  sequence-CJK misalignment refusal. The frozen translation table
  stays as the backstop; the v0.38.0 dialect teaching retires with the
  prompt section.

### Removed

- The `render_diagram` tool, `diagram.Budget`, and the diagram-budget
  lines of `agent_info` (ADR-0063 §5).

## [0.59.0] - 2026-09-02

### Fixed

- **`agentic_file_search` never fired — the system prompt routed around
  it** (ADR-0062). Measured across all 75 real sessions (788 tool
  calls): zero spontaneous delegations; the one recorded call was the
  ADR-0037 E2E naming the tool by hand. The Working style section
  prescribed the manual list/search/read loop by name and never
  mentioned delegation, so the model followed the workflow it was
  given — while ten turns ran four-plus navigation calls each (one ran
  thirty). The prompt now routes exploration-shaped questions
  ("where/how is X done", anything expected to take more than a couple
  of list/search/read calls) to `agentic_file_search` first,
  self-navigation stays for known targets, and the trust contract
  ("trust the report — re-read only the lines you will edit or
  quote") now spans all three surfaces: the tool description, the
  prompt, and the report header — whose old in-band "verify with
  read_file" invitation was measured triggering a 29-call
  re-exploration right after a successful delegation. Wiring pinned
  by tests; verified live twice (unprompted questions delegate
  first; post-report re-reads dropped to 6 targeted ones after the
  header fix).

### Changed

- **Repositioned as an independent agent runtime; promoted to
  cli-series** (ADR-0061, docs-only). The Claude Code-backup charter is
  retired: real-world deployment outgrew it. Drop-in compatibility with
  a project's `AGENTS.md` / `CLAUDE.md` / `.mcp.json` / skills stays the
  top requirement, now justified as ecosystem compatibility; scope
  minimalism stands on its own charter instead of "the core 20% of
  Claude Code". The monthly drill becomes an on-demand health check, and
  the drill-based promotion bar is closed as superseded — the repository
  moves from the lab-series umbrella to cli-series, whose stability
  contract (org breaking-change process) now applies. No code changes.

## [0.58.0] - 2026-08-31

### Added

- **Deny with reason — the `N` answer** (ADR-0060). The approval dialog
  (and the plain-stdin gate) gains a fifth answer between deny and
  always-allow: `N` opens a one-line field, and what you type rides back
  to the model inside the denial itself — "wrong file, put it in
  notes.md" arrives at the moment of decision instead of costing a model
  round of "how should I proceed?". `n`, Esc and Ctrl+C stay the
  one-keystroke deny; an empty reason line is a plain deny; Esc backs
  out of the field with nothing decided. Denial results now ship
  unwrapped — recognized by message provenance, never by content, so a
  tool result merely shaped like a denial stays nonce-wrapped — and the
  typed reason lands in the `gate_decision` transcript record but never
  in the telemetry export.

### Fixed

- **`/auto` left the footer's ⚡auto marker stale.** The slash handler
  flipped the agent's flag but could not see the TUI model, so after
  `/auto` the footer kept reporting auto ON while every change asked
  (or the reverse). `/auto` now routes through the same toggle as
  shift+tab, which updates the marker and prints the same localized
  state line. Found live in this release's E2E.

## [0.57.0] - 2026-08-31

### Added

- **`gem-agent workdirs` and `workdirs clean`** (ADR-0059) — the cleanup half
  of the accumulation note, which shipped as a report without a remedy. The
  listing shows id, age, files and size per earlier session; `clean` deletes
  the named ids (or every non-running one), printing exactly what will go and
  asking first — EOF aborts, `--yes` is for scripts, and a directory whose
  session still holds its transcript open is never touched. The startup note
  now points at the command, and its singular form finally agrees with its
  verb.

## [0.56.2] - 2026-08-31

### Fixed

- **`@`-references reach the session work directory.** Spilled MCP results and
  staged intermediates land there and their paths are visible in the
  conversation, but the reference resolver still confined text references to
  the project alone — the last one-root consumer, found by enumerating every
  consumer of the old boundary after v0.56.1. A relative reference still means
  the project; the work directory is reached only by the absolute path the
  conversation shows; outside both roots (sibling-prefix paths and planted
  symlinks included) is still refused, and with no work directory the refusal
  keeps its one-root wording.

## [0.56.1] - 2026-08-31

### Fixed

- **The risk ladder now knows the session work directory.** ADR-0058 made the
  work directory a sandbox root and a file-tool root, but both tiers of the
  auto-approve ladder (ADR-0004) still judged against the project alone: the
  rule tier Blocked a `write_file` into the work directory ("absolute path
  outside the project directory") and a shell redirect into it, and the model
  tier — whose payload never named the work directory — refused a `mkdir -p`
  there as "outside the stated project directory", costing a model review plus
  a human prompt for operations the design calls ordinary (all three observed
  in the v0.56.0 field test transcript). The rule tier now accepts both roots
  and its audit lines name the root that matched; the reviewer's instructions
  and payload state the work directory when one exists. Outside both roots is
  still Blocked, and credential-looking paths stay Blocked even inside a root.

## [0.56.0] - 2026-08-31

### Added

- **A work directory per session** (ADR-0058), under the same state root as
  transcripts and memory and keyed by session id, so `--resume` lands back in
  the one its earlier session used. It is a writable root of the sandbox
  profile and a second root of the file tools, its path is in the system
  prompt and in `/status`, and it is exported as `GEMAGENT_WORK_DIR` — which
  reaches `shell_exec`, every hook, and `${GEMAGENT_WORK_DIR}` in an
  `mcp.json` args entry.
- Startup reports how many earlier work directories exist and how much they
  hold. Nothing is deleted for you; a directory a session left empty is
  removed on exit.
- `make lint` (golangci-lint, org config in `.golangci.yml`), now part of
  `make check`. The repo had no linter at all; the one exclusion is
  `fmt.Fprint*` to the CLI's own streams, because reporting a failed write to
  the stream that just failed is circular.

### Fixed

- **MCP results are bounded at last.** Built-in tool results have always been
  cut at 20,000 bytes; MCP results were passed into the conversation whole.
  An oversized result is now saved to the work directory and the model gets
  the head plus the path — saved, not truncated, so nothing is lost.
- **Non-text MCP content is no longer discarded.** `CallTool` flattened every
  image to `[non-text content: image]`, which made screenshots taken through
  `chrome-pilot-mcp` invisible to the model. They are now saved to the work
  directory and the model is told to call `view_image` on the path.
- Every error the code means to ignore now says so. Reviewed one at a time and
  no latent defect was behind any of them — all are read-side `Close`, a
  `Close` on an already-failed write path, or best-effort temp-file cleanup;
  the durability paths (the `Close` before a rename, the rename, the GCS
  writer's commit `Close`) were checked already.
- Dropped dead code the linter surfaced: `Agent.toolPolicy` (superseded by
  `callPolicy` in ADR-0045) and two unused diagram test helpers.
- Four TUI tests assigned an updated model and never read it; the assertion is
  on the captured output, so the assignment was dead, not the call.
- Migrated off the deprecated `attribute.Value.Emit`.

### Changed

- The session id is resolved before the MCP servers start, so
  `${GEMAGENT_WORK_DIR}` in `mcp.json` expands to a directory that exists.
- `tools.OutputCap` is exported: the MCP adapter applies the same number the
  built-in tools do.

## [0.55.0] - 2026-08-30

### Added — every model call leaves an accounting record (ADR-0057)

Operator question: can a session's cost be computed from its transcript
at catalog prices? Not before this: the API reports tokens and never
money, and two of the spenders wrote nothing down.

- Every model call now writes one `usage` record — `source`, `model`,
  and the buckets billing uses (`prompt`, `output`, `thoughts`,
  `cached`, `total`). Sources: `main`, `risk`, `progress_review`,
  `compact`, `summarize_file`, `web_search`, `web_fetch`,
  `agentic_file_search`. **Risk evaluations and compaction used to
  record nothing at all** — their tokens lived in `/usage` and left
  with the process (309 such calls in the transcripts on the author's
  machine, countable only as `auto_decision` lines).
- Side-call records gain the two buckets that make them priceable:
  thinking tokens (billed as output) and cached prompt tokens (billed
  at a discount). `total` is the API's own count, kept as a checksum —
  `prompt + output + thoughts == total`, measured.
- The descriptive records (`web_search`, `web_fetch`, `summary_usage`,
  `agentic_search_usage`) keep their diagnostics and lose their token
  fields: one place to count, so no aggregator can double-count.
- The session header records `location` — prices resolve per SKU per
  region.
- The audit stream's `model.usage` (ADR-0035) gains `thought_tokens`
  and `total_tokens`, so a Cloud Logging figure uses the same
  arithmetic. Metadata only, unchanged.
- Not included, deliberately: a price table and a cost report. This
  makes the arithmetic possible later without baking churning prices
  into the tool.

## [0.54.0] - 2026-08-30

### Fixed — the stall warning cried wolf on large file writes (ADR-0056)

Operator field report: the model is clearly still working, but around
an `edit_file` / `write_file` call the status line says the connection
may be stalled.

- Measured (live, gemini-3.7-flash): a `write_file` with 21,761 bytes
  of content produced **33s with no chunk**, and a tap on the HTTP
  response body showed **40s without a single byte** — Gemini emits a
  function call as one whole part, so composing a large argument is
  silence on the wire, and it scales with the file. No client-side
  signal separates that from a dead connection.
- The warning threshold moves from 20s to 90s, and **nothing is added
  to the screen**: the heartbeat shows what it always showed. Putting
  the cause on screen was tried and rejected on review — the
  supplier's framing of a part is not something an operator can act
  on, so it lives in the ADR. Suppression while a tool runs is
  unchanged, and no automatic timeout was added.
- The stall warning no longer repeats `(Ctrl+C interrupts)` inside
  itself — the status bar's own hint renders right after it, and the
  duplicate truncated mid-word at 80 columns. A test pins the hint at
  80 columns in both languages.
- `/riskbook learn` set its stall suppression flag before
  `beginTurnStats()`, which resets it — so a pass waiting on a human
  warned about a connection it was not using. Ordering fixed.
- Both measurements stay in the tree as live tests
  (`internal/llm/chunkgap_live_test.go`,
  `internal/llm/wirebytes_live_test.go`); the latter promotes
  `golang.org/x/oauth2` to a direct test-only dependency.

## [0.53.0] - 2026-08-29

### Added — piped stdin as isolated data (ADR-0055)

Operator field report: `curl -s https://ipinfo.io | gem-agent --auto
-p "investigate the IP…"` — the piped JSON was silently discarded.

- One-shot mode now reads piped stdin (never a terminal) and attaches
  it to the turn as a **nonce-wrapped text attachment** — the same
  lane as `@` files, flattened at send as "Attached stdin (-), quoted
  as data". It is never merged into the prompt: the `-p` string alone
  remains the risk evaluator's instruction channel (ADR-0038/0054),
  so an injection in whatever the pipe fetched cannot impersonate the
  operator. The read is bounded at 256 KiB with the clip disclosed
  inside the attachment; binary (non-UTF-8) input is skipped with a
  warning; empty stdin attaches nothing. The attachment persists in
  the transcript and survives resume; stderr notes the attached size.
- New `Agent.AttachData(ref, kind, content)` queues a data attachment
  for the next turn (between-turns discipline, drained by Run) —
  wired for one-shot stdin today, reusable for any future
  paste-as-data need.


## [0.52.0] - 2026-08-29

### Added — one-shot approval controls (ADR-0053)

Operator field report: a headless Slack read-summarize-post pipeline
died on the one-shot blanket deny — the risk ladder never ran, and
`[agent].auto_approve` was silently ignored in `-p` (an uncommented,
unrecorded force-disable).

- **`--auto`**: arms the ADR-0004 two-tier ladder in one-shot mode.
  Approvals work exactly as in the TUI; everything the ladder would
  escalate to a human — Block-tier calls, `"always"`-policy tools,
  model doubts, evaluation errors — is denied instead, fail-closed,
  with the escalation's reason in the `[denied: …]` line. The config
  key stays ignored in `-p`, now deliberately: an unattended run's
  grant must be visible on the invocation itself. Interactively,
  `--auto` starts the session in auto mode with flag provenance in
  `/settings`. Approved calls print `[auto-approved …]` to stderr so
  the pipeline's audit trail shows what ran, not only what was
  refused.
- **`--allow "name"`**: per-run approval grants (repeatable or
  comma-separated), in the `[approval.tools]` vocabulary — exact
  tool names or `mcp__server__*` prefixes. Entries join the global
  policy scope at flag precedence and go through the normal policy
  build, so a project's `"always"` tighten still wins, the Block
  floor is not lifted, pre-tool hooks still deny, and a bare `"*"`
  is still an error. Nothing persists: the deliberation lives in the
  invocation that names the tools. The one-shot `[denied: …]` line
  now names both remedies and carries the ladder's escalation reason
  when there is one.

### Changed — the risk evaluator sees the instruction in every round (ADR-0054)

ADR-0038's 3-round window, set by intuition, was measured against
all 55 real transcripts: 70% of model-tier evaluations and 63% of
turns' *terminal* gated calls fell outside it, and every
beyond-window escalation was a network-category call the operator
then approved by hand. The cutoff is removed — the operator's typed
instruction (the one context channel an injection attacker cannot
write, at any round) now rides on every model-tier evaluation,
clipped and evidence-wrapped exactly as before. The egress rubric,
Safe/Block tiers, and the confidence bar are unchanged; the same
measurement shows instruction context does not reliably rescue
read-only network lookups even when present (whois: 2 approved /
7 escalated in-window) — that friction is policy's or the
rulebook's to settle, not this change's.

### Fixed

- The `session.start` telemetry event reported the raw
  `[agent].auto_approve` config value while one-shot mode forced the
  effective value off — an audit record claiming auto-approve was
  armed in runs where it never could be. It now reports what the
  session actually runs with (ADR-0053 §4).


## [0.51.0] - 2026-08-28

### Added — ignore-aware navigation (ADR-0052)

Operator field report: in grown projects the model lists and searches
more and more, responses slow down, and noise drowns the targets.
Measured on a real 19k-file Tauri project, 99.3% of what the walks
scanned was dependency and build output — and it was the noise:
matches inside `node_modules` READMEs, a tree cap 86% consumed by
generated directories.

- **Ignore-aware walks**: `list_tree` and `search_files` skip
  well-known dependency/build directories (built-in list) and
  `.gitignore`'d entries — full gitignore(5) semantics implemented
  in `internal/ignore` with no new dependency, cross-checked against
  `git check-ignore` in the tests. Enumeration only: explicit paths
  never consult the filter; ignored directories still appear, marked
  `[ignored]` (in `list_files` too); every skip is reported;
  `include_ignored=true` is the escape hatch; a walk rooted inside an
  ignored area shows everything and says so. Measured: 1.4 s → 10 ms
  warm, and only project files match.
- **`search_files` answers "where"**: at most 5 match lines per file
  with the remainder counted, an `include` gitignore-syntax file
  filter (`*.go`, `src/**`), `mode="files"` for per-file counts
  only, and true totals in the summary even when capped.
- **`list_tree` budgets per directory**: big directories are elided
  at a reported per-directory cap (50) instead of one directory
  starving every sibling after it; `dirs_only=true` gives a
  file-count-annotated skeleton for orientation.

### Fixed

- A submodule checkout's `.git` (a file, not a directory) was listed
  and searched; VCS plumbing is now skipped by name either way.


## [0.50.0] - 2026-08-27

### Added — four floors against summarizing overwrites (ADR-0051)

Operator field report: revising a large project document tended to
destroy it — the model regenerates the whole file from a partial,
summarized, or compacted-away copy, and everything not reproduced
verbatim vanishes, reported as success. Four co-reinforcing floors:

- **The shrink guard**: `write_file` refuses to overwrite an existing
  file of 2KB or more with content under 70% of its current size
  unless the call declares `allow_shrink: true`. The refusal names
  both sizes and both remedies; the declaration is an argument, so it
  is visible on the approval dialog and recorded in the transcript. A
  destroyed document now requires either targeted edits or an
  explicit, recorded claim that the shrink is deliberate.
- **The regeneration rule** (prompt): prefer `edit_file` for existing
  files even for large revisions; never overwrite an existing file
  without having read it in full, in this conversation, after any
  compaction.
- **The compaction staleness notice**: the message that stands in for
  compacted history now warns that file contents read before it are
  no longer verbatim in context — re-read before editing, never
  rewrite a file from the summary alone. Deterministic framing, not
  summarizer output.
- **The dialog size delta**: a `write_file` that overwrites an
  existing file annotates the approval detail with what it replaces
  (`replaces existing file: 42KB → 8KB`), via a new display-only
  `Annotate` hook on built-in tools.

## [0.49.2] - 2026-08-27

### Changed — /help is a map, and exits leave a receipt (operator UX reports)

- `/help` rewritten in both languages: one line per item in aligned
  columns, blank lines between sections, and no design rationale — the
  source text's own mid-sentence line breaks used to make it wrap at
  half the screen on wide terminals, and explanations like "because
  you typed them yourself" answered questions nobody was asking. The
  details (modifier-key behaviour, queueing rules, approval tiers)
  live in the interface and approval references.

### Added

- Every interactive exit (`/quit`, Ctrl+C, Ctrl+D) now prints a
  two-line summary as the last thing in the scrollback: the session id
  with its resume command, and the session's round/token totals.
  Silent when nothing happened, and never printed in one-shot mode.


## [0.49.1] - 2026-08-26

### Changed — status output is not documentation (operator UX report)

Static explanatory captions that repeated on every render have been
removed from command output; the facts they carried live in the
reference docs instead. The operator's test named the pattern: a line
that is true but unactionable on every viewing reads as "so what?" and
trains skimming.

- `/usage` no longer appends "— cache saves cost/latency, not window
  space" to the cached line; the reading note moved to the sessions
  reference.
- `/riskbook` no longer labels the base layer "(hand-written)" — the
  path is the provenance.
- `/mcp` no longer appends the two-line explanation of tool naming and
  gating; the integration reference carries it.
- The post-save confirmation of `/riskbook learn` no longer recites the
  show/clear manual.

Unchanged on purpose: empty-state teaching (the one place the operator
actually asks "so what do I do?"), per-event disclosures (clips, hidden
lines, handbacks), varying provenance, and banner navigation pointers.


## [0.49.0] - 2026-08-26

### Added — the risk rulebook (ADR-0050)

The successor to the withdrawn `/learn`, rebuilt on the operator's
architecture: the decision record corrects the **judge**, never the
policy. The auto-mode risk reviewer now reads operator-authored
guidance on every call it judges, in two layers:

- **Base rules** — `~/.config/gem-agent/risk-rules.md`, hand-written;
  gem-agent never writes it.
- **Project rules** — drafted by `/riskbook learn` from your own
  recorded gate decisions (what the reviewer verdicted vs. what you
  actually answered; typed answers count individually, an allowlist
  `a` is one vote), then shown to you **in full, byte-for-byte what
  would be stored** — nothing takes effect until you accept it. Also
  hand-editable; stored per project outside the repository.

The rulebook is guidance, not policy: it biases the reviewer's
confidence in either direction and never skips a gate. Block-tier
calls never consult the reviewer, pre-tool hooks still run first,
memory writes still always ask, manual mode is unchanged, and prose
urging blanket approval is itself treated as a reason to escalate.
`/riskbook` shows what is in force, `/riskbook reload` picks up hand
edits live, `/riskbook clear` removes the project layer, and the
startup banner announces a rulebook while it is in force.


## [0.48.0] - 2026-08-26

### Removed — /learn is withdrawn (ADR-0049)

Field-tested twice, failed twice in opposite directions: v0.46.0
proposed nothing from 25 real approvals, and v0.47.0's fixes led the
operator to judge that far too much ends up permitted. Per-rule
confirmation — even with the full covered-tool list disclosed — did
not prove a durable boundary for loosening approvals, and accepted
command rules were invisible in every management surface. The command,
the proposal engine, and its UI are removed; the design and the
post-mortem are ADR-0045, ADR-0048, and ADR-0049.

What this release does with what /learn left behind:

- `[projects."…".commands]` entries in the machine-owned `policy.toml`
  are **no longer applied**. A startup note reports any that exist;
  delete them or keep them — they do nothing either way.
- Global `[tools]` wildcards it wrote (`mcp__<server>__*`) are plain
  ADR-0008 policy, indistinguishable from hand-written entries, and
  **remain in force** — review `~/.config/gem-agent/policy.toml` if
  you accepted server proposals.
- `gate_decision` / `auto_decision` transcript records (with the
  aggregation key and the answer's source) are still written, and
  `Approve` still reports whether the session allowlist answered:
  the data outlives the feature.


## [0.47.0] - 2026-08-26

### Fixed — /learn proposed nothing on its first real session

An auto-mode session escalated ~25 times, every one approved, and
`/learn` afterwards found nothing to propose. Two causes (ADR-0048):

- **Votes were collapsed per session because the gate could not report
  how it answered.** A session allowlist (`a`) turns one keystroke into
  any number of recorded approvals, so v0.46.0 counted each session
  once — throwing away the twenty-five decisions the operator actually
  made. The gate now reports whether the allowlist answered, recorded
  as `source` on `gate_decision`: allowlist answers still collapse to
  one vote, typed approvals count individually. The bar is five typed
  approvals **or** three approving sessions.
- **MCP friction has a shape per-tool frequency cannot see.** The
  escalations were different tools, each called once — no per-tool
  threshold is both safe and reachable. `/learn` now proposes a rule
  per **MCP server** (`mcp__asn-lookup__*`) once two or more of its
  distinct tools have been approved with none denied.

### Changed

- MCP server rules are **global**, unlike command rules: a server comes
  from your own config, behaves identically in every project, and
  cannot be introduced by a cloned repository. Servers a project
  supplied through its own `.mcp.json` are excluded. Per-tool,
  project-scoped MCP proposals are withdrawn — two proposal shapes for
  one call would duplicate or contradict.
- A server proposal lists **every tool the rule would cover**, split
  into the ones you approved and the ones you have not used, each with
  the server's own description. The rule grants more than the evidence
  for it, so the disclosure comes before the answer, in the scrollback
  where nothing clips it.
- `/learn` now reports each saved rule as it happens rather than after
  the last question.


## [0.46.0] - 2026-08-26

### Added

- `/learn`: approval rules proposed from your own recorded answers
  (ADR-0045). It reads this project's transcripts, aggregates the
  decisions you made at the approval gate, and offers one rule at a
  time with its evidence — `"never"` for a command approved in three
  or more separate sessions with no denial anywhere, `"always"` for
  one denied in two or more. Nothing changes until you accept a
  proposal, and the command only runs when you type it.
- Per-command approval policy, project-scoped
  (`[projects."<path>".commands]` in the machine-owned `policy.toml`).
  A learned rule is ordinary policy: it appears in `/settings` with
  its source, it is removed there, and it does not lift the rule
  tier's Block floor or skip a pre-tool hook. There is deliberately no
  global command table — `make build` being settled in one repository
  says nothing about the next clone.
- `gate_decision` transcript records: what the gate was asked, what
  you answered, and the aggregation key, so the learner reads
  decisions rather than inferring them from what ran. Diagnostic like
  `auto_decision`, invisible to resume, no schema change.

### Notes

- Votes are counted per session, not per call: answering `a` once
  turns one keystroke into many approvals, and five calls in one
  session are one decision made once.
- The learner is deterministic and never shows transcript text to a
  model — tool output and file contents reaching a policy proposal
  would be a route from prompt injection to persistent permission.
  The model's declared purpose (ADR-0047) is likewise excluded.

## [0.45.0] - 2026-08-26

### Changed — the declared-purpose argument is now namespaced

- `purpose` is renamed **`gem_agent_purpose`** (ADR-0047 §6). Raised by
  the operator: the bare word is generic enough that an MCP server may
  already use it for an argument of its own — an access-request tool is
  the obvious case.
- A collision was handled safely (gem-agent stands down and injects
  nothing) but degraded **silently, in the worst place**: the operator
  would lose the declaration on exactly the third-party tool whose
  effects gem-agent cannot classify, with nothing saying why the line is
  missing. The prefix makes that path effectively unreachable; the
  stand-down stays as the guard.
- `tool_call` is left out of the name — every tool argument belongs to a
  tool call — while the vendor prefix, which is the part doing the work,
  stays. The extra tokens ride a request prefix that is cached
  (ADR-0018).
- Only the wire-level argument changes. The audit event's `purpose`
  attribute, the docs, and the prompt line keep their names.

## [0.44.1] - 2026-08-26

### Fixed — a name collision could hide an argument from the approval prompt

- An MCP tool that publishes an argument of its own called `purpose`
  correctly received it untouched (gem-agent stands down and injects
  nothing), but the argument summary filtered the name unconditionally:
  the prompt rendered `(no arguments)` for a call granting access "for
  a billing audit". An approval prompt may never drop an argument
  (ADR-0021), least of all the one carrying the reason.
- The filtering moved from `CallDetail` — which now renders every
  argument it is handed — to the new `Agent.Describe`, which knows
  which tools gem-agent added the field to. A tool with its own
  `purpose` shows it among the arguments and reports no declared
  purpose, because it was never offered a field to declare one in.
- Raised by the operator asking whether the parameter name could
  collide. Measured across the 19 configured MCP servers: none declares
  one today, so nothing in the field was affected.

## [0.44.0] - 2026-08-26

### Added — the approval prompt now says why, not only what

- Every approval-gated tool takes a required `purpose` argument
  (ADR-0047): the model's one sentence about why the call is needed,
  shown on the approval prompt above the arguments, on the `⚙` event
  line for calls that never open a dialog, in the `tool.call` audit
  event as its own attribute, and in the session transcript.
  Reported by the operator: approval was requested for a `cp` into a
  temp directory — staging a file for a Slack upload — and nothing
  said so anywhere. The motivation was not missing but unreachable:
  measured across 45 transcripts, 349 tool-calling turns carried a
  text part exactly once, because Gemini 3 writes its preamble as a
  thought summary, which is display-only, cleared the instant a round
  ends in a call, and never stored.
- The argument is injected centrally, so built-in and MCP tools are
  covered identically, and stripped again before the tool runs — no
  MCP server receives an argument its own schema never declared. A
  server that publishes its own `purpose` keeps it untouched.
- Self-declaration is never evidence: the purpose is stripped from the
  risk-evaluation payload (the evaluator must not read the proposer's
  justification), cannot move a rule-tier verdict, and is excluded
  from the loop guard's signature so re-worded justifications cannot
  disguise a repeated call. All three pinned by tests.
- A call that arrives without a purpose still runs; the prompt and the
  audit event say *(no purpose declared)* instead. Refusing would
  invent a new failure at an approval prompt the operator cannot
  satisfy, for an annotation that is not a safety control.

## [0.43.0] - 2026-08-26

### Added

- Auto-approve model tier now reads MCP tool self-descriptions
  (ADR-0046): an `mcp__` call's risk evaluation carries the
  description the server publishes for the tool, nonce-wrapped as an
  untrusted claim — the evaluator no longer guesses semantics from
  the tool name alone, which is where verdicts wobbled call to call.
  Honest read-only semantics support approval; arguments contradicting
  the description escalate; a description that lobbies for its own
  approval is itself escalation evidence (live-measured). Built-in
  tools are unchanged, and the Block floor and pre-tool hooks are
  untouched.
- ADR-0045 (transcript-driven approval-rule learning, `/learn`)
  drafted as Proposed — design only, no behaviour change yet.

## [0.42.0] - 2026-08-26

### Added

- `/version` slash command: one line with the build's version, macOS
  version, and platform — the same identity line `agent_info` leads
  with, now reachable without asking the model (operator proposal)

## [0.41.1] - 2026-08-25

### Fixed — verify-release now gates on notarization; an un-notarised zip once shipped green

- The v0.41.0 zip went out un-notarised with every check green. Three
  failures stacked: Apple's updated developer agreement made the
  notary profile probe fail, `notarize-darwin.sh` failed open by
  design (so contributors without credentials can still build), and
  `verify-release` *displayed* the spctl verdict without gating on it
  — piped through `head`, the pipeline's exit status is head's, so a
  `rejected` could never fail the chain. (The zip was re-submitted
  unchanged after the agreement was signed: same bytes, now Accepted,
  so the published asset and the tap needed no update)
- `notarize-darwin.sh` now writes `<zip>.notarized` only on
  `status: Accepted`, and `verify-release` requires that marker —
  deterministic and local, no reliance on spctl's online ticket
  lookup, which can lag a fresh submission. The fail-open path for
  credential-less builds remains, but it can no longer reach a
  release unnoticed. Both directions are tested live: marker present
  passes, marker absent fails the build

## [0.41.0] - 2026-08-25

### Added — operator pre-tool hooks: the org's guards survive the fallback (ADR-0044)

- `[[hooks.pre_tool_use]]` in the global config runs an operator
  command before every model tool call its `matcher` covers, with
  Claude Code's PreToolUse JSON on stdin. The org's
  `guard-recursive-write.py` runs **unchanged** — measured, not
  assumed: the guard reads only `tool_input.command`, and gem-agent's
  `shell_exec` argument has the same name
- A hook deny is a deterministic floor: the approval ladder,
  auto-approve, and the session allowlist never see the call, and the
  reason returns to the model as the tool result (the ADR-0043
  principle), which steers the retry
- Both verdict contracts: stdout `permissionDecision` JSON (exit 0 —
  what the installed guard actually emits) and exit code 2 with stderr.
  Anything else — crash, timeout, unparseable output — proceeds with a
  warning: hooks only ever tighten, and a broken guard script must not
  brick the fallback tool
- Matchers accept gem-agent names, Claude Code names (`Bash` ↔
  `shell_exec`, `Write`/`Edit`/`Read` likewise), `a|b` alternation, and
  `*`. Global config only; `~/.claude` remains unread (ADR-0011), and
  the org installer is untouched — registration is one config block
  pointing at the same script file

## [0.40.0] - 2026-08-22

### Changed — diagrams are drawn by a tool, and the runtime stops rewriting the reply (ADR-0043)

- The chat rewrite is **removed**. A ```` ```mermaid ```` fence in a reply
  is displayed as source: the reply is shown as the model wrote it
- `render_diagram` draws instead. The art appears in the terminal as a
  side effect and the model receives a status line — never the art,
  which it would reproduce badly and pay for twice
- **The model is now told when a diagram fails.** The old path called
  `diagram.Rewrite` from one place, the Markdown renderer, so a refused
  diagram was invisible to its author: the model could not learn that
  its source was malformed, and repeated the mistake. Every refusal now
  returns a reason it can act on — the measured width against the
  budget, the line count against the cap, a label the renderer dropped,
  an edge count that does not match the arrowheads drawn
- Verified live at 72 columns: the model tried `flowchart LR`, was
  refused twice, switched to `flowchart TD`, and drew — the operator saw
  only the diagram that worked
- The three rules of ADR-0042 (translate / fit / verify) are unchanged;
  they are the tool's engine now, and their refusals became feedback

### Added — `agent_info` reports the diagram budget

- Usable columns and the fixed line cap, so the model can shape a
  diagram to fit before composing it rather than discovering the
  constraint by being refused
- Deliberately the budget and **not** the console's dimensions: the
  inline TUI scrolls, so the terminal's rows are not the bound, and the
  usable width is the terminal minus the Markdown renderer's margin. A
  model told the raw size shrinks diagrams that had room and overruns on
  a tall terminal
- It is not in the system prompt: that stays byte-stable so ADR-0018's
  cache prefix survives, and a number that changes on every resize would
  either break it or go stale

## [0.39.2] - 2026-08-22

### Fixed — a full documentation audit: 43 confirmed discrepancies

Seven releases had shipped since the last audit. Eight parallel audits
checked every claim in the doc set against the code; only `CLAUDE.md`
was clean. What was actively misleading:

- The **RFP** — the canonical spec — still declared memory *out of
  scope* while memory ships, offered `location = "us-central1"` in its
  config sample (the Gemini 3 family is global-endpoint-only, so a
  reader following it gets 404s), listed 5 built-in tools of 20, 3
  flags of 8, 5 slash commands of 13, and gave the pre-ADR-0022 session
  path
- **`trusted_projects`** was documented as approval relaxation. It
  grants full project trust: the project's `.mcp.json` servers spawn,
  its skills are discovered and its instruction files are read. Someone
  relaxing one approval was enabling three other things
- `p` (persist to policy) was stated unconditionally; it is a TUI
  answer, absent from the plain-stdin gate
- The drill runbook pointed at the flat session path, called
  auto-approve a config default (it ships off), and referred to a step
  number that shifted when a step was inserted — as did the promotion
  criteria

Everything from v0.35.0–v0.39.1 that had not reached the reference
volumes is now there: the memory-write auto-approve exclusion (missing
from approval, sessions *and* architecture), the memory save trigger,
`internal/diagram` (absent from the architecture doc entirely),
terminal diagrams in `README.md`, `/memory`'s removal routes,
`sessions --all`, and the fact that the plain REPL really does reload.
Incomplete enumerations were completed: `⚡auto` in the status line, the
`pattern` provenance value, `OnRoundLimit`/`OnToolDone`,
`web_search`/`web_fetch` as gated tools, `/dev` in the sandbox's
writable set, `@` absolute paths covering documents and media as well
as images, the 80-line diagram height cap, and the previously
undocumented `GEMAGENT_MCP_STDERR` and `RUNEWIDTH_EASTASIAN`.

### Changed — the mirror check now compares content, and covers the READMEs

- `scripts/docs-mirror-check.sh` verified only that each `docs/en` file
  had a `docs/ja` counterpart. That is how `README.md` lost the
  terminal-diagram sentence for six releases while `README.ja.md`
  carried it — the pair existed, and nothing compared them. The root
  READMEs were not even in scope
- It now also compares the **identifiers** of every pair: tool names,
  config keys, CLI flags and slash commands in backticks, outside
  fenced blocks. A translation must not change those, and they are what
  goes stale. Placeholders, filenames and prose are ignored. Measured
  across the set: 55 pairs, and it found three real one-sided
  identifiers on its first run
- The RFP and AGENTS.md no longer carry counts or version numbers that
  rot. Where they used to enumerate, they now name the catalogue that
  owns the list

## [0.39.1] - 2026-08-22

### Fixed — `/memory` hid how to delete a memory, exactly when there was one to delete

- The listing printed where memories live and how to remove one only in
  its **empty** branch: the guidance vanished the moment something was
  stored, which is the moment it is needed. `/memory` takes no arguments
  (there is no `/memory delete`), so the omission read as "there is no
  way to remove one". Both branches now print the storage paths and the
  two routes — ask the agent (`delete_memory`, approval-gated) or delete
  the file
- The scope tag is padded, so `[global]` and `[project]` rows no longer
  shift every column after them

## [0.39.0] - 2026-08-22

### Fixed — the agent never proposed a memory on its own (measured: 0 in 39 sessions)

- Every memory ever stored followed an explicit operator request; the
  write gate had never fired unprompted, so the feature's apparent
  precision was measuring the operator's judgement rather than the
  agent's. The prompt granted a capability ("you can persist…") and
  spent its only concrete sentences on three prohibitions — a vague
  positive beside concrete negatives reads as "do this rarely"
- The memory prompt now states **when** to save: as a piece of work
  finishes, ask whether you learned something that would have saved work
  had you known it at the start, and if so save it without being asked.
  The positive test is as concrete as the prohibitions, and a test pins
  both, including that the trigger outweighs them in the text
- Verified live: on a task whose only quirk had to be discovered by
  hitting it, the agent finished and then proposed the workaround as a
  project memory, unprompted

### Security — the model tier can no longer approve its own memory writes

- ADR-0020 §4 names MITL at write time as the defence for memory (a
  persistence vector for prompt injection) and the tool policy as the
  operator's *deliberate* relaxation. A model evaluator is not that
  deliberation — it is the same party that proposed the write, so the
  poisoned-tool-result attack would clear both steps by itself
- With saves finally firing, auto mode was measured approving one
  ("saving a project-scoped memory note is safe and low-risk") without
  the operator seeing it. `save_memory`/`delete_memory` are now excluded
  from auto-approval and always escalate, whatever the tier evaluation
  would have said

## [0.38.0] - 2026-08-22

### Changed — the model is taught the dialect instead of being corrected (operator direction)

- The system prompt now states the mermaid subset the terminal draws
  best: square-bracket labels for every node, `-->|label|` edge labels
  rather than `-- label -->`, no `direction` inside subgraphs, no
  classDef/style/click, no `&` inside a label, and "keep it to what
  fits a terminal". The model writes what renders instead of having
  its diagram rewritten underneath it
- The translation table is **frozen** as the backstop for a model that
  does not follow the guidance. Measured before freezing: removing it
  costs 2–3 correct diagrams of 18 and lets one wrong graph through,
  so it earns its place — but a new construct belongs in the prompt,
  not in the table
- Measured after the change: the model's next three diagrams used the
  taught dialect with no violations, and all three drew

## [0.37.6] - 2026-08-22

### Fixed — flowcharts with subgraph-id edges were refused by an assumption

- v0.37.2 refused any flowchart with an edge whose endpoint is a
  subgraph id ("the renderer draws a phantom node") — written from an
  assumption, never measured. Measurement shows the renderer draws
  those edges correctly in most diagrams (the field case: 13 source
  edges, 13 arrowheads, every label present), and where it does not,
  the generic verification already catches it. The blacklist is gone
- The tight-padding retry is gone too: it overwrote label cells in
  double-width text ("種別判定" came back as "種別┬定"). One layout —
  the art fits the terminal or the source is shown

### Changed — the package is three rules, and nothing else (operator direction)

- Four field reports had produced four special cases. The operator
  named the pattern — build the minimum necessary judgment instead of
  bolting external judgment and correction onto the renderer — so the
  package now runs exactly: **translate** (deterministic mapping of
  constructs the renderer's grammar rejects, each entry a syntax fact),
  **fit** (one layout), **verify** (labels present, edges == arrowheads)
- Rule 3 is what makes per-construct blacklists unnecessary; both that
  had accumulated are deleted. Measured against every mermaid block
  from five field sessions: 16 of 18 draw, and the two refusals are
  diagrams where the renderer genuinely lost a subgraph title or an
  edge label

## [0.37.5] - 2026-08-22

### Fixed — flowcharts with multi-word edge labels stopped drawing (operator report)

- The fidelity guard compared the source's labels against the art with
  only whitespace stripped, but the renderer PADS labels with its own
  line art: a horizontal edge label is drawn as "──IP─/─CIDR──" and a
  label crossing a subgraph border as "Domain│/ FQDN". Those read as
  lost labels, so correct diagrams were refused and shown as source —
  visible only once a diagram used an edge label containing a space
  (single-word labels never tripped it, which is why it shipped)
- The guard now compares through the decoration (box drawing, block
  elements, geometric arrowheads, whitespace). Presence is all that
  becomes more permissive; the edge-count guard still proves the
  structure. Verified against every mermaid block from three field
  sessions: all flowcharts draw with edges == arrowheads, the
  phantom-node case still falls back, ER is bound only by width

## [0.37.4] - 2026-08-22

### Reverted — the ER complexity cap (operator direction)

- v0.37.3's dense-ER fallback (relationships/degree thresholds) is
  gone: a diagram that fits the screen is shown, crossings and all —
  readability is the operator's call, and "too complex, simplify" is
  a message to the model, not a threshold. The guards that remain are
  about being wrong (labels, edge counts, phantom nodes), never about
  being ugly. The subgraph `direction` fix from v0.37.3 stays

## [0.37.3] - 2026-08-22

### Fixed — subgraph `direction` drawn as a node; dense ER diagrams cross (operator report)

- A `direction TB` statement inside a subgraph was drawn as a literal
  node and fused the adjacent subgraph titles ("Client ZoneMCP
  Servers"); `direction` hints are now dropped before rendering, and
  the multi-subgraph flowchart draws with its titles and edges intact
- A dense ER diagram (measured: the field's 7 relationships, one entity
  at degree 4) has its crow's-foot lines cross and become unreadable —
  and the lines are all present, so no label or edge guard catches it.
  ER diagrams beyond 5 relationships or with an entity at degree >3 are
  now shown as source (2–3 relationships still draw). A layout-quality
  limit, phrased conservatively

## [0.37.2] - 2026-08-22

### Fixed — a flowchart drawn wrong passed the fidelity guard (operator report)

- The `A -- text --> B` edge-label syntax was not parsed by the
  renderer, which read "A -- text" as a node: the decision node lost
  its branches and phantom nodes appeared — and every label was still
  present, so the label-presence guard let the wrong graph through.
  Edge text is now normalized to the `-->|text|` form (also the dotted
  and thick variants), and a **structural guard** requires the source's
  edge count (|left| × |right| per arrow, '&' fan-ins included) to
  equal the arrowheads drawn — a diagram with a dropped or mis-parsed
  edge falls back to source
- An edge whose endpoint is a subgraph id draws a phantom node named
  after the id; such diagrams now stay source

## [0.37.1] - 2026-08-22

### Fixed — box art sheared under a Japanese locale (operator report)

- Under LANG=ja_JP.UTF-8 go-runewidth treats East Asian Ambiguous
  glyphs — box drawing, arrows, "…" — as two cells, while the rest of
  the width stack (x/ansi, uniseg) and the common terminal setting
  treat them as one. glamour pads code-block lines with go-runewidth,
  so box art got per-line padding that depended on how many box
  characters the line held (measured 176/125/172/125 cells on
  consecutive lines of one ER diagram) and the scrollback hard-wrap
  sheared the over-padded tails. The TUI now pins go-runewidth to
  narrow (unless RUNEWIDTH_EASTASIAN is set explicitly) — one width
  model everywhere; a test renders box art through glamour under the
  CJK setting and asserts uniform widths
- A '&' inside a flowchart node label is read by the renderer as the
  fan-in operator even when quoted (measured); the fidelity guard
  correctly refused those diagrams — they now draw, with the label's
  '&' shown as the full-width ＆

## [0.37.0] - 2026-08-22

### Added — mermaid diagrams draw in the terminal (ADR-0042, operator direction)

- A ```` ```mermaid ```` block in the answer becomes Unicode box art at
  flush time when it is a type the renderer draws faithfully —
  flowchart/graph (any direction, subgraphs), sequenceDiagram with
  ASCII labels, erDiagram — and fits the terminal; everything else
  stays as source. The system prompt (TUI only) tells the model
  exactly that list and asks for a one-line caption on anything else;
  files the model writes are untouched
- Two pure-Go renderers were measured against Japanese diagrams: one
  mangled UTF-8 and silently dropped edges (worse than source); the
  adopted one (AlexanderGrooff/mermaid-ascii, MIT) aligns CJK in
  flowcharts and ER. Node shapes it cannot parse are normalized to
  boxes before rendering, and a fidelity guard refuses art that lost
  any label — a diagram is never drawn incompletely
- Width-budgeted (tight padding retry, then source), height-capped,
  and the art rides the same scrollback discipline as everything
  else. Plain REPL and one-shot stay verbatim. Binary grows ~5MB (the
  renderer's package carries its web-server dependencies)

## [0.36.1] - 2026-08-22

### Fixed — whole-code review round 3 (ADR-0041, operator request)

- **security**: the agentic_file_search child expanded @-references
  in its model-authored question — the @ grammar's out-of-project
  reads (images, documents, media by absolute/~ path) rest on "an @
  is operator-typed"; the child now runs with mention expansion off
- **tui**: the live region never expanded tabs, so tab-indented code
  soft-wrapped the managed view (the v0.34.1 renderer-desync class,
  from the other side) — expanded before the width clip now
- **cmd**: the ask/round-limit prompt wrapped os.Stdin in a second
  bufio.Reader (typed-ahead stranded, piped sessions could hang) —
  the shared REPL reader is used; pinned by a source-level test
- **tui**: the ask dialog silently truncated long questions — wraps to
  the box, budgets the height, discloses hidden lines; thoughtView
  showed the oldest words — shows the freshest two lines; stall
  detector re-arms on a new OnToolDone signal (never on side-call
  chunks), ignores side-call thoughts during a tool, resets at turn
  boundaries, and `!command` no longer warns about a connection it
  never had; releaseTurn drains a pending approval; zero-option ask
  cannot panic
- **agent**: checkpoints and plainAsk honour a cancelled context;
  automatic compaction now emits the `compaction` audit event (only
  /compact did); non-interactive extensions announce themselves;
  loop-trigger evidence includes the triggering call; loop-guard
  skipped calls audited as outcome=skipped; typed RoundLimitError
  (errors.As) replaces wording matching; rune-safe report truncation
- **misc**: /usage labels "risk & progress reviews"; startup-notes tee
  freezes after the banner; dialog reason clipped

## [0.36.0] - 2026-08-22

### Changed — the round limit is an intervention ladder, not a guillotine (ADR-0040, operator report)

- Triggered by a session log: a healthy 50-round research turn was
  killed mid-pipeline by max_turns while monotonically progressing,
  and the error recommended /clear — the one action that would have
  destroyed the recoverable state
- A deterministic loop detector escalates three identical consecutive
  calls immediately (a real runaway no longer gets 40 free rounds);
  legitimate repetition (polling) is handled by whitelisting the
  signature after one "continue"
- Reaching the limit runs a progress review (ADR-0038-style evidence:
  the operator's instruction + the turn's activity trace, nonce-
  wrapped, no tools) and then decides per mode: interactive asks via
  the ask-dialog grammar with the verdict as evidence; auto-approve
  continues by itself on a confident "progressing" with a visible
  notice (auto exists to reduce interruptions — operator direction);
  one-shot lets the review decide, fail-closed
- The absolute cap is 3× max_turns — the Block-floor principle applied
  to rounds; the agentic_file_search child keeps its plain hard bound
  (a child that runs dry needs a narrower question, ADR-0037)
- Stop messages now teach recovery: progress is saved, "continue"
  resumes where it left off; /clear is no longer recommended.
  Interventions are recorded in the transcript (round_intervention)

## [0.35.0] - 2026-08-22

### Added — in-session reload of skills and MCP, and --mcp (ADR-0039, operator request)

- `/mcp reload` reconnects every MCP server mid-session — full
  restart, config re-read, fresh tool lists — without losing the
  conversation: the recovery path for a wedged server, and how a
  server added to mcp.json joins a running session. `/skills reload`
  re-runs skill discovery; the system prompt's skill section and the
  agent's tool declarations follow immediately
- Both reuse the startup code paths and the startup trust verdict —
  a reload can never widen what the trust gate allowed; the session
  approval allowlist survives (keyed by tool name); connection
  warnings land in the command output (stderr would corrupt the TUI)
- load_skill is now registered even for zero-skill sessions (reading
  the live list through a getter), so a reload can populate a session
  that began empty
- Reloads are audited: an `integration.reload` telemetry event plus a
  transcript record — a session whose tool surface grew mid-way must
  not look like the session that started
- `--mcp on|off` overrides `[mcp].enabled` per run (flag provenance
  in /settings): `off` skips every server spawn — what a `-p`
  pipeline usually wants; `on` forces MCP against a disabling config

## [0.34.1] - 2026-08-22

### Fixed — frame rows leaking into scrollback (operator report, recurrence)

- Thought-stream fragments and the running-status heartbeat could leak
  into the conversation scrollback, glued to tool-event lines. Root
  cause read straight out of Bubble Tea v1.3.10's renderer: queued
  tea.Println lines are printed VERBATIM — no truncation, and no
  end-of-line erase for a line at or beyond the terminal width — so a
  single over-wide scrollback line (a ⚙ tool event with a long
  detail, e.g. save_memory content) soft-wraps, the renderer's cursor
  accounting desyncs by the extra rows, and the previous frame's top
  rows survive into history with old row tails glued after the
  wrapped line's end
- Fix: emit() now hard-wraps every scrollback line to width-1 cells
  (wrapping, never truncating — tool details and shell output are
  evidence). Regression-tested: the leak test fails against the old
  code and the printed-row accounting is asserted exact

## [0.34.0] - 2026-08-22

### Added — instruction context in the auto-approve model tier (ADR-0038, operator direction)

- For a turn's first 3 rounds, the risk evaluation sees the request
  the operator typed — the one context channel an injection attacker
  cannot write. Alignment supports approval (fewer needless
  escalations); a call contradicting the request, or serving
  directions found in file contents, now escalates with the
  contradiction named — invisible to the call-only view
- The instruction rides inside the same nonce wrap as the call
  (pasted text must not command the reviewer), clipped at 2000 runes;
  attachments, history, and the model's own intent narration are
  excluded by design
- Later rounds fall back to the conventional call-only evaluation
  byte-identically (operator direction: deep-turn calls legitimately
  serve sub-goals the instruction never names) — no regression is
  possible where the context does not apply
- Invariants unchanged: Block floor decided before the model tier,
  fail-closed, Safe tier never consults the model. The context
  reaches Review-tier calls only (shell, memory writes, MCP)
- Live-measured: an aligned `make build` approves citing the
  instruction; the same command explicitly forbidden by the
  instruction escalates ("Operator explicitly requested not to run
  builds"); at round 5 the conventional approval returns

## [0.33.0] - 2026-08-21

### Added — agentic_file_search tool (ADR-0037, operator direction)

- Delegated project search: a child agent loop (main model) explores
  the project in its own isolated context and returns only a compact
  report — the exploration, dead ends included, never enters the
  conversation. ADR-0014's summarize principle generalised from one
  file to one question; deliberately narrowed from a general-purpose
  sub-agent (approval without visible context is not approval)
- Read-only by construction: a positive allowlist built with the new
  `Registry.Subset` (unknown names are loud errors) — orientation,
  windowed reads, summaries; never shell, edits, web, MCP, ask_user,
  or itself (recursion structurally impossible); deny-all approver as
  fail-closed insurance; 10-round cap
- Report contract names its negative space: what was NOT found is
  stated explicitly; evidence as path:line-range with verbatim
  quotes, flagged lossy in the result header
- Delegation is visible and audited: child tool calls render live as
  "↳ tool" lines, the stream heartbeat keeps ticking, and the new
  `telemetry.Sink.Sub` labels every child audit event with
  agent="agentic_file_search"; spend lands in /usage as its own
  category (the context gauge tracks the main conversation only)

## [0.32.0] - 2026-08-21

### Added — ask_user tool (ADR-0036, operator request)

- The model can now present a question with 2-8 options and get the
  operator's choice without ending its turn — Claude Code's Ask tool,
  on the approval dialog's interaction grammar: arrows/Tab move,
  digits 1-9 select-and-confirm in one press, Enter confirms, Esc
  declines (a distinct result telling the model to ask in prose or
  proceed with stated judgment — information, not an error)
- Read-only and never approval-gated; question/options bounded
  (500/100 runes, at most 8 options); dialogs arriving after Ctrl+C
  are auto-declined and a pending dialog is drained on turn end, so
  the blocked tool goroutine is never stranded
- Every mode answers honestly: TUI dialog, numbered stderr prompt in
  the plain REPL, and an informative refusal in one-shot -p (a
  pipeline must not hang on a question). No free-text option by
  design — ending the turn and asking IS the free-text channel

## [0.31.0] - 2026-08-21

### Added — telemetry auth from a credentials file (operator report)

- OTLP auth headers can now come from `[telemetry].headers_file` — a
  mode-0600 JSON file (by convention ~/.config/gem-agent/auth.json)
  of header name → value. The operator's point: an environment
  variable disappears under launchd, cron, and shells that never
  sourced the profile, and auth that silently vanishes with its
  environment is not workplace auth
- Owner-only permissions are enforced (anything else is refused
  naming `chmod 600`), garbage and empty files fail with the expected
  shape named, the gcp backend rejects the key (it authenticates via
  ADC), and unset still falls back to OTEL_EXPORTER_OTLP_HEADERS.
  Verified on the wire: the file's Authorization header arrives at a
  real OTLP/HTTP receiver

## [0.30.0] - 2026-08-21

### Added — audit logging: Cloud Logging by default, OTLP for collectors (ADR-0035, operator request)

- Opt-in `[telemetry]` config exports audit events: session
  start/end, tool.call with clipped detail/duration/outcome,
  approval.decision with the deciding layer, turn.end, model.usage,
  compaction, media.upload — with service/session/project/host
  attributes
- The default backend is `gcp` (operator observation: the credentials
  already exist) — events land in Cloud Logging of the [gcp] project
  via the same ADC Vertex uses, log name "gem-agent", structured JSON
  payloads; `enabled = true` is the entire setup. `otlp-grpc` /
  `otlp-http` send OpenTelemetry log records to an org collector
  instead; wire format verified against a real OTLP/HTTP receiver
- Metadata only, by design: prompts, responses, file contents, and
  thought summaries never travel this channel — the local transcript
  stays the full record
- Default off; only the operator's global config can enable telemetry
  or set the endpoint (a project's .gem-agent.toml structurally
  cannot — the exporter is an egress channel); headers via the
  standard OTEL_EXPORTER_OTLP_HEADERS
- Telemetry never hurts the session: batching, a single stderr
  warning on export failure, silent degradation, 3s-capped shutdown
  flush; disabled costs zero (no-op sink, no call-site branches)

## [0.29.2] - 2026-08-21

### Fixed — false stall warning during long tool runs (follow-up to 0.29.0)

- While a tool executes, the model stream is silent BY DESIGN — but
  the ADR-0033 stall detector counted that silence and warned
  "connection may be stalled" ~20s into any long shell command. The
  warning is now suppressed while a tool call is running and re-arms
  the moment the stream speaks again (found while confirming the
  operator's question that the silent tool and the dead Ctrl+C shared
  one root cause — they did: the defeated timeout)

## [0.29.1] - 2026-08-21

### Fixed — cancellation deadlock (ADR-0034, operator report)

- A skill's python tool call never returned, and Ctrl+C deadlocked on
  "interrupting…". Root cause: exec.CommandContext kills only the
  DIRECT child (sandbox-exec/bash); a grandchild (python) survived
  holding the inherited output pipe, and CombinedOutput's Wait blocked
  until EOF — the 120s timeout and the operator's Ctrl+C both fell
  into the same hole (reproduced in a test that hung before the fix)
- Shell commands now start as process-group leaders and cancellation
  SIGKILLs the whole group; WaitDelay (2s) backstops a setsid escapee
  so the session never hangs for an orphan. Measured: the reproduction
  returns in ~0.3s where it previously hung for the child's lifetime
- Last-resort exit: if a future tool still ignores cancellation, a
  second Ctrl+C warns and a third quits gem-agent (the transcript is
  per-event, so everything up to the wedged call is already saved)

## [0.29.0] - 2026-08-21

### Added — turn observability (ADR-0033, operator report)

- The operator reported turns sitting on "thinking…" indefinitely with
  no way to tell what was happening. Three states used to render
  identically: a healthily thinking model, a stalled connection, and a
  silent backoff retry (up to 30+ seconds of deliberate silence)
- The running status is now live: `thinking… 1m07s · 34 chunks ·
  last 0s` — elapsed, chunks received, seconds since the last one.
  20s without data switches to a warning naming Ctrl+C; scheduled
  retries show `retry 2/3 (429) — waiting 4s`
- `[tui].show_thoughts = true` (default) streams the model's thought
  summaries into the live area, dim, replaced as the answer starts —
  display-only: thoughts are never written to the transcript, and the
  replayed history stays signatures-only (measured: multi-round tool
  turns and resume run clean with summaries requested)
- The running-status strings joined the ADR-0029 language catalogs
- No automatic timeout, by design: long thinking is legitimate; the
  display plus the operator beats a timer that kills real work

## [0.28.0] - 2026-08-21

### Added — --thinking flag (operator request)

- `--thinking minimal|low|medium|high` overrides `[model].thinking`
  for one run, mirroring `--model`; the literal `default` clears a
  configured level back to the model default (the empty string already
  means "flag not given"). Invalid values fail at startup naming the
  knob; `/settings` and `agent_info` show the flag-provided value with
  `flag` provenance

### Docs — README restructured into an overview + feature references (operator request)

- README.md / README.ja.md are now overviews: why, quickstart (with
  the brew install that was missing from the body), a one-paragraph
  map per domain, and pointers — down from ~700 lines to ~120
- The details moved to seven per-domain references under
  docs/{en,ja}/reference/: interface, tools, attachments, approval,
  sessions, integration, configuration — each linked from the README,
  the INDEX, and each other
- Features that had never made it into the README are now covered:
  slash-command Tab completion, the full 12-command table with the
  /exit alias, the UI language mode as a feature (not only a config
  key), per-project session layout with GEMAGENT_STATE_DIR, parallel-
  launch safety, and the GCS-object retention consequence

## [0.27.0] - 2026-08-21

### Added — datetime tool (ADR-0032, operator request)

- New read-only `datetime` tool: op now (current local/UTC/unix/
  weekday/ISO week), info (weekday, day-of-year, ISO week, days in
  month, leap year), add (signed calendar shifts; Go's month-end
  normalization is disclosed in the output when it fires), diff
  (calendar breakdown + total days/hours/minutes + both weekdays),
  convert (IANA timezone conversion; explicit offsets win over
  `from`). Naive inputs are read in `tz` (default local); errors
  teach the accepted forms
- No business-day counts by design: weekday arithmetic without a
  holiday calendar is wrong exactly where it would be used (Japanese
  national holidays)
- The system prompt now carries the session-start date + weekday +
  timezone (cache-stable, ADR-0018) and directs the model to the tool
  for the live moment and ALL calendar arithmetic

## [0.26.0] - 2026-08-21

### Fixed — whole-code review round 2 (ADR-0031, operator request)

Five parallel review lenses over the ~4,200 lines added since ADR-0021;
every accepted finding verified against the code, measured where
measurable. Highlights (the ADR carries the full list):

- **ja language mode reached the TUI at last**: the resolved catalog
  was never passed into tui.Options, so the whole chrome fell back to
  English; a new AST-level wiring test pins the handoff
- **Denied view_image/read_document no longer attach their bytes**:
  the attach guard screened only the "error:" prefix, which the
  denial string does not carry — the operator's refusal was silently
  ineffective for the largest payloads
- **Media store can no longer be silently poisoned**: single-fd
  hash+upload, a verifying reader that fails the stream if the file
  changed mid-upload, and an abortable writer context so failed
  copies commit nothing to the permanent content-addressed store
- **Resume loads under the flock** it appends under; lockSession
  distinguishes errnos and removes its just-created file on failure
- **Tab completion is rune-safe**: 資料/説明-style Japanese names
  drove the byte-wise common-prefix to a lone invalid UTF-8 byte
  (measured)
- **The input box renders while a turn runs** (ADR-0007's promise —
  keys were routed, the box never drawn)
- Approval dialog adapts its detail budget to short terminals (the
  clamp was silently cutting the TITLE first); interrupted turns
  auto-deny in-flight approval requests; per-turn contexts released;
  policy.toml mutations are flocked read-modify-write; docext gains
  an aggregate decompression budget; /settings shows the two tracked
  rows it never rendered and stops offering a theme edit that applied
  nothing; wide-rune wrap accounting; ~15 further honesty fixes
- Refuted by measurement (no code change): a transcript ending in an
  unanswered FunctionCall resumes cleanly against the live API

## [0.25.1] - 2026-08-21

### Fixed — parallel same-project launches (operator question)

- The operator asked whether timestamp session ids collide under
  parallel execution. The ids themselves were already safe (O_EXCL +
  numeric suffix since the first logger, flock since ADR-0021) — but
  writing the concurrent test found a real race one layer down: the
  `.project` marker was written with os.WriteFile (create-empty, then
  write), so a simultaneous launch of the SAME project could read the
  empty marker between those steps and refuse startup as a
  "path-escape collision" (measured: roughly half of 16-way
  simultaneous starts tripped it)
- Markers now land by temp-file + rename (atomic: readers see no
  marker or the whole marker), an empty marker counts as unowned and
  is repaired, and the genuine different-project refusal is unchanged
- New regression tests: 16-way concurrent Open (session) and 20×16
  concurrent EnsureProjectDir (statedir), both under -race; verified
  with 6 simultaneous real processes — 6 distinct sessions, same-second
  ids suffixed -2/-3

## [0.25.0] - 2026-08-21

### Added — agent_info self-information tool (ADR-0030, operator request)

- New read-only `agent_info` tool: the model can now report its own
  runtime — version, host platform (macOS version/arch/CPUs), the model
  it runs as and its thinking level, context-window occupancy,
  cumulative token usage (same accounting source as /usage), limits,
  approval/sandbox state, project directory, session id, connected MCP
  servers, skills, memory and media-bucket availability
- No approval prompt (read-only tier); "which model are you" and "how
  much context is left" become one cheap call instead of a guess or a
  shell_exec approval round
- Deliberately withheld: GCP project id, bucket name, hostname —
  environment identifiers with no behavioral value to the model (the
  bucket appears only as configured/none)

## [0.24.0] - 2026-08-20

### Added — UI language mode (ADR-0029, operator request)

- `[tui].language = "auto" | "ja" | "en"` (default auto: first
  non-empty of LC_ALL/LC_MESSAGES/LANG; "ja" prefix selects Japanese).
  The interactive chrome — /help, hints, the approval dialog and its
  verdicts, queue notices, /auto//clear//compact feedback, and the
  startup-safety prompts — now renders entirely in one language
  instead of the historical EN/JA mix
- The strings live in one catalog struct with two complete literals
  (internal/uitext); a completeness test fails on any string missing
  from either language, so the mix cannot silently return
- Deliberately still English: banner labels and warning: lines
  (grep-stable log output), cobra --help, model-facing text, error
  chains

## [0.23.0] - 2026-08-20

### Added — Tab completion for /commands (operator request)

- Tab on a "/"-prefixed input completes command names the same way
  @-references complete: unique match in place, longest common prefix
  otherwise, candidates listed when Tab cannot advance. After
  "/skill ", skill names complete too

## [0.22.1] - 2026-08-20

### Fixed (operator reports)

- **Footer floating mid-screen after closing /settings** (ADR-0028):
  the panel budgets itself to height−1 rows — taller than the rows left
  below already-printed content — so rendering it scrolled the terminal
  and left the printed-line counter pointing at rows that had scrolled
  away; the next frame was then padded against them. The counter now
  lives in the render state and self-heals on overflow, so any
  over-tall frame closes back to a bottom-pinned footer
- **Markdown wrapped far narrower than the console**: an aesthetic
  100-column cap made glamour hard-wrap (real newlines) on wide
  terminals, so copied lines broke mid-sentence. The cap is removed —
  the wrap width is the terminal's
- **Policy dump flooding the startup banner**: with a policy grown by
  'p' answers and MCP wildcards, the banner listed every rule. It now
  shows the first three plus a count, pointing at /tools for the
  per-tool effective gates

## [0.22.0] - 2026-08-20

### Added — audio and video input (ADR-0027, operator request)

- `@memo.m4a` / `@clip.mp4` attachments (in-project, absolute, or ~
  paths): the model transcribes and understands media natively. With
  `[gcp] bucket` set, media ALWAYS routes through the operator's GCS
  bucket as gs:// URIs — inline bytes would be re-sent with every
  round's history replay, so one upload beats many resends. Without a
  bucket, inline up to 15MB; larger files are refused naming both
  remedies. Media files are never truncated (a clipped mp4 is a broken
  file) and never deleted by gem-agent (bucket lifecycle rules own
  retention)
- Uploads are content-addressed (`gem-agent/media/<sha256><ext>`) and
  deduplicated: re-attaching the same recording is free. The transcript
  stores the gs:// URI, not the bytes — resume stays cheap while the
  object exists
- The uploader pins its quota project to the configured `[gcp].project`
  (a stale `quota_project_id` in ADC 404s every storage call while
  Vertex keeps working — measured; the fix rides the auth library's
  env override because `option.WithQuotaProject` conflicts with the
  storage client's transport options in this version)
- Verified live: an inline voice memo transcribed exactly; a
  bucket-routed video answered from both its audio track and frames;
  dedupe confirmed on re-attachment. One new dependency:
  cloud.google.com/go/storage

## [0.21.0] - 2026-08-20

### Added — document reading (ADR-0026, operator request)

- **PDF, Word, Excel, PowerPoint interpretation.** PDFs ride to the
  model as document parts — measured before designing: accepted as a
  user attachment and inside a multimodal function response, with the
  conversation continuing cleanly past the tool round. The Office XML
  formats (.docx/.xlsx/.pptx) are extracted to text locally by a new
  stdlib-only package (`internal/docext`): paragraphs in document
  order, sheets as tab-separated rows named per sheet, slides as
  numbered blocks. Legacy .doc/.xls/.ppt are out of scope, stated in
  the error
- Two access paths per ADR-0012's "who chose the file" split:
  `@report.pdf` / `@data.xlsx` operator attachments (absolute and ~
  paths allowed, like images), and the project-confined
  `read_document` tool for the model
- Extraction is nonce-wrapped untrusted data; PDFs inherit the image
  framing (visible text is content, never instructions). Caps reported:
  PDFs over 12MB refuse whole, extraction truncation is noted, zip
  members decompress through a bomb guard
- Verified end-to-end against ground truth: a cupsfilter-produced PDF
  and a textutil-produced .docx answered correctly via both paths

## [0.20.0] - 2026-08-20

### Added — thinking level (ADR-0025, operator request)

- `[model] thinking = "minimal" | "low" | "medium" | "high"` sets the
  Gemini 3 thinking level for main-model calls; unset keeps the model's
  default, and an unknown value is a startup error. Measured on one
  arithmetic prompt (gemini-3.7-flash): 93 / 170 / 222 thought tokens
  at low / medium / high; "minimal" is rejected by that model with a
  clear 400 on the first turn — supported levels are model-dependent,
  and the API stays the authority
- The summarize_file model deliberately keeps its own default;
  /settings shows the value read-only with its source

## [0.19.2] - 2026-08-20

### Fixed — scrollback bursts write once (operator report)

- Consecutive scrollback lines around one event — streamed text + the
  tool event, streamed text + "(interrupted)" / the error line, shell
  output + its outcome, attachment reports — now ride a single Println
  write. Each write is a separate clear-insert-repaint cycle on the
  inline renderer, and over a slow terminal (SSH) the intermediate
  frames were visible as content flashing through the output area —
  the Ctrl+C "(interrupted)" line most prominently. One write, one
  repaint, no window

## [0.19.1] - 2026-08-20

### Fixed — footer bounce once the screen is full (ADR-0024, operator report)

- Once printed history filled the screen, the bottom padding clamped to
  zero and the frame's bottom edge — the footer — moved with the view's
  own height: every MCP-call flush (live tail resetting from up to 12
  lines to none) and every closing approval dialog bounced it by up to
  a dozen rows. The frame's total height is now held steady in the
  full-screen regime: vacated rows render blank inside the frame, and
  each later scrollback line consumes one blank row, so history flows
  into the gap and the footer stays glued to the bottom. Supersedes
  ADR-0003's "degrades to plain inline" clause

## [0.19.0] - 2026-08-20

### Added — startup safety (ADR-0023, operator request)

- **Broad-root guard**: launching in `/`, the home directory, or an
  ancestor of home asks for confirmation (default no) after naming the
  consequence — file tools and sandboxed shell writes would span that
  entire tree. Non-interactive runs are refused there
- **First-run project trust**: the first launch in a project that
  provides agent-facing files lists them — instruction files (injected
  as instructions), `.mcp.json` (each server entry starts a child
  process), `.claude/skills` — and asks once whether to trust the
  project (default no; persisted per project in the machine-owned
  policy.toml as `trust = "granted" | "declined"`). Declining still
  starts the session with none of the project's files loaded, and the
  banner says so. Hand-listed `[approval].trusted_projects` skip the
  question; projects providing nothing ask nothing
- Non-interactive runs in an undecided project run bare (no injection,
  no project MCP, no project skills; note on stderr; nothing recorded)
  so read-only `-p` pipelines over fresh clones keep working

## [0.18.0] - 2026-08-20

### Changed — per-project session layout (ADR-0022, operator request)

- New session transcripts live under
  `sessions/projects/<escaped path>/` — one subdirectory per project,
  the same escaped-path + `.project`-marker convention as memory
  (shared implementation, `internal/statedir`). A cleanup in one
  project's directory structurally cannot touch another's transcripts,
  the listing reads one directory instead of header-filtering every
  file, and the project binding is visible in the filesystem
- **Legacy flat transcripts keep working in place**: listed, found, and
  resumed where they are — nothing is moved (the operator chose
  zero-file-motion over migration)
- An id typed in the wrong project still resolves across projects so
  the informative refusal ("recorded in X — run gem-agent there")
  survives the layout change
- `GEMAGENT_STATE_DIR` relocates the whole state root (sessions and
  memory): tests and drills run against an isolated tree and
  structurally cannot see — or delete — real state

## [0.17.0] - 2026-08-20

### Fixed — whole-code review batch (ADR-0021, operator request)

A six-lens parallel review plus static analysis and live API
measurement. Two findings independent reviewers marked certain
("dangling function call 400s on resume", "interrupted tool round
poisons history") were refuted by measurement and needed no fix.

Session integrity:

- `/clear` is recorded in the transcript; a cleared conversation no
  longer resurrects on resume, and post-clear compaction replays
  against the right list. Transcript schema is now 2 (older builds
  refuse newer files instead of misreading them; schema-1 files load
  unchanged)
- A crash's torn last line now costs exactly one line: Reopen repairs
  the missing newline and the loader skips corrupt lines with a
  reported count (measured before: one glued tear silently dropped
  everything after it — 1 of 6 turns survived)
- The session file takes a flock: a second `--resume` of a live session
  is refused instead of interleaving two processes' appends
- A failed conversation-bearing transcript write stops recording at a
  consistent prefix and says the session can no longer be fully resumed

Approval:

- **The session allowlist (`a`) no longer lifts the Block floor or an
  explicit "always" policy**: one 'a' on a benign shell_exec used to
  wave every later `sudo`/`rm -rf`/credential-path command through
  unprompted, in every mode. Block prompts now show the rule tier's
  reason and default to 拒否
- Policy resolves scope before pattern specificity: a project wildcard
  tighten now beats a global exact rule (ADR-0008's promise restored)
- Keys arriving within 300ms of the approval dialog opening are
  dropped — mid-run typing could answer (or session-allowlist) a
  dialog the operator never saw
- Multi-line shell approval details are budgeted with an explicit
  "+N lines hidden" warning instead of scrolling out of sight

TUI:

- `!` and `/` cannot be queued mid-run (queued prose merged after a
  queued `!` executed as shell; text after a queued `/command` was
  silently dropped); a half-typed draft survives a queued send; an
  interrupted `!` command hands the queued message back
- Tab-bearing output (`!git diff`) no longer drifts the pinned input
  line; the managed view is clamped on short terminals; startup
  warnings survive the first clear by riding the banner; clipping is
  rune-safe (no more U+FFFD mid-word in Japanese)
- Plain REPL: Ctrl+C during `!` or /compact interrupts the operation
  instead of killing the process

Tools and LLM layer:

- edit_file's near-miss diagnosis quotes the right region on files
  starting with blank lines; read_file's window note reports the real
  line count; edit reports no longer count a trailing newline
- A response cut off mid-generation (SAFETY / MAX_TOKENS) with partial
  text is reported instead of silently presented as complete;
  metadata-only stream chunks no longer disarm the transient retry;
  URL-metadata nil elements no longer panic
- A resumed over-threshold session can auto-compact on its first round
- file_info no longer leaks out-of-project link targets through
  intermediate symlinks, and resolve errors no longer name
  out-of-project paths
- /tools shows the live policy after mid-session edits;
  trusted_projects entries match as resolved paths with ~ expansion;
  over-long MCP tool names get a deterministic hash suffix instead of
  colliding; MCP stdin writes are generation-guarded (data race fixed)
- Dependencies bumped: govulncheck 5 reachable vulnerabilities → 0

## [0.16.0] - 2026-08-20

### Added — agent memory (ADR-0020, operator request)

- `save_memory` / `delete_memory`: the agent persists short facts across
  sessions — global scope (about the operator or this machine, recalled
  in every project) and project scope (one project only). One memory =
  one small markdown file; saving an existing name updates it
- Stored machine-owned outside the repository
  (`~/.local/state/gem-agent/memory/`), beside the session transcripts;
  nothing is written into the project tree and `~/.claude` is never
  read. The lossy path-escaping is guarded by a `.project` marker —
  a collision skips that directory with a note instead of misattributing
  another project's memories
- All memories are injected into the system prompt at session start
  (global first, then project, alphabetical — a deterministic order
  that keeps the ADR-0018 cache prefix stable) under a fixed budget,
  clipping reported. The section is framed as agent-recorded background
  knowledge, explicitly below the operator's instruction files
- **Writes are approval-gated and classified Review, never Safe**:
  memory is a persistence vector for injected instructions, so the
  human reviews each write; the ADR-0008 policy is the deliberate
  relaxation
- `/memory` lists what is stored right now; a banner line
  (`memory: N global, M project`) shows what this session loaded

## [0.15.1] - 2026-08-20

### Added — /usage (ADR-0019, operator request)

- The session's token statement: main-loop rounds, prompt/output/
  thoughts, cached share (with ADR-0018's honest line printed — cache
  saves cost/latency, not window space), current context against the
  window, and per-category side-calls — auto-approve risk checks,
  compaction, and the tools that spend on their own backends
  (summarize_file, web_search, web_fetch), each naming the model that
  spent the tokens. Empty categories stay silent: a statement, not a
  form
- In-memory accounting, not log-parsing: a display command must not
  reread a transcript that may hold megabytes of base64 images to add
  integers

### Fixed

- **Side-call usage no longer stomps the footer.** Risk evaluations and
  compaction fed the same callback as the main loop, so a risk check
  momentarily replaced the footer's "ctx" gauge with its own prompt
  size. Side-calls now accumulate into their own buckets and the ctx
  gauge always means the main conversation
- Web tools' LLM calls now report token usage at all (previously
  untracked anywhere)

Note: tag v0.15.0 carries this release's code but not its docs — a
compound-command failure applied the code commit and the tag while the
doc edits silently did not run (the same guard-and-heredoc trap as
before, caught by the CHANGELOG assertion). Released tags are never
moved in this org, so v0.15.0 stays unpublished and v0.15.1 is the
release.

## [0.14.0] - 2026-08-20

### Added — implicit context caching (ADR-0018, operator request)

- Honest scoping first: caching economises **cost and latency**, not
  window occupancy (that stays compaction's job). What it discounts is
  the agent loop's defining expense — every round re-sends the whole
  history
- The blocker was our own injection defense: the isolation tag was
  regenerated **per LLM call**, landing in the system instruction and
  every wrapped tool result, so consecutive requests differed from the
  first byte — implicit caching could never match a prefix
- **The main loop's tag is now session-scoped** (rotated on `/clear` and
  resume). Sound because nlk/guard's `Wrap` refuses content containing
  the tag name: a leaked tag cannot escape the wrapper, only get its
  carrier withheld — an availability nuisance, not an integrity break.
  Side-calls (risk eval, compaction, summaries) keep per-call tags:
  one-shot calls have no prefix to reuse
- **Measured, not assumed** — the same 4-round task on the old and new
  binary: per-call tag cached **0 / 0 / 0 / 0** tokens; session tag
  cached **0 / 35k (81%) / 42k (95%) / 42k (93%)**. The
  `cachedContentTokenCount` now flows API → usage records → footer
  (`cache NN%`), so whether caching fires stays a glance, not a claim
- Explicit CachedContent objects deliberately not built: hourly storage
  fees and TTLs fit a fixed prefix reused across sessions, while ours
  grows every round — implicit caching is designed for exactly this
  shape. Revisit only if the counter disappoints

## [0.13.0] - 2026-08-19

### Added — web access (ADR-0017, operator request)

- **`web_search(query)`** — Grounding with Google Search on the main
  model, per the operator's own call: plain search APIs barely exist and
  their terms froze the org's agentic-web-search project once already;
  grounding is first-party and ToS-clean. Answers return **with their
  sources** (title/domain/URI from the grounding metadata) so claims can
  be checked; a sourceless answer is flagged, not dressed up
- **`web_fetch(url, focus?)`** — the URL Context tool on the lightweight
  digest model, implementing the operator's suggestion exactly: fetched
  content goes through extraction and organisation, never raw into the
  main model. Three savings stack: the page bytes never enter the local
  process, never the digest model's output, never the main conversation.
  **Server-side fetching kills SSRF by construction** — localhost and
  the LAN are structurally unreachable; the mirror cost (no intranet or
  authenticated pages) is reported per URL with its retrieval status
- **Both tools are egress-gated by default** (`Mutating: true`): a query
  or URL is a channel where injected instructions could exfiltrate what
  the model can read. The ADR-0008 policy (`"web_search" = "never"`) is
  the deliberate per-operator relaxation — and it makes the tools usable
  in one-shot mode
- Digests and answers return as ordinarily nonce-wrapped tool results
  (no ADR-0010 exemption); the layer that cannot be wrapped — the
  server-side page — gets the defensive framing in the fetch prompt,
  stated as the weaker layer it is (ADR-0012's position)

## [0.12.1] - 2026-08-19

### Fixed

- file_info's magic table skipped the majors — operator review: "JPEG,
  PNG… quite a few big ones look unsupported". They were only caught by
  a fragile fallback (NUL-byte heuristic → stdlib sniffer). Now explicit:
  PNG, JPEG, GIF, WebP, TIFF, BMP, HEIC/AVIF/MP4/MOV (ftyp brands at
  offset 4), WAV/AVI (RIFF forms), MP3/Ogg/FLAC, tar (magic at offset
  257), 7z, xz, bzip2, zstd, RAR, WebAssembly, OLE compound documents,
  binary plists, and PEM text
- **The 0xCAFEBABE collision was a real bug**: every Java class file was
  reported as a Mach-O universal (fat) binary. Distinguished the way
  file(1) does — a fat header carries a small architecture count where a
  class file carries its version word
- Mach-O filetypes refined: dylibs, bundles, object files and dSYM
  companions are named instead of everything being "executable"
- Short ASCII magics ("BM", "ID3", "BZh", "OggS") now carry structural
  validity checks, so prose beginning with those letters stays text —
  misreading text as media is the exact mistake a type judgement exists
  to prevent

## [0.12.0] - 2026-08-19

### Added — file_info (ADR-0016, operator request)

- What a file *is*, without reading it into context: content-judged
  type (Mach-O incl. fat/64-bit, ELF, PE, zip, gzip, PDF, SQLite,
  shebang scripts, text/binary with a line count — the extension is
  shown but never trusted, and a mismatch is called out), size, mode
  with the executable bit named, modified time, and **birth time** —
  macOS-only by design, so the Darwin field costs nothing promised
- **MD5, SHA1, SHA256 in one streaming pass** — precisely the trio the
  org's malware-lookup MCP consumes, so identify → date → hash → look
  up runs in one agent loop with no approval-gated shell round.
  Oversized files (>512MB) skip hashing with a note
- `paths` array for a batch (one bad path is an in-batch error, never a
  hidden one); directories get entry counts, no hashes; an in-project
  symlink that escapes is a *reported fact* — target shown, nothing
  inspected — rather than a bare refusal
- The magic table is finite and test-first, not libmagic: it names what
  it knows and says `data` honestly otherwise

## [0.11.0] - 2026-08-19

### Added — edit_file v2 (ADR-0015, operator request)

- The anchor stays an exact unique string — the operator's instinct
  against line-number editing is right: a stale number writes to the
  wrong place *silently*, a string anchor fails loudly or works — and
  the tool gains what makes it cheap in rounds:
  - **`edits` array**, applied in order (each edit sees its
    predecessors' output) and **atomically**: any failure writes
    nothing and names the failing edit. Five changes are one call, not
    five history replays
  - **`replace_all`** per edit for renames, with the count reported;
    the uniqueness error now lists the occurrences' line numbers
  - **Diagnosed misses**: a whitespace-insensitive near-match is quoted
    with the file's *real* text and line — the way models actually miss
    is a tab — so the fix is a copy-paste instead of a re-read. In a
    batch, the diagnosis also reminds that earlier edits have already
    been applied
  - **Evidence on success**: the changed region with its line span (in
    the header, never as per-line prefixes — ADR-0014's rule), so
    verification needs no read-back round
- The intended loop end to end: windowed read → one batched edit →
  verify from the result. Nothing reads or writes the whole file unless
  the whole file is the point

### Changed

- The example config's `[model].summary` suggestion is now a concrete GA
  model (`gemini-3.5-flash-lite`) instead of a placeholder — operator
  review: a fallback tool's parts should not depend on a preview model's
  schedule. Measured working on the global endpoint, with and without
  the `google/` publisher prefix

## [0.10.0] - 2026-08-19

### Added — context economy (ADR-0014, operator request)

- **`read_file` line windows** — `start_line`/`end_line` (1-based,
  inclusive) read a slice instead of the whole file, annotated
  `[showing lines A–B of N]` so a partial view never masquerades as the
  full text. Content stays raw — no line-number prefixes, which would
  poison `edit_file`'s exact-match contract the moment the model copies
  what it read. Pairs with `search_files` output (`path:line`)
- **`summarize_file(path, focus?)`** — returns a short summary instead
  of the bytes; the history then carries the summary, and the saving
  repeats on every later round. The summariser model is
  `[model].summary` — the operator's requested lightweight slot,
  sharing the main model's client — defaulting to the main model, since
  the context saving does not depend on the model being cheaper
- The summariser is the compaction pattern applied to one file: content
  nonce-wrapped, defensive framing first, no tools offered; a blocked
  or empty summary is a reported error naming the reason and the
  fallback (read the file directly). The result names itself a lossy
  summary and names the model that wrote it. Summariser tokens go to
  the session log, not the footer — the context gauge tracks the main
  conversation
- The system prompt steers the read economy: windows for precision,
  summaries for gist, whole files only when the whole file is the point

## [0.9.0] - 2026-08-19

### Added — navigation tools (ADR-0013, operator request)

- **`list_tree`** — the project as an indented tree, recursive from an
  optional subdirectory. Orientation used to cost one round per
  directory (`list_files` only), and every round replays the whole
  history — fewer, smaller rounds is the cheapest optimisation this
  tool has. VCS internals are skipped (stated in the description);
  entry and depth caps are reported with the way to see more, never
  silent
- **`search_files`** — fast grep across the project: Go regex or
  `literal=true` for the exact string, results as `path:line: text`.
  Pure Go, no index, no ripgrep dependency — a backup tool must not
  acquire a prerequisite on the day it is needed, and "not RAG, fast
  grep" was the operator's own ceiling. Binaries are skipped by content
  sniff, oversized files and `.git` likewise; the match cap is reported
  when hit
- Neither tool follows symlinks: a walk that follows links can leave
  the project through a link the per-path checks never see (the
  out-of-project-symlink case is covered by a test that plants one)
- The system prompt steers: orient with `list_tree`, locate with
  `search_files`, then `read_file` the specific files — reading files
  wholesale to find something is the most expensive possible search

## [0.8.0] - 2026-08-19

### Added — image input (ADR-0012, operator request)

- **Screenshots are first-class input.** Three operator routes through
  the existing `@` mechanism:
  - `@path/in/project.png` — as any reference
  - `@/absolute/path.png`, `@~/Desktop/shot.png` — images (and only
    images) may come from anywhere, because `@` is parsed from what the
    operator types, never from model output or tool results
  - `@clipboard` — the clipboard image via macOS osascript:
    Cmd+Ctrl+Shift+4, then `@clipboard ここがおかしい`
- **`view_image`** — the model's own route, prompted by the operator's
  observation that MCP servers *produce* images the agent must look at
  (urlscan's `get_screenshot`, pcap extraction). Read-only and
  project-confined exactly like `read_file`; the pixels ride inside the
  function response as a multimodal response part. That shape was
  chosen by measurement: the obvious alternative — a user message
  appended after the tool round — passed one round and 400'd the next
  on Gemini's call/response pairing rules
- MIME is sniffed from bytes (a renamed binary is refused), images cap
  at 8MB each and 4 per message, and an oversized image is refused
  whole — a truncated PNG is a broken file, not a smaller picture
- Images cannot be nonce-wrapped, so the isolation stance gets a stated
  visual counterpart in the system prompt: text visible inside an image
  is data, never instructions. Weaker than tag isolation, and said so
- Transcripts store image bytes (base64), so a resumed session keeps
  the screenshots it was looking at; the compaction summariser sees
  `[image: ref, N bytes]`, never bytes. `read_file` refuses images and
  points at `view_image`
- Verified live with a solid-red PNG: all three `@` routes and
  `view_image` (colour-neutral filename, follow-up tool round,
  signature replay intact) answered correctly

## [0.7.0] - 2026-08-19

### Changed — skills move to gem-agent's own directory (ADR-0011, operator review of v0.6.0)

- v0.6.0 read `~/.claude/skills/` directly — Claude Code's live
  environment. The operator caught it within hours: that is environment
  mixing (skills installed *for Claude Code* may assume its tools and
  context, and the fallback's behaviour would change whenever the
  primary's environment does), and it broke the symmetry MCP already
  settled — Claude Code's *format*, gem-agent's *location*
- The global scope is now `~/.config/gem-agent/skills/`, exactly the MCP
  arrangement; `~/.claude/` is no longer read at all. The project scope
  (`<project>/.claude/skills/`, shared) is unchanged — a repository is
  the project's environment, not either tool's
- **Sharing with Claude Code is a symlink the operator makes**, per
  skill or wholesale — discovery follows links, and the read confinement
  applies to the resolved directory:
  `ln -s ~/.claude/skills/<name> ~/.config/gem-agent/skills/<name>`
- Scope labels now match MCP's: `[global]` / `[project]`. The `/skills`
  empty state prints both paths and the sharing recipe

## [0.6.0] - 2026-08-19

### Added — skills (ADR-0010, operator request)

- gem-agent now reads **Claude Code's skills, as-is**:
  `~/.claude/skills/<name>/SKILL.md` and
  `<project>/.claude/skills/<name>/SKILL.md`, same format, same
  locations, nothing to migrate. A skills-series zip unpacked into the
  personal directory serves both agents — the procedures the operator
  wrote down stop being unavailable exactly when the fallback is in use
- Progressive disclosure: each skill contributes one description line to
  the system prompt; bodies load only when used. The model calls
  `load_skill(name)` when the task matches a description — and
  `load_skill(name, file)` for the skill's own `references/` and
  `scripts/` — or the operator types `/skill <name> [args]`, which
  injects the body directly with no extra model round. `/skills` lists
  what was found, with argument hints
- **Skill content is instructions, not data**: `load_skill` is the one
  tool whose results skip the nonce wrap. A skill body is a file the
  operator installed — the same trust tier as the `AGENTS.md` already
  injected unwrapped — and wrapping it while the system prompt forbids
  following wrapped content would leave every skill half-inert. The
  exemption is bounded: reads are confined to discovered skill
  directories, symlinks resolved and re-checked
- Frontmatter is parsed minimally; `allowed-tools` is deliberately
  ignored — gem-agent has its own approval model (ADR-0004/0008), and
  honouring a foreign permission grant would bypass it. The project wins
  a name collision, announced like an MCP one; a skill without a
  description is skipped with a note
- Verified live with a project-scoped skill: both invocation paths ran
  the procedure — including the supporting-file fetch — and produced the
  exact prescribed output

## [0.5.1] - 2026-08-19

### Fixed

- Closing `/settings` with Esc left the input line one row higher than it
  started. The panel's view was **taller than the terminal** — 42-43
  lines on a 40-line screen — which scrolls the screen and drifts the
  inline renderer's line accounting by exactly the overflow. It is the
  same failure as the resize staircase, on the other axis
- The row budget was a guessed margin (`height - 8`) that did not account
  for section headings, the two "… more" markers, the scope line, the
  footer, or the trailing newline. It is now derived from the real
  chrome, and a window's cost is counted exactly — the first rendered row
  always prints its section heading, even when it shares one with the row
  above, and that single uncounted line was enough to go over
- A terminal too short for any honest layout (under 8 lines) now says so
  instead of overflowing
- Long MCP tool names pushed the value column out of alignment: padding
  is computed on the plain text before styling, since `%-Ns` counts bytes
  and a styled label is mostly escape sequences

## [0.5.0] - 2026-08-19

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

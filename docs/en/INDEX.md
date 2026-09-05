# Documentation Index

Entry point for gem-agent's maintainer-facing documentation. For
user-facing material see [`README.md`](../../README.md).

Japanese mirror: [`INDEX.ja.md`](../ja/INDEX.ja.md). `scripts/docs-mirror-check.sh`
enforces the structural half in `make check` — every `docs/en` file has its
`docs/ja` counterpart and back, the ADR catalogue is complete and ordered in
both, and the code spans of each pair agree. Prose parity is the author's job.

## Specification

- [`gem-agent-rfp.md`](gem-agent-rfp.md) — the canonical spec: problem
  statement, functional surface, scope boundaries, phase plan. Features
  outside it need an ADR, which is how session resume and context
  compaction got in.

## Reference

Current behaviour. Evergreen — updated in place as the code changes.

Feature references (the README links here; one domain per file):

- [`reference/interface.md`](reference/interface.md) — TUI, plain REPL,
  one-shot, keys, slash commands, completion, `/settings`, theme and
  UI language
- [`reference/tools.md`](reference/tools.md) — every built-in tool and
  the design decision behind it
- [`reference/attachments.md`](reference/attachments.md) —
  @-references: files, images, documents, audio/video, the GCS route
- [`reference/approval.md`](reference/approval.md) — MITL gates,
  auto-approve, the per-tool policy, sandbox, startup safety,
  untrusted-content isolation
- [`reference/sessions.md`](reference/sessions.md) — transcripts,
  resume, state layout, compaction, `/usage`, agent memory
- [`reference/integration.md`](reference/integration.md) — project
  instruction files, MCP servers, skills
- [`reference/configuration.md`](reference/configuration.md) — install,
  the config file, precedence, flags, telemetry, content filters,
  endpoints

Project references:

- [`reference/architecture.md`](reference/architecture.md) — package
  layout, the turn loop, the two confinement boundaries, persistence,
  and failure behaviour in one table
- [`reference/drill.md`](reference/drill.md) — the on-demand health
  check (the former monthly drill, ADR-0061): what rots on its own, the
  procedure that catches it, and the record of the first run
- [`reference/promotion.md`](reference/promotion.md) — closed record of
  the lab-series → cli-series bar and the 2026-09-01 promotion decision
  that superseded it (ADR-0061)

## ADRs

Point-in-time design decisions. Immutable once accepted; a changed
decision gets a new ADR that supersedes the old one (typo and link fixes
excepted).

- [`ADR-0001`](adr/0001-sandbox-mechanism.md) — sandbox-exec plus MITL:
  two boundaries, one for decisions and one for containment
- [`ADR-0002`](adr/0002-tui.md) — Bubble Tea in inline mode; alt-screen
  rejected to keep native scrollback and copy/paste
- [`ADR-0003`](adr/0003-bottom-pinned-layout.md) — pinning the input to
  the window bottom without alt-screen
- [`ADR-0004`](adr/0004-auto-approve.md) — auto-approve as a two-tier
  ladder: a rule floor the model cannot lift, then a model judgement
- [`ADR-0005`](adr/0005-session-resume.md) — the session log becomes the
  resume source of truth; refusals over warnings on project and model
- [`ADR-0006`](adr/0006-context-compaction.md) — summarise the older
  half instead of failing at the window; fail safe, never fail small
- [`ADR-0007`](adr/0007-input-during-a-turn.md) — typing during a turn is
  kept and queued; auto-sent only when the turn finished cleanly
- [`ADR-0008`](adr/0008-per-tool-approval-policy.md) — per-tool approval
  policy; a project may tighten freely and loosen only where trusted
- [`ADR-0009`](adr/0009-settings-panel.md) — a settings panel showing
  provenance, and a machine-owned policy file so comments survive
- [`ADR-0010`](adr/0010-skills.md) — Claude Code's skill format read
  as-is; skill content is instructions, bounded by confined reads
  (location clause superseded by 0011)
- [`ADR-0011`](adr/0011-skill-scope-separation.md) — skills live in
  gem-agent's own directory; sharing with Claude Code is a symlink
- [`ADR-0012`](adr/0012-image-input.md) — image input: operator-attached
  via @ (clipboard included), model-viewed via view_image
- [`ADR-0013`](adr/0013-navigation-tools.md) — list_tree and
  search_files: orientation and fast grep, no index, no dependencies
- [`ADR-0014`](adr/0014-context-economy-tools.md) — summarize_file on a
  lightweight model, and line-window reads for read_file
- [`ADR-0015`](adr/0015-edit-file-v2.md) — edit_file v2: batched atomic
  edits, diagnosed misses, evidence on success
- [`ADR-0016`](adr/0016-file-info.md) — file_info: content-judged type,
  metadata, and the MD5/SHA1/SHA256 trio
- [`ADR-0017`](adr/0017-web-tools.md) — grounded search and digested
  fetch; egress-gated by default, SSRF dead by construction
- [`ADR-0018`](adr/0018-context-caching.md) — a session-scoped isolation
  tag makes implicit caching fire; measured 0% → 81–95%
- [`ADR-0019`](adr/0019-usage-accounting.md) — per-category usage
  accounting and /usage; side-calls stop stomping the footer
- [`ADR-0020`](adr/0020-agent-memory.md) — agent memory across sessions:
  two scopes, machine-owned outside the repo, writes are the trust
  boundary
- [`ADR-0021`](adr/0021-review-fixes.md) — whole-code review fixes:
  transcript clear/tear/lock, the allowlist floor, scope-first policy,
  and two refuted-by-measurement findings
- [`ADR-0022`](adr/0022-per-project-session-layout.md) — per-project
  session subdirectories (memory's convention), legacy read in place,
  and GEMAGENT_STATE_DIR isolation
- [`ADR-0023`](adr/0023-startup-safety.md) — startup safety: broad
  roots confirm, and one first-run trust question covers a project's
  instructions, .mcp.json, and skills
- [`ADR-0024`](adr/0024-bottom-hold.md) — bottom-hold: the frame's total
  height is held once the screen is full, so the footer stops bouncing
  (supersedes ADR-0003's full-screen clause)
- [`ADR-0025`](adr/0025-thinking-level.md) — configurable Gemini 3
  thinking level for main-model calls; summary model unaffected,
  supported levels model-dependent (measured)
- [`ADR-0026`](adr/0026-document-reading.md) — document reading: PDF
  native as measured multimodal parts, Office XML extracted locally,
  legacy binaries out of scope
- [`ADR-0027`](adr/0027-audio-video.md) — audio/video input: a
  configured bucket always wins over inline (round-replay economics),
  content-addressed uploads, nothing deleted
- [`ADR-0028`](adr/0028-self-healing-line-counter.md) — the printed-line
  counter follows reality: over-tall frames scroll the terminal, and
  the counter self-heals by the overflow (amends ADR-0003's definition)
- [`ADR-0029`](adr/0029-ui-language.md) — UI language mode:
  `[tui].language` auto/ja/en, one catalog struct with two complete
  literals, completeness enforced by test; log-shaped and model-facing
  text stays English
- [`ADR-0030`](adr/0030-agent-self-info.md) — `agent_info`: a read-only
  self-information tool (model, context occupancy, usage, limits,
  platform); fields earn their place by changing model behavior — GCP
  identifiers and hostname withheld
- [`ADR-0031`](adr/0031-review-fixes-round-2.md) — review round 2:
  ~30 fixes (Msgs wiring, denial bypass, media-store poisoning,
  resume-under-flock, rune-safe completion, adaptive approval budget,
  flocked policy mutations, docext aggregate cap); one 400 claim
  refuted by measurement; four non-changes recorded
- [`ADR-0032`](adr/0032-datetime-tool.md) — `datetime`: one read-only
  tool for the clock and calendar arithmetic (now/info/add/diff/
  convert); month-end normalization disclosed, business days refused;
  session-start date rides the system prompt cache-stably
- [`ADR-0033`](adr/0033-turn-observability.md) — turn observability:
  a stream heartbeat and stall warning, visible backoff retries, and
  ephemeral live thought summaries (displayed, never stored)
- [`ADR-0034`](adr/0034-cancellation-deadlock.md) — cancellation ends
  the call: process-group kill + WaitDelay (a grandchild holding the
  pipe hung timeout AND Ctrl+C), and a three-press last-resort exit
- [`ADR-0035`](adr/0035-opentelemetry-audit.md) — OpenTelemetry audit
  logging: OTLP log events, default off, global-config-only (the
  exporter is an egress channel), metadata never payloads, and
  telemetry that never hurts the session
- [`ADR-0036`](adr/0036-ask-user-tool.md) — `ask_user`: a structured
  mid-turn choice on the approval dialog's grammar; declining is
  information; every mode answers honestly; no free-text by design
- [`ADR-0037`](adr/0037-agentic-file-search.md) — `agentic_file_search`:
  delegated project search in an isolated child context — read-only
  allowlist, no recursion, labeled telemetry, ADR-0014 generalised
  from one file to one question
- [`ADR-0038`](adr/0038-risk-eval-instruction-context.md) — the
  auto-approve model tier sees the operator's typed request for a
  turn's first rounds: evidence-wrapped, misalignment escalates, late
  rounds fall back byte-identically to the call-only view
- [`ADR-0039`](adr/0039-integration-reload.md) — `/skills reload` and
  `/mcp reload` reuse the startup paths and the startup trust verdict;
  declarations and the system prompt follow; `--mcp on|off` for
  one-shot pipelines; reloads are audited
- [`ADR-0040`](adr/0040-round-limit-intervention.md) — the round limit
  becomes an intervention ladder: loop detector, progress review,
  operator dialog (auto mode continues itself on a confident verdict),
  a 3× cap no verdict can lift, and a stop message that teaches
  "continue" instead of /clear
- [`ADR-0041`](adr/0041-review-round-3.md) — whole-code review round 3:
  16 findings, 3 high (the child agent expanded model-authored @refs,
  the live region's tab width hole, a second stdin reader), plus the
  stall detector, ask dialog, and audit-gap fixes
- [`ADR-0042`](adr/0042-terminal-diagrams.md) — mermaid diagrams
  draw in the terminal: the measured-faithful types only (flowchart,
  ASCII sequence, ER), shapes normalized to boxes, a fidelity guard,
  the rest shown as source (the FIT rule and the prompt section were
  later removed by 0063)
- [`ADR-0043`](adr/0043-diagram-tool.md) — diagrams are drawn by a tool, not by rewriting what the model wrote (superseded by 0063)
- [`ADR-0044`](adr/0044-pre-tool-hooks.md) — operator pre-tool hooks: the org's guards survive the fallback
- [`ADR-0045`](adr/0045-transcript-approval-learning.md) — transcript-driven approval-rule learning: `/learn` proposes, the operator decides (withdrawn by 0049)
- [`ADR-0046`](adr/0046-mcp-description-risk-evidence.md) — MCP tool descriptions as risk-evaluation evidence: tell the evaluator what the operator already installed
- [`ADR-0047`](adr/0047-declared-purpose.md) — model-declared purpose on gated calls: the operator sees why, not only what
- [`ADR-0048`](adr/0048-learning-that-fires.md) — learning that fires on real usage: server-scoped MCP rules, and counting the answers people actually gave (withdrawn by 0049)
- [`ADR-0049`](adr/0049-learn-withdrawn.md) — `/learn` is withdrawn: confirmation was not a durable boundary for loosening
- [`ADR-0050`](adr/0050-risk-calibration.md) — the risk rulebook: layered guidance for the judge, and learning is one way to write it
- [`ADR-0051`](adr/0051-destructive-rewrite-floors.md) — whole-file rewrites that shrink are a red flag: the shrink guard, the regeneration rule, the compaction staleness notice, and the dialog size delta
- [`ADR-0052`](adr/0052-ignore-aware-navigation.md) — ignore-aware navigation: the walks skip generated/ignored content (built-in list + full gitignore semantics, no new dependency), search answers "where", list_tree budgets per directory (amends ADR-0013's premise)
- [`ADR-0053`](adr/0053-one-shot-approval-controls.md) — one-shot approval controls: `--auto` arms the ADR-0004 ladder headless (escalations become explained denials; the config key stays ignored — the grant belongs on the invocation), `--allow` grants per-run `"never"` entries through the normal policy build, and SessionStart reports the effective auto state
- [`ADR-0054`](adr/0054-risk-context-every-round.md) — the risk evaluator sees the operator's instruction in every round: ADR-0038 §3's cutoff measured against real transcripts (70% of evaluations and 63% of turns' terminal gated calls fell outside it) and removed; egress rubric, tiers, and confidence bar unchanged
- [`ADR-0055`](adr/0055-piped-stdin-as-data.md) — piped stdin in one-shot mode becomes a nonce-wrapped text attachment (the `@`-file lane), never prompt text: the `-p` string alone stays the risk evaluator's instruction channel; bounded read with a disclosed clip, binary skipped, terminal stdin never read
- [`ADR-0056`](adr/0056-stall-warning-threshold.md) — the stall warning cried wolf: a Gemini function call arrives as one whole part, so composing a large write/edit argument is measured minutes of dead wire (not one byte, let alone a chunk); the threshold moves 20s → 90s and nothing is added to the screen — the supplier's constraint belongs in this ADR, not in the status bar; `/riskbook learn` finally sets its suppression flag after `beginTurnStats`
- [`ADR-0057`](adr/0057-usage-accounting-records.md) — every model call leaves one `usage` record (source, model, prompt/output/thoughts/cached/total): the API never reports money, so cost is token counts × catalog price and the counts must be on disk at call time; risk and compaction spend used to die with the process, and thoughts bill as output while cached is a discounted share of prompt (both measured)
- [`ADR-0058`](adr/0058-session-work-directory.md) — a work directory per session (state root, keyed by session id, resume lands back in it): a sandbox write root and a second root of the file tools, exported as `GEMAGENT_WORK_DIR`. MCP results were the one tool output never bounded, which is why every file-mediated server grew a `workspace_root` — a server cannot know the model's context window; oversized results are now saved here rather than truncated, and non-text content (silently discarded until now) is saved for `view_image`
- [`ADR-0059`](adr/0059-workdirs-cleanup-command.md) — `gem-agent workdirs` list + `clean`: the cleanup half of ADR-0058's accumulation note (a report without a remedy trains people to ignore it). Confirmation is the default with deny-on-EOF, a live session's directory is never deleted (shared-flock probe on its transcript), and cleaning stays per-project and CLI-side — freeing disk must not require a model session
- [`ADR-0060`](adr/0060-deny-with-reason.md) — deny with reason, the `N` answer: the fixed denial text spent a model round fetching a reason the operator knew when they pressed `n`, and the denial's own function response is the one slot the API leaves open mid-round (ADR-0012). `n` stays a one-keystroke deny; denial results ship unwrapped by message provenance, never by content (a denial-shaped tool output stays wrapped); the reason lands in `gate_decision` but not in telemetry
- [`ADR-0061`](adr/0061-independent-runtime-promotion.md) — independent agent runtime: the backup charter is retired (real-world deployment outgrew it), drop-in compatibility stays the top requirement with an ecosystem-compatibility rationale, scope minimalism stands on its own charter instead of "20% of Claude Code", the drill becomes an on-demand health check, and gem-agent is promoted to cli-series by operator decision — the drill-based bar superseded, not passed
- [`ADR-0062`](adr/0062-delegation-first-exploration.md) — delegation-first exploration: 75 sessions / 788 tool calls held zero spontaneous agentic_file_search firings because the system prompt prescribed the manual list/search/read loop by name and never mentioned the tool — a description-level trigger cannot outcompete a prompt-level workflow. The prompt now routes exploration to delegation first (self-navigation is the known-target path), the description gains "trust the report", and the wiring is pinned by tests
- [`ADR-0063`](adr/0063-diagram-fences-render-in-place.md) — diagram fences render in place and the runtime says nothing about diagrams: two months measured the tool firing once while the model hand-drew box art instead (the fence prohibition over-generalized — a specific prohibition beside a vague recommendation reads as "rarely"). The fence path returns as a view-layer rewrite, the FIT gate is deleted (overflow wraps and loses nothing — measured), the wrongness guards stay, and an attempted draw that fails keeps the source plus a one-line reader-facing note
- [`ADR-0064`](adr/0064-first-message-argument.md) — a positional argument is the first interactive turn: `gem-agent "message"` submits it after the banner through the exact typed path (shell escape, slash, /skill, mentions), fires exactly once, composes with `--continue`/`--resume`/`--auto` unchanged, refuses combination with `-p`, and leaves ADR-0055's piped-stdin boundary untouched
- [`ADR-0065`](adr/0065-cancellation-in-process.md) — cancellation ends the call, part 2: the file walks consult the context and return a named partial result, a return-guaranteed floor under every tool call abandons a wedged call after a 1 s grace (audited as `abandoned`, counted on the exit receipt, recorded on a late return, and a late mutating return announced next turn; `ask_user` exempt), the three-press escape ladder reaches the plain REPL and `-p`, and the exit says when it is flushing audit events
- [`ADR-0066`](adr/0066-tool-prompt-usage-bucket.md) — the fourth bucket: the SDK defines `totalTokenCount` as the sum of four counts and ADR-0057's checksum had three, because its probe (the main loop) never enables a built-in tool — so every `web_search` / `web_fetch` record whose tool returned content failed the check it was meant to pass. `usage` records now carry `tool_prompt` (always written, zero included; a missing key marks a pre-0066 record), the checksum is `prompt + output + thoughts + tool_prompt == total`, and `model.usage` gains `tool_prompt_tokens`
- [`ADR-0067`](adr/0067-piped-stdin-wait-notice.md) — a one-shot run waiting on piped stdin says so: `-p` still reads a non-terminal stdin to EOF (a slow producer is never cut off), but a pipe still open after 2 s earns one stderr line naming both remedies (close the pipe, or launch with `< /dev/null`), and an announced wait is seen to end — the idle inherited pipe a scheduler or harness hands a child no longer passes for a hang
- [`ADR-0068`](adr/0068-telemetry-resource-declared-not-detected.md) — the telemetry client declares its Cloud Logging resource instead of probing for one: with `backend = "gcp"` the library's Logger classified the host by fetching from the GCE metadata server, and on a Mac that link-local fetch intermittently cost 4.5–7.2 s of silent startup (dial timeouts retried as transient while the kernel probed ARP for a neighbour that does not exist); the `global` resource the detection would have fallen back to is now declared, construction touches no network, and no wait notice is added because the wait was never a contract
- [`ADR-0069`](adr/0069-session-and-prompt-hooks.md) — session-start and prompt-submit hooks: `[[hooks.session_start]]` (sources `startup`, `resume`, `clear` on `/clear`; optional `matcher`) and `[[hooks.user_prompt_submit]]` (every turn that reaches the model) run on Claude Code's measured contract — the same stdin payloads, plain stdout or `hookSpecificOutput.additionalContext` as context, exit 2 or the JSON block forms refusing a prompt (erased, nothing recorded), a session start never blockable — and their output reaches the model as a data attachment beside the typed input (ADR-0055's lane, capped at 8000 runes, announced), never the system prompt and never the risk evaluator's trusted instruction channel
- [`ADR-0070`](adr/0070-skill-directory-and-shared-writable-roots.md) — a loaded skill names its directory, and the rule tier's writable places are the sandbox's: `load_skill(name)` and `/skill <name>` open with Claude Code's own line `Base directory for this skill: <dir>` (the symlink-resolved directory reads are already confined to), so a `SKILL.md` written to Claude Code's `SKILL_DIR` contract can run its scripts from gem-agent's global skill directory instead of searching the disk for them; `sandbox.ScratchDirs()` is the one list of scratch roots (`TMPDIR`, `/private/tmp`, `/dev`) that both the profile and the redirect rule read, so `2>/dev/null` no longer reads as a write outside the roots; and a read-only tree walk (`find`, `fd`, `du`, `rg`, recursive `grep`) starting at `/`, `~`, or an absolute path outside the roots is Review, not Safe — the read side of the sandbox is open by design, so the walk's cost is the mounts', and a cost is the model tier's to weigh
- [`ADR-0071`](adr/0071-session-identity-contract.md) — the session identity contract, aligned with Claude Code: a UUID v4 session id (timestamp ids still list and resume; `--resume` takes an unambiguous prefix), `/clear` starts a new session (`SessionEnd` then `SessionStart`), the project is a directory exported as `GEMAGENT_PROJECT_DIR` (no minted, stored or git-derived project identifier), and `[[hooks.session_end]]` joins the events; state layout, transcript format and payload shapes unchanged
- [`ADR-0072`](adr/0072-whole-code-review-round-4.md) — whole-code review round 4: forty-one findings from a maintainer pass and five independent reviewers, all but four fixed — a pre-tool hook's refusal no longer lets `view_image` attach its bytes; the rule tier reads a command the way bash runs it (newlines separate, `/bin/rm` is `rm`, `find -exec` and `sed -i` are not read-only, `<(…)` is dynamic); writes under `.git/` are Block and writes to `AGENTS.md`, `.mcp.json` and the other instruction/configuration files are Review only the operator answers (`OperatorOnly`); `/clear` rotates every consumer of the work directory and no longer freezes the TUI under a session hook; `session_start` on `/clear` has source `clear` (ADR-0071 §4 amended)
- [`ADR-0073`](adr/0073-capability-lanes-for-shell-exec.md) — capability lanes for `shell_exec`: the 103 findings of ADR-0072 classified by root cause; the kernel, not the command text, decides what a shell command may do — three Seatbelt lanes (`read` runs unasked and denies writes, network, preference writes, signals and IPC-capable programs; `write` asks and denies the persistent files; `operator` is the operator's alone), the rule tier reduced to a Block floor that can only escalate, one list each for scratch, persistent and credential paths read by the profile and the file tools, and `internal/archtest` pinning confined opens, bounded reads (`internal/bounded`) and the single decision point
- [`ADR-0074`](adr/0074-trust-is-granted-to-content.md) — trust is granted to content, not to a path: the agent-facing files a trusted project provides (instruction files, `.mcp.json`, `.gem-agent.toml`, skills) are pinned by SHA-256 when trusted and a changed one asks again before it is loaded (`-p` leaves it out and says so; `gem-agent trust --accept` re-pins); the write lane denies renaming the parent directories of existing persistent files; what a session added or changed among persistent files is reported at `/clear` and exit; `[approval].pin_trusted_files` opts out
- [`ADR-0076`](adr/0076-the-configuration-home-stays-readable.md) — **(Proposed)** the operator's configuration home stays readable to the lanes: a first draft denied `~/.config`, and its review found one deny breeding four re-allow rules, two ordering rules, symlink resolution and an operator-maintained list — the kernel sees the syscall, not the intent, and the tools the read lane exists for read their settings there; the behaviour is addressed at its trigger (ADR-0075) instead. ADR-0011 §3 amended: a skill installed for Claude Code is copied, not linked — `~/.claude` is a credential store the lanes cannot read and Seatbelt matches the resolved path; the `/skills` empty state prints the copy command

## History

Frozen audit trail of superseded documents. Empty so far — nothing has
been superseded yet. When something is, it moves here with a note saying
what replaced it, rather than being deleted: the discussion in a
superseded document is often the only record of why the current design is
shaped as it is.

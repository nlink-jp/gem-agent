# ADR-0031: Whole-code review round 2 — findings and fixes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-21 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the codebase changed extensively (ADR-0022–0030) — review the whole of it again |

## Context

Five parallel review lenses (concurrency/fs, TUI invariants, security
boundaries, LLM data shapes, config/catalog consistency) were run over
the ~4,200 lines added since the first review (ADR-0021), each finding
verified against the code — and where the claim was measurable, by
measurement — before being accepted. Machine checks (full `-race` run,
vet, govulncheck) were all clean; every defect below was found by
reading, and none by a failing test, which is itself the lesson the
first review taught: the tests pin what was already fought for, not
what was never wired.

## Fixed

### 1. Wiring and language (ADR-0029 aftermath)
- **`Msgs` was never passed to the TUI** — the catalog was resolved in
  cmd and handed to prompts and slash output, but the `tui.Options`
  literal omitted the field, so the entire TUI chrome fell back to
  English. One line — and an AST-level wiring test that fails when any
  required field disappears from the Options literal, because no
  behavioral test can reach that literal without running the REPL.
- Plain-REPL interrupt/error/bye lines, the shift+tab toggle notice,
  `/mcp` output, and "unknown command" were hardcoded English on
  surfaces the ADR-0029 scope covers — all moved into the catalogs.
- `/mcp` claimed MCP tools "always require approval"; auto-approve's
  risk tier can pass routine calls and 'a' covers a tool for the
  session. The text now says what the gate actually does.

### 2. Approval integrity
- **A denied `view_image`/`read_document` still attached the
  bytes**: the attach branches screened only for the `error:` prefix
  and the denial string does not carry it. The denial is now a named
  constant both branches recognize.
- A tool call reached after Ctrl+C cancelled the turn could still
  open an approval dialog (and an 'a' answer would allowlist on
  behalf of a dead call): execCall refuses on a cancelled context, and
  the TUI auto-denies gate requests that arrive between the interrupt
  and TurnDone.
- On short terminals the approval frame overflowed and the View clamp
  cut rows FROM THE TOP — the title first — with no disclosure. The
  detail budget now adapts to the height; title and options always
  render, and the hidden count stays honest. `clipDetail` also counted
  hidden lines after its 600-rune pre-clip, under-reporting.

### 3. Session and state integrity
- **Resume loaded the transcript before Reopen took the flock** — the
  gap the lock exists to close. The history is now loaded under the
  lock, from the open Logger's own path.
- `lockSession` mapped every flock errno to "already in use" and left
  the just-created O_EXCL file behind on failure; errnos are now
  distinguished and the exact-path file removed.
- statedir's second marker read concluded "non-empty therefore ours"
  without comparing — a colliding project's marker landing between the
  two reads produced a success built on a false ownership premise. The
  second read re-verifies.
- policy.toml was rewritten whole from each process's startup
  snapshot; a stale concurrent writer could resurrect a trust the
  operator had just declined. All mutations now go through
  `MutatePolicyFile`: flock, fresh load, single change, save.

### 4. Media store (ADR-0027 aftermath)
- The content-addressed invariant had two holes: the file was hashed,
  closed, and re-opened by path (rename-replace between passes stored
  different bytes under the old hash), and a failed `io.Copy` still
  `Close`d the GCS writer — which FINALIZES the partial buffer as the
  complete object. Both poisoned the permanent store silently. Now:
  one file descriptor for both passes, a verifying reader that
  re-hashes during upload and fails the stream on mismatch, and a
  cancelable writer context so an aborted copy commits nothing.
- Uploads ignored the turn context (Ctrl+C could not reach a
  multi-minute upload) — the callback now takes the turn's ctx.
- `sync.Once` pinned a transient client-construction error for the
  session; only success is cached now.
- `GOOGLE_CLOUD_QUOTA_PROJECT` stayed in the process env forever and
  leaked into every later `shell_exec` child; it is now restored right
  after client construction (verified by a live upload).

### 5. Resource bounds
- docext had a per-member cap but no aggregate: many under-cap
  sheets/slides accumulated unbounded text before the final clip —
  `read_document` being ungated made a crafted workbook a
  model-reachable memory exhaustion. Extraction now stops at the text
  budget. The mention path's Office branch also gained the 32MiB file
  cap its PDF sibling already had.

### 6. Smaller honesty fixes
- Tab completion's longest-common-prefix trimmed bytes: 資料/説明
  share a UTF-8 lead byte and the completer wrote a lone 0xE8 into
  the input box (measured). It trims runes now.
- The running view never rendered the input box ADR-0007 promises the
  operator can see; it does now.
- `emit` assumed perfect cell packing; a double-width rune straddling
  the wrap column wraps whole, and the uncounted row drifted the
  bottom pin. The count now simulates the terminal's greedy wrap.
- Clean turn completion never cancelled the per-turn context (one
  leaked child per turn on the session context); the 'p' answer was
  the last two-write event (slow-terminal flash); the settings panel
  overflowed by one row at height 8 (min raised to 9).
- `/settings` gained the two tracked-but-unshown rows
  (`agent.compact_at_pct`, `mcp.enabled`), the theme row became
  read-only with a restart note (it was editable and applied nothing),
  the language row shows what `auto` resolved to, and panel edits
  show `session` provenance instead of crediting a file that never
  held the value.
- `agent_info` labels the MCP list as the startup snapshot and states
  when project trust withheld the project's own tools; the trust
  prompt counts skill directories with a SKILL.md, not raw entries;
  `/exit` is completable and documented as an alias; tool-message
  attachments (view_image/read_document PDFs) carry the same
  untrusted-data note user attachments get; `estimateTokens` counts
  tool-call args and inline bytes; a 400 with `[model].thinking` set,
  or with a gs:// attachment in history, names the knob or the way
  out; empty `[gcp].location` fails at config load, not in the client.

## Refuted by measurement

The claim that a transcript ending in an unanswered FunctionCall (a
hard kill mid-tool-round) makes every resumed turn fail with 400: a
transcript cut exactly there was resumed against the live API and
answered normally — consistent with the first review, where two
"certain" 400 claims also fell to measurement. No sanitizer was added.

## Deliberately not changed

- MCP tools structurally cannot reach the Block tier, so a session
  'a' covers any later arguments — ADR-0004's design; the `/mcp` text
  now says so instead of overstating.
- `/skill`-injected skill bodies pass through @-expansion, so a
  malicious skill could attach off-project files — bounded by skills
  loading only from the operator's own directory or a trusted
  project, and recorded here rather than coded around.
- statedir's write-write first-launch race (two different projects
  claiming a fresh dir simultaneously) remains last-rename-wins: the
  marker is a best-effort disambiguator, not a lock.
- `.project.tmp-*` files from a crash between CreateTemp and Rename
  are not swept: cleanup would need wildcard deletion, which this
  operator's deletion rules forbid; the litter is harmless.

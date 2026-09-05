# ADR-0072: Whole-code review round 4 — findings and fixes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the recent feature work changed a great deal of code — read the whole of it once and inspect it |
| Amends | ADR-0004 (rule tier), ADR-0020 §4 (generalised to instruction and configuration files), ADR-0071 §4 (the `/clear` source is `clear`) |

## Context

Thirty releases landed between the last whole-code review (ADR-0041,
v0.36.1) and this one: 133 commits and 26,700 added lines, among them
the cancellation floor (ADR-0065), the data lane (ADR-0055), the
context hooks (ADR-0069), the skill directory and shared writable
roots (ADR-0070), and the session identity contract (ADR-0071). The
method was ADR-0041's: a maintainer pass over the agent loop, five
independent reviewers in parallel (agent/llm, cmd wiring, TUI and
diagram, security boundaries, persistence and infrastructure), `go vet`,
the race detector over every package, `govulncheck`, `gosec` and
`golangci-lint` — and every reviewer claim re-verified against the code
or a probe before it counted. The machine checks were clean; every
finding below came from reading or from a probe. Forty-one findings
(five high), all but four fixed; the four are recorded in §3 with the
reason.

## 1. Fixed — the boundaries

### 1.1 A pre-tool hook's refusal did not keep the pixels out (high)

`view_image` and `read_document` attach their bytes to the tool message
when the call was not refused. Round 2 keyed that guard on the gate's
denial flag; a pre-tool hook (ADR-0044) refuses *before* the gate, and
its result text carries neither the flag nor the `error:` prefix — so
the image or PDF rode along with the refusal. `execCall` now reports
`ran`, true only when the tool's `Run` was reached and returned on its
own, and the attach branches key on that: a refusal from any layer —
gate, hook, cancel floor — keeps the bytes out. A hook denial is also
audited as `denied`, not `ok`.

### 1.2 A cancel during the ladder reached the gate (medium)

`execCallInner` checked the context once at entry. A Ctrl+C landing
while a pre-tool hook ran, or during the model-tier risk review (a
network round trip), came back as an escalation and reached
`gate.Approve`: the TUI's auto-`n` then recorded a `gate_decision` no
human made, and the plain REPL's prompt ate the next stdin line as its
answer. The context is re-checked after the hook and after the ladder;
the call is `interrupted`, as `roundIntervention` already did.

### 1.3 The rule tier read the shell differently from bash (high)

Probes over about 160 command strings found the rule tier's model of a
command line too narrow in five ways. All are Review unless noted; the
defect class is *Safe on a mutating command*, which in auto mode runs
with no prompt and no model call.

- **Newlines.** The segment splitter knew `|`, `;`, `&&`, `||`, `&` —
  not `\n`. `ls\ntouch pwned` was Safe: the first line's head decided
  the whole text. A newline is now a separator.
- **Spelling.** The block patterns anchored on the bare name:
  `/bin/rm -rf`, `\rm -rf`, `RM -rf` (the default filesystem is
  case-insensitive), `/usr/bin/sudo`, `curl … | /bin/sh` all landed in
  Review, where the model tier may approve. Every segment's first word
  is canonicalised (path prefix, backslash, case) before the patterns
  run; `rm`'s recursive-and-force is read from the flags in any
  spelling (`-r -f`, `--recursive --force`, `-Rf`, behind `xargs` or
  `env`); git's global options are skipped before the subcommand, and
  `checkout … --`, `restore`, `stash drop|clear`, `clean --force` join
  the list.
- **Exec-capable read-only commands.** `xargs`, `env`, `find -delete`
  / `-exec`, `fd -x`, `rg --pre`, `sed -i` and the `w` command,
  `awk system(…)`, `sort -o`, `yq -i`, `tee` were all Safe —
  `find . -name '*.go' | xargs sed -i '' …` is the org's own
  guard-recursive-write case. `tee` and `xargs` leave the read-only
  list; the others are read-only only in their plain form
  (`mutatingUse`).
- **Process substitution.** `cat <(rm -r x)` runs the inner command
  under bash; `<(` and `>(` are dynamic construction.
- **Walk starts (ADR-0070 §3).** `find $HOME`, `find ..`, `find ~user`,
  `ls -R /` were Safe; they are Review like `find /`. Credential paths
  gain `~/.config/gh/`, `~/.docker/config.json`, `.git-credentials`,
  shell history, and match relative `.ssh/` and the like.
- **`/tmp`.** `> /tmp/x` was a false Block with an untrue reason:
  Seatbelt sees `/private/tmp`, which is a scratch root. `/tmp`, `/var`
  and `/etc` resolve to their aliases before the roots check — the
  ADR-0070 §2 drift, closed on the lexical side.

### 1.4 Writes that persist into what later sessions trust (high)

`write_file .git/hooks/pre-commit`, `.git/config`, `.mcp.json`,
`.gem-agent.toml`, `.claude/skills/*/SKILL.md`, `AGENTS.md` were all
Safe: a relative, non-credential path. A hook or a config value under
`.git/` runs *outside the sandbox* on the operator's next git command;
`.mcp.json` spawns a server unsandboxed at the next launch; a
`.gem-agent.toml` policy removes a gate; an instruction file becomes the
next session's system prompt. ADR-0020 §4 made memory writes Review
with no model tier for exactly this reason — persistence lets a
poisoned tool result reach every later session unseen — and these paths
achieve the same persistence.

Decision: writes under `.git/` are **Block** (no file tool has business
there). Writes to `AGENTS.md`, `AGENT.md`, `CLAUDE.md`, `GEMINI.md`,
`.mcp.json`, `.gem-agent.toml` and anything under `.claude/` are
**Review that only the operator answers** — `Verdict.OperatorOnly`,
which `decideAuto` honours like the memory exclusion. The rule applies
through the file tools and through shell redirects (`echo x >
AGENTS.md`) alike; other shell forms (`tee`, `cp`, `sed -i`) reach the
model tier as before, since they are no longer Safe. In auto mode this
costs one prompt per instruction-file edit — the price of the
evaluator not being the proposer.

## 2. Fixed — the seams

### 2.1 `/clear` rotated the work directory for the environment only (high)

ADR-0071 gave `/clear` a new session, transcript and work directory,
and exported the new directory to children — but every consumer of the
path was bound at startup: the sandbox profile (a shell told to write
to `$GEMAGENT_WORK_DIR` was denied), the file tools' second root, the
MCP intake's spill directory, and the system prompt, which named the
old path. `rotateWorkDir` now moves them all: `Registry.UseWorkDir`
(an empty directory removes the root), a `liveExec` the registry calls
through so the profile can be swapped, the intake reading the registry,
and `SetSystem` — a cleared conversation has no cached prefix to
protect. Two more `/clear` defects went with it: the side-call tools
(`summarize_file`, `web_search`, `web_fetch`, `agentic_file_search`)
captured the logger by value at startup and wrote their `usage`
records to the closed transcript (`liveLog` forwards to the variable);
and with an unresolved state root, `session.Open("")` resolved the
project subdirectory against the working directory and wrote a
transcript *into the operator's project* — the root is checked first,
and the new transcript is opened before anything ends, so a failure
leaves the session it found.

### 2.2 `/clear` with a session hook froze the TUI (high)

`hookNotify` called `Program.Send`; the slash handler runs inside the
TUI's `Update`, and Bubble Tea's message channel is unbuffered with
`Update` as its only consumer — a `Send` from there never returns. Any
operator with a `session_start` hook (the agent-board setup) who typed
`/clear` in the TUI had to kill the process. Notes raised inside a
slash command now ride back in its output (`uiNotes`); the `/clear`
failure paths that wrote to stderr under the TUI go the same way.

### 2.3 `Restart` kept the old session's state (medium)

`Agent.Restart` reset the history and switched the logger, and nothing
else: a dead-transcript mark from the old session silenced the new
transcript entirely; a `session_start` attachment queued before the
`/clear` rode into the new session's first turn beside the new one; an
abandoned call's late note (ADR-0065 §2) would have been announced to a
conversation that never made the call; the auto-compaction off-switch
stayed off. All belong to the session that ended. The logger is read
under the mutex beside the dead mark, since an abandoned call's
late-return goroutine may be about to record while `/clear` swaps it.

### 2.4 The second compaction saw 1,500 runes of the first summary (medium)

`renderTranscript` clips every text attachment at the tool-result
budget, and the summary a compaction leaves behind is an attachment.
Every compaction after the first summarised a summary missing its
tail — where the prompt puts "what is open" and "next step". The prior
summary is rendered whole.

### 2.5 `session_start` on `/clear` fired with source `startup` (medium)

ADR-0071 §4 wrote `startup`; the config template, the reference
document and Claude Code's own matcher vocabulary say `clear`. A hook
written to the documentation (`matcher = "clear"`) never fired on the
normal path. The code follows the documents; ADR-0071 §4 is amended.

### 2.6 Persistence and children (medium / low)

- A live **legacy flat-layout session** read as free: `session.InUse`
  looked only in the project subdirectory, and `workdirs clean` would
  have deleted its work directory. It looks where `Reopen` does.
- A **torn diagnostic write** (ENOSPC mid-record) glued the next
  message onto the fragment; one invalid line swallowed a whole message
  on resume. The logger repairs the tear before its next record, as
  `Reopen` does across processes.
- An **MCP server that ended its own stdout** stayed a zombie with both
  pipes open; the incarnation is reaped on EOF. `${VAR:-default}` in
  `.mcp.json` expands as in Claude Code.
- **Hooks**: `WaitDelay` bounds a grandchild holding stdout; the
  pre-tool payload carries the stripped arguments (ADR-0047 §2 said
  "everywhere" and the hook was the exception).
- `workdirs clean` accepted a piped `y` without `--yes` (ADR-0059 said
  it must not); `-p` printed a bare newline to stdout on a turn that
  produced no text; memory and instruction truncation cut mid-rune.

### 2.7 Dialogs and diagrams (high / medium)

- The **approval box** rendered its detail unwrapped, and `clipLines`
  cut it at the terminal edge with no marker; `edit_file`'s one-line
  detail put `path=` past column 80 — the operator approved an edit
  whose target they could not see. Detail, purpose and reason wrap to
  the box, and the extra rows come out of the detail budget. The
  **ask dialog** budgets its option rows and discloses what it cuts.
- The **settings panel** body was hardcoded English around a catalogued
  hint line; it reads from the catalog, as does `(no output)`.
- **Mermaid**: a quoted label is literal (`A["read_file(path)"]` was
  rewritten to `read_file[path]`, and the fidelity check compared the
  drawing with the rewrite); `;` separates statements (`A-->B; B-->C`
  drew a phantom node that passed both guards); a node id that starts
  with `direction` or `subgraph` is a node; `<-->` counts two heads.
  All under ADR-0042 §5's three rules — each is a syntax fact.

## 3. Intentionally not changed

- **A signal outside a turn skips the closers.** `signal.Notify` lives
  in `runTurnWith`; SIGTERM/SIGHUP at the plain REPL prompt takes the
  default action: no `session_end` hook, no telemetry flush. The
  transcript is closed by the kernel and resumes (tail repair). A
  process-level handler is a design change (the REPL blocks in a read),
  recorded for a later ADR.
- **The file-search child runs no pre-tool hook.** Its tool subset is
  read-only (ADR-0037) and `searchDenyGate` refuses mutation; an org
  guard keyed on reads would not see the child's. Left as is; noted in
  AGENTS.md as the one place hooks do not run.
- **`--o` / `--x` edges** contribute to neither the edge count nor the
  arrowhead count, so a dropped one would pass. No field report;
  measured drawing correctly.
- **Text-only turns carry no thought signatures** on gemini-3.8-flash
  (measured with and without `IncludeThoughts`;
  `textsig_live_test.go` keeps the probe). The parts path now replays
  them if a model ever sends them; the gotcha's "every Part" wording
  is narrowed to what is measured.

## 4. Post-release review of v0.68.0 (2026-09-05)

An external reviewer read the release; every claim was re-verified
against the code or a probe and all seven held. Fixed in v0.68.1:

- **`awk 'BEGIN { system ("rm -rf .") }'` was Safe** — `mutatingUse`
  matched `system(` per whitespace token; valid awk puts a space
  before the paren. The call is matched across the joined script.
- **Symlink TOCTOU in the file tools** — `resolvePath` checked the
  resolved target and returned the lexical path; a link swapped between
  the check and the open escaped the roots, from the unsandboxed main
  process. The registry now holds its roots as `os.Root` handles and
  `read_file`, `write_file`, `edit_file`, `view_image`,
  `read_document` and `file_info` open through them: the check and
  the open are one operation, and a link that leads out is refused at
  open time whatever changed in between.
- **Reads held the whole file before the cap** — `read_file` read a
  file whole and truncated to 200KB after; a huge or sparse file could
  exhaust memory, line windows included. Reads stream: the window and
  the cap apply as the file is read, no line is held past the cap, and
  images and documents are refused by size before any read.
- **`edit_file` never consulted its context** — a call the floor
  abandoned during a slow read went on to write after the operator saw
  "interrupted". It checks before the read and before the write.
- **A late return after `/clear` landed in the new session** — the
  abandoned goroutine appended its note and record at completion, to
  whatever session was current. The call captures its session epoch
  and logger; after a `/clear` the note is dropped, the record goes to
  the old transcript if still open, and the audit event alone carries
  on.
- **A failed work directory left `GEMAGENT_WORK_DIR` set** — the MCP
  servers reconnected next inherited the previous session's directory
  (and at startup a nested launch inherited its parent's). The
  variable is unset when the session has none.
- **`/clear` emitted `session.end` before the `session_end` hook** —
  the reverse of ADR-0071 §4a. The order follows the ADR and is pinned
  on the source.

### 4.1 Second pass (2026-09-05, v0.68.2)

The reviewer re-read v0.68.1 and found three remainders, all held:

- **The walks still used the lexical path** — `search_files`,
  `list_files`, `list_tree` and `file_info` listed and read with
  `os.ReadDir` / `os.ReadFile` after `resolvePath`; a directory swapped
  for an escaping link was walked from the unsandboxed process. Every
  listing, stat, readlink and read in the tools package now goes
  through the roots (`readDirIn`, `lstatIn`, `readlinkIn`,
  `readFileCapped`), and the package has no direct `os.Open` family
  call left on a resolved path.
- **Work-root rotation raced an abandoned call** — `UseWorkDir` wrote
  `workDir` / `workRoot` and closed the old root with no
  synchronisation against a goroutine still resolving through them.
  The roots are read as one snapshot under a lock, a rotated-out root
  is never closed (one descriptor per `/clear`, so the call that
  snapshotted it keeps a valid handle), and a `Subset` reads its
  parent's roots so the file-search child follows the rotation.
- **The late-return audit event still named the new session** — the
  epoch fix kept the note and the transcript record with the old
  session; the event went to the re-resourced sink. The call captures
  the sink's session id at its start and the event carries it as
  `origin_session_id`.

### 4.2 Third pass (2026-09-05, v0.68.2)

Three more on the second pass's own diff, all held:

- **`.gitignore` reads bypassed the roots** — `internal/ignore`
  `Lstat`ed and `os.ReadFile`d the lexical path; a `.gitignore` (or a
  directory above it) swapped for an escaping link between the two
  was read from the unsandboxed process, and a swap for a huge file
  was read whole before the 1 MiB cap. The rules take a `FileReader`;
  the file tools pass one that stats and reads through their roots
  with the cap applied on the stream (`gitignoreReader`).
- **`search_files` presented a capped read as a complete search** —
  `readFileCapped`'s `more` was discarded, so a file that outgrew the
  2 MiB cap between the listing and the read was searched to the cap
  and counted as searched. Such a file is skipped (`readForSearch`),
  as an oversized one always was.
- **A rotated-out work root was never closed** — a descriptor per
  `/clear` for the life of the process. Roots are `rootHandle`s with
  a holder count: acquired under the rotation lock, released after
  the open, retired on rotation and closed by whichever of retire or
  the last release comes second.

### 4.3 Fourth pass — a whole-codebase re-read (2026-09-05, v0.68.2)

A reviewer re-read the whole codebase after the third pass; nine
findings, all held except one that was a test-environment fact:

- **Shell forms naming a persistent file** (high) — §1.4 covered the
  file tools and redirects and left `cp`, `tee`, `install`, `mv`,
  `sed -i` naming `.git/hooks/pre-commit` or `AGENTS.md` at plain
  Review, where the model tier may approve. A writing command that
  names such a path in any argument now gets the file's verdict
  (`persistentTokens`); reads (`cat .git/config`) stay Safe. A path
  hidden inside an argument string (`python3 -c "open('.git/…')"`) or
  produced by an archive (`tar -xf`) is not a token and reaches the
  model tier — the limit of a lexical rule. A Seatbelt deny on
  `<project>/.git` was considered and rejected: `git commit` and
  `git init` through `shell_exec` must write there.
- **Wrappers hid the blocked command** (high) — `normalizeHeads`
  canonicalised the first word only, so `env /usr/bin/sudo id`,
  `time /usr/bin/git push`, `nohup /bin/dd …` kept their paths and
  slipped the block rules. After a wrapper (`env`, `time`, `nohup`,
  `nice`, `xargs`, `sudo`, …) every path-spelled word in the segment
  is canonicalised.
- **`/dev` was writable as a whole** (medium) — ADR-0070 §2 put `/dev`
  in the scratch roots for `2>/dev/null`; the profile's
  `(subpath "/dev")` allowed every character device, the operator's
  terminal included. The sinks are `sandbox.ScratchFiles` (literals:
  `/dev/null`, `/dev/zero`, `/dev/std{in,out,err}`, `/dev/{u,}random`)
  plus `/dev/fd` as a directory; the rule tier reads the same list, so
  `> /dev/tty` is now the Block Seatbelt will enforce.
- **Byte cuts through multibyte characters** (medium) — `truncate`,
  `read_file`'s marker, `clipText`, skill bodies and files, the edit
  diagnostics all cut at a byte offset. They cut on a rune boundary.
  (Whether a broken tail reaches the API as a 400 was not measured:
  Go's JSON encoder replaces invalid bytes; the cut is wrong either
  way.)
- **`.xlsx` sheet numbers have gaps** (medium) — a deleted sheet
  leaves `sheet1.xml`, `sheet3.xml`; the counting loop stopped at the
  gap and lost every later sheet. The members present are listed and
  taken in numeric order, like the slides.
- **`edit_file` read without a limit** (medium) — 8 MiB cap, refused
  by size before the read.
- **Hooks ran outside a process group** (low) — a child the hook
  started survived the timeout. Same `Setpgid` + group kill as
  `shell_exec`.
- **Web tools had no retry** (low) — `web_search` / `web_fetch` retry
  429 / 5xx with the main stream's backoff; a single-shot call has
  nothing consumed to duplicate.
- **Nested sandbox tests** — a `sandbox-exec` inside a sandbox cannot
  apply a profile (exit 71). `sandbox.Available` probes it and the
  tests skip, so the suite is honest under a sandboxed reviewer.

### 4.4 Fifth pass (2026-09-05, v0.68.2)

Four on the fourth pass's diff, all held:

- **`load_skill` read through the lexical path** (high) — the skill
  directory was resolved once at discovery and every later read used
  `os.Stat` / `os.ReadDir` / `os.ReadFile` on the joined path; a swap
  for an escaping link read outside the skill, and the result is the
  one tool output the agent hands the model *unwrapped* (ADR-0010 §4).
  `Body` and `File` also read whole before the cap. Each skill holds
  its directory as an `os.Root` from discovery; the description,
  the body and every supporting file are read through it, capped on
  the stream, size-gated before the read; a reload closes the
  replaced list's roots.
- **`--flag=path` and script strings escaped the persistent-file
  rule** (high) — candidates were whitespace words, and a word
  starting with `-` was skipped before its `=` was looked at.
  Candidates are now split on every delimiter a shell or a script
  puts around a path (whitespace, quotes, `=`, parens, commas,
  brackets), so `--file=.git/config` and
  `python3 -c 'open(".git/config","w")'` both yield the path. The
  accepted cost: a writing command that merely *mentions* the file
  (`git commit -m "update AGENTS.md"`) asks the operator once.
- **Sheet names by position** (medium) — a reordered workbook does not
  number its files in display order; the name now follows the
  sheet's `r:id` through `xl/_rels/workbook.xml.rels`, position being
  the fallback for a workbook without relationships.
- **The truncation note named the limit, not the bytes shown** (low)
  — after a rune cut the two differ; the note names the cut's length.
- **Found in the same sweep: instruction files** — `internal/instructions`
  read `AGENTS.md` / `CLAUDE.md` with `os.ReadFile` and no link
  check; a link planted where `AGENTS.md` should be pulled any file
  into the system prompt. Each is read through an `os.Root` at its
  own directory (a `CLAUDE.md → AGENTS.md` link beside it still
  resolves), capped on the stream; a refused link is noted. The
  `@`-reference reader (`internal/mention`) keeps its check-then-read
  shape: its input is operator-typed, in-session, and the swap would
  have to land between the operator's Enter and the read — recorded,
  not changed.

### 4.5 Sixth pass — a fixed-baseline whole-repository review (2026-09-05, v0.68.2)

Two reviewers read HEAD `e572994` across the whole repository (one
of them noting that the earlier passes had been diff-centred, which
is why the same class surfaced in a new place each time). Seventeen
findings, all held:

- **OperatorOnly could be answered by the session allowlist** (high) —
  `mustPrompt` covered Block and `always` only; an earlier `a` for
  `write_file` answered a write to `AGENTS.md`. OperatorOnly is a
  floor like Block.
- **A symlinked project skill escaped the trust prompt** (high) — the
  probe counted `DirEntry.IsDir()`, discovery followed `os.Stat`; a
  project whose skills were all links offered "nothing" and was
  trusted silently. The probe stats like discovery.
- **Shell quoting hid a persistent path** (high) — `.g''it/config`,
  `.git\/config`, `\.git/…` are `.git/config` to bash. Quotes and
  backslashes are removed before the candidate split.
- **`shell_exec` held all output before the cap** (high) —
  `CombinedOutput`; a bounded writer keeps one cap's worth and counts
  the rest. Hook output the same (`boundedBuffer`).
- **`@` attachments read whole before the gate** (medium) — images
  sized after the read, documents and media `Stat`ted then read, text
  cut by byte. Every attachment opens once (an `os.Root` at the
  project for in-project refs), sizes on the descriptor, reads
  bounded, cuts on a rune.
- **MCP results capped per block, not per response** (medium) — many
  blocks each under the cap added up without limit. One budget per
  response: a block that no longer fits is spilled like an oversized
  one.
- **MCP children outside a process group; kill called twice** (medium)
  — `Setpgid` and a group kill, once.
- **MCP tool-list pagination unbounded** (medium) — a repeated cursor,
  100 pages or 5000 tools end the listing with the server's name.
- **XLSX / PPTX in member-number order** (medium) — the workbook's
  sheets array and the presentation's `sldIdLst`, resolved through
  their relationships, give the display order; unreferenced members
  follow numerically.
- **XLSX lost empty columns and rich inline strings** (medium) — the
  `r` attribute places each stored cell; `<is><r><t>` runs are joined.
- **A long line in a windowed `read_file` was cut silently** (medium)
  — the note says how many lines were cut.
- **Directory listings unbounded** (medium) — `readDirIn` returns at
  most 10,000 entries and says when there were more; `list_files`,
  `list_tree`, `search_files`, `file_info` disclose it; skill
  directories, `@directory` and completion have their own caps.
- **Project config parsed before trust, unbounded** (medium) —
  `.gem-agent.toml` and `.mcp.json` are read through a 1 MiB cap
  before parsing.
- **Skill roots leaked on override, on the MaxSkills cut, and at
  exit** (low, second reviewer) — closed in each case.
- **Backslash before a persistent path** (low, second reviewer) —
  covered by the unquoting above.
- **The `@` text attachment's byte cut** (low, second reviewer) —
  covered by the attachment rewrite above.

Every item has a regression test.

### 4.6 Seventh pass — the fixed-baseline re-check (2026-09-05, v0.68.2)

The reviewer re-checked the fourteen items of §4.5 against `97b241e`
with an overlay of failing tests: nine confirmed fixed, five partly
remaining, two regressions from the fixes. All seven held:

- **R03** — a backslash-newline is bash's line continuation;
  `shellUnquote` removed the backslash and left the newline as a
  separator. The continuation is removed whole, first.
- **N01** (regression) — `columnIndex("ZZZZZZZ1")` asked for billions
  of empty cells, and the padding ignored the text budget. A reference
  past XFD is not a column; the budget is checked between rows.
- **R05** — `openConfined` knew the project root only; a work-directory
  text reference opened bare and a swapped link read outside. Both
  roots are passed, for files and directory listings.
- **R06** — spill previews of oversized blocks were not charged to the
  response budget. Every rendered piece is paid from it; what does not
  fit is saved together.
- **R09** — the `r:id` attribute was read without its namespace, so
  `id` matched too and attribute order decided. Read by namespace.
- **R12** — the startup scans (skills root, trust probe, work
  directories) still listed whole; the `@directory` omission claimed
  a count it did not know; `list_tree` counts dropped `more`. All
  bounded; unknown remainders say "more than", counts say "+".
- **N02** (regression) — `boundedOutput` sliced by byte before the
  rune cut. The whole kept buffer goes to the cut.

### 4.7 Eighth pass — three residuals (2026-09-05, v0.68.2)

The re-check of §4.6 confirmed four items and left three related
paths, all held:

- **R05** — the media upload took a path and reopened it; a swap after
  the check uploaded an outside file. `UploadMedia` receives the file
  mention opened through its root (`*os.File`) and the uploader hashes
  and streams that descriptor (`UploadFile`).
- **R06** — a non-text block whose note did not fit got a "not saved"
  line each — ten thousand of them — after `binary` had already saved
  it. Leftover non-text blocks are neither saved nor described one by
  one: one line names how many, and how many were saved but not
  listed.
- **R12** — `List` cut at 10,000 sessions silently, so `Sweep`,
  `workdirs list` and `workdirs clean <id>` treated the cut list as
  the whole; the per-session walk was unbounded. `List` reports the
  cut, the walk stops at 50,000 files and marks the entry partial,
  every consumer shows it (`+`, "more than"), and the named cleanup
  stats the directory itself.

## Lessons

- **Independent reviewers found what the maintainer pass did not**, for
  the fourth round running: the hook-denial attach bypass was in the
  maintainer's own reading, but the newline separator, the `/clear`
  freeze and the work-directory seam were not — and each of those was a
  shipped, operator-reachable defect. The method (ADR-0041) stands.
- **A permission keyed on one layer's flag is a hole at every other
  layer.** Round 2 fixed the attach guard for the gate; round 4 found
  the same bypass one layer up. Guards that mean "the tool ran" must
  key on that fact, not on any refuser's signature.
- **"Follow the variable" is not enough when the consumer holds a
  copy.** ADR-0039's live-variable rule was applied to `sessionLog` in
  the closures and missed in three constructors; `/clear`'s work
  directory was exported and not propagated. A rotation needs a list
  of consumers, and a test that walks it.
- **Persistence is the boundary, not the tool name.** ADR-0020 §4's
  argument was about memory; it was always about any write that
  outlives the session and is trusted by the next one.

## Consequences

- Auto mode asks once per edit to an instruction or configuration
  file, and never approves a write under `.git/`.
- The rule tier's vocabulary is larger; new dangerous forms still go in
  `internal/risk` with a corpus test (`review4_test.go` holds this
  round's).
- `/clear` is a complete session boundary: transcript, work directory,
  sandbox, file roots, intake, prompt, hooks, and the agent's own
  per-session state.

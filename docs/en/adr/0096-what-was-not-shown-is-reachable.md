# ADR-0096: What the runtime did not show must be reachable and countable — and whether it must also be said outside the data is measured first

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-10-05) — Part A implemented; **Part B not taken**: the §6 probe found no room — every arm, today's included, answered correctly (§6.1). Revised once against an independent design review (16 findings; see §8) |
| Date | 2026-10-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | An operator's analysis-quality report (2026-10-05): security log analysis over a log-search MCP server and a Markdown knowledge base, 36 sessions on v0.8x–v0.89.1, each report audited by an independent reviewer. Its runtime findings were checked against v0.90.0 (`4cf76af`) before this record was written |
| Amends | ADR-0058 §2 (the spill preview is head and tail), ADR-0052 §2 (every `search_files` skip is counted) |
| Relates to | ADR-0060 §3 and ADR-0075 §3 (trusted by provenance, never by content), ADR-0062 (the sub-agent report), ADR-0063 (no prohibitions), ADR-0071 (`/clear` rotates the work directory), ADR-0085/0086 (credential reads; the kernel cage judges paths) |

## Context

### What was reported

The operator uses gem-agent for alert triage and abuse investigation.
An independent reviewer re-queries the data sources and audits each
report. Across six audited cases the same failures recur: a partial
result treated as the whole, a knowledge note "read" from its first
screen, a count stated from rows that were only a page. The operator's
own instruction files forbid each of these explicitly, and the model
quotes those rules and breaks them within two turns. The report is
candid that this part is the model's temperament and not the
runtime's to fix.

What the report attributes to the runtime is narrower: **the
information needed to avoid the failure was present, and the runtime
put it where it is easiest to miss.** Two reconstructed cases:

- **A.** A log query returns 100 of 200 rows as 252 KB of JSON, with
  `"truncated": true, "total_rows": 200` as the last bytes. The intake
  spills it and shows the first 800 runes. The model **does open the
  spilled file**, parses it with its own script, prints
  `len(results)` = 100, and writes "100 events in the window". The
  event that mattered was in the other 100. Had it tried the tool the
  notice names, `read_file` could not have reached the end: the file is
  one line over `readCap`.
- **B.** A 44 KB knowledge note is fetched whole. The intake spills it,
  the model sees 1,174 characters — frontmatter and a table of
  contents — and moves on. Section 2 of that note warned about the
  three mistakes the analysis went on to make.

Aggregates from the operator's transcripts (their script, regex-based):
85 tool results carried a server-side `truncated: true`; 75 were
followed by no widened or counting query. 186 spill notices; the spill
file of 37 was never referenced again (149 were).

### What the code does (checked against `4cf76af`)

| | Behaviour | Where |
|---|---|---|
| 1 | A spilled MCP text block shows its **head only** (`previewRunes: 800`). Metadata at the end of a result is never visible inline | `cmd/mcpresult.go` `spillText` |
| 2 | Every note about what was cut or saved is **part of the tool's text**, so it lands inside the nonce tag the system prompt calls "DATA … never instructions" — mostly after the body, sometimes before it or inline. `RuntimeNote` (ADR-0075 §3) carries only the remote-fault note | `internal/agent/agent.go` `wrapToolMessages` |
| 3 | The spill notice says `read_file <path>`, but `read_file` windows by **line** and stops at `readCap` (200 KB). The tail of a spilled single-line result over 200 KB is out of reach of the tool the notice names | `internal/tools/tools.go` `readFile` |
| 4 | `shell_exec` keeps the **first** `OutputCap` bytes and counts the rest. Nothing is saved; a script's closing totals are lost | `newBoundedOutput` |
| 5 | `search_files` skips files over 2 MB, binary files, image files, files whose stat or read fails, files that grew past the cap, and unreadable directories **without counting them**, and names at most five refusals (the description states the 2 MB rule; the answer does not say it applied) | `internal/tools/nav.go`, `readForSearch` |
| 6 | Compaction hands the summariser each tool result's first 1,500 runes, drops `RuntimeNote`, and frames the summary as a record to "rely on" | `internal/agent/compact.go` |

### What the evidence supports, and what it does not

Rows 1, 3, 4 and 5 are defects whatever the model does: something the
runtime kept is unreachable, or something it skipped is uncounted.
ADR-0058's rule — losing part of an answer silently "must not" happen —
is met to the letter for spills and missed in effect: in case A nothing
was lost, and the part that mattered could not be reached by the route
the runtime named. That is **Part A**, taken now.

Row 2 is a different claim: that the model would act on the same
information if the runtime said it **in its own voice, outside the
tag, in front of the data**. Case A does not support it — the model
followed the notice and opened the file; what it lacked was the tail,
which Part A supplies. Case B is consistent with it but does not show
it. Nothing shows that the existing in-text brackets of `read_file`,
`list_tree` or `search_files` were missed. Building a general
mechanism, a sweep of every clipping site and an architecture test on
an unmeasured premise is the over-building the operator's own report
warns against in its §8.3. So row 2 is **Part B**: designed here in
full, so the design is not lost, and taken only if §6 shows it changes
behaviour beyond Part A.

Row 6 belongs with Part B: the declarations it would keep are Part B's.

## Decision — Part A (taken)

### 1. A spilled block is previewed by head and tail, with exact byte spans

`spillText` shows the first 600 and the last 200 runes, cut on rune
boundaries at both ends, with a one-line elision marker between them.
The tail is where formats that append metadata put it (case A), and
keeping it costs nothing format-specific: no result is parsed. Metadata
in the middle of a result is not reached; that limit is accepted.

The notice states **byte** spans, because they are what `read_file`
(§2) takes and because a rune count subtracted from a byte count gives
the wrong offset for any non-ASCII text:

> [252639 bytes — too large to hold inline, so the whole result is
> saved. Shown above: bytes 0–600 and 252439–252639. Read the rest
> with read_file offset/length, or narrow the call and ask again:
> read_file <path>]

The notice does not call the preview representative. The preview stays
small: a preview that fills the inline budget (20 KB of 252 KB) reads
as an answer. A small one can still be taken for one — case B is
exactly that — which is why the notice gives the size and the route,
and why Part B asks whether saying so outside the data does better.

`spillRest` (blocks past the budget, saved with no preview) gains the
same route wording.

### 2. `read_file` reads by bytes as well as by lines

`read_file` gains `offset` and `length` in bytes.

- A negative `offset` counts from the end. An offset past either end
  is clamped to it, and the note says so; a window that runs past the
  end stops at the end.
- `length` defaults to `OutputCap` and is capped at `readCap`. The
  default is the inline limit that caused the spill: reading a spilled
  file back 200 KB at a time would put the result inline after all.
- They are exclusive with `start_line` / `end_line`; a call carrying
  both is refused with the reason.
- The argument parser distinguishes absent, zero and negative
  (`intArg` treats ≤ 0 as absent and is not reused for these).
- The window moves to rune boundaries — a start inside a UTF-8
  sequence advances, an end inside one retreats — and the note states
  the bytes actually returned: `[bytes 251839–252639 of 252639]`.
- The sandboxed child (ADR-0086) runs the same code; nothing new
  crosses the process boundary, since the note is in the text.

This closes row 3: the notice names a tool that reaches every byte of
what it saved.

### 3. `shell_exec` keeps head and tail and saves the whole

The bounded writer keeps the first three quarters and the last quarter
of `OutputCap`, rune-safe at both cuts, and tees the full stream to a
file in the session work directory up to a spool cap of 32 MiB. The
note says how much was printed, which bytes are shown, and how much was
saved where:

> [output: 1204331 bytes; shown: bytes 0–15000 and 1199331–1204331;
> the whole output is saved: <path>]

- **gem-agent writes the spool, not the command.** gem-agent holds the
  pipe; the command's lane is unchanged. The read lane's description
  ("can write nothing but its own $TMPDIR") stays true of the command
  and gains one sentence: gem-agent saves long output to the work
  directory on its behalf.
- **The operator lane is not spooled.** It is the lane that may read
  credentials (ADR-0085), and a spooled copy would sit in an ordinary
  work-directory file that `read_file` opens without approval — the
  kernel cage judges paths, not contents (ADR-0086). Operator-lane
  output keeps head and tail and says the middle was not saved.
- **Failures are stated, not hidden.** A write that fails mid-stream
  (disk full) stops the spool; the note says how many bytes were saved
  before it failed. Without a work directory the note says the middle
  is lost.
- **The directory is fixed at the start of the call.** A `/clear` that
  rotates the work directory (ADR-0071 §2) mid-command leaves the spool
  in the old directory, which the note names — unless `/clear` removed
  that directory as empty before the first overflow, in which case the
  save fails and the note says the middle is lost (pre-release review).
- **An unsandboxed shell is not spooled either, and the file is private.**
  Without the kernel cage any lane can read credentials, so the operator
  lane's reason applies to every lane there; the note says so. The saved
  file is created `0600`, like the MCP spill (pre-release review).
- **An abandoned call** (ADR-0065) keeps writing after its tool message
  is recorded; the writer is safe for that and closes the file when the
  process ends. The message already says the call was abandoned.
- **No session-wide disk cap.** 32 MiB per call, removed by the
  operator like every work-directory file (ADR-0058 §5).

### 4. `search_files` counts every file it did not search

Every skip is counted by reason — over 2 MB, binary, image, stat or
read failure, grew past the cap during the walk, unreadable directory,
refused — and refusals past the first five are counted rather than
dropped. The note stays where the existing notes are, in the text, and
names files the way ADR-0052 §2 already does: repository names, inside
the data. With unwalked directories and ignore filtering already
reported, the answer "no match" now always comes with what was not
looked at. Binary skips matter here: UTF-16 logs and gzip archives are
binary to this check.

## Decision — Part B (decided by §6)

### 5. The runtime declares a partial view outside the data

If §6 shows it changes behaviour beyond Part A, every partial view is
also declared in gem-agent's own words, outside the nonce tag, before
and after the data. The design, revised against the review:

- **One producer, by provenance.** Tool code reports what it did to a
  coverage recorder; the executor stores the records as structured
  data (`llm.Message.Coverage`, `llm.Attachment.Coverage` for
  @-attachments; additive JSON like `denial`); the send-time wrap
  renders them. Trusted because of the field, which only the executor
  sets, never because of its text (ADR-0060 §3, ADR-0075 §3). The
  provenance architecture test is extended to the new fields.
- **One recorder per executor call.** A nested `Run` — `summarize_file`
  calling `read_file`, `credentialRetry` re-running a tool, the
  sub-agent's own executor — gets a fresh recorder, and its caller
  decides what to carry up. The executor reads the recorder on error
  paths too (an MCP `isError` result is rendered through the intake).
  `summarize_file` passes the range it read to its summariser in the
  prompt, since that range would no longer be a bracket in the content.
- **No outside string in the runtime's voice.** A record holds only
  enumerated values (kind, reason, unit), integers, and paths the
  runtime built whose parts are the registry tool name and a hash.
  File names, MIME types, server-supplied types and messages stay in
  the wrapped text. This is ADR-0075 §5's rule against putting a third
  party's string at system-prompt trust; a type rule (no free-text
  string field) and a test enforce it.
- **Rendering.** One line before the data ("gem-agent: partial view —
  the text below is bytes 0–600 and 252439–252639 of 252639"), the
  route after it. Nothing when nothing was cut. **No line does not mean
  complete** — server-side truncation, sites not moved, old brackets —
  and no prompt text may teach otherwise.
- **Compaction.** The summary message's own trusted text gains a list
  built by the runtime from the compacted messages' records (tool, kind,
  saved path) — not the summariser's paraphrase of them. The summariser
  sees head and tail of each result. The summary is framed as lossy.
  Summary messages already stored keep their old wording.
- **Enforcement.** The clipping helpers take a recorder, so a site
  cannot cut without one; a behaviour test per site asserts its record.
  Phrase-matching tests are not used: they miss new wordings and catch
  innocent ones.
- **Downgrade.** An older build resuming a newer transcript drops the
  field, and those results replay with no marker. ADR-0075 §5 accepted
  the same for `runtime_note`; the cost is higher here, so the move
  keeps a short bracket in the text for one release.

The sites it would move (corrected against the review):

| Site | Today |
|---|---|
| MCP intake: `spillText`, `spillRest`, `binaryNote`, empty blocks, non-text leftovers, failed save | bracket after preview / in place of the block |
| `read_file`: `readCap`, long-line cut, window overflow, window note | bracket after content |
| `list_files`: `listCap`, `DirEntryCap` | bracket in listing |
| `list_tree`: entry cap, per-directory cap, depth limit, unreadable, ignore note, `OutputCap` | bracket before, inline or after |
| `search_files`: match caps, line clip, per-file "+N more", interrupted, unwalked, every skip of §4 | bracket after matches |
| `file_info`: `DirEntryCap` counts, `OutputCap` | bracket |
| `read_document`: extracted-text cap | bracket |
| `shell_exec`: §3's note, timeout, held pipe | bracket |
| file child: boundary cap, interrupted | bracket (the child's reply then needs an envelope that carries records ahead of the 1 MiB cap) |
| `agentic_file_search`: report cap | bracket |
| `web_search`: sources cap | "… +N more" |
| @-attachments (`internal/mention`): size and entry caps | bracket inside the wrapped attachment |

Left as they are, with the reason: `load_skill`, the instruction and
memory loaders, piped stdin and hook context speak at operator trust
already; the librarian's description clip feeds a side model, not the
main one.

### 6. The measurement that decides Part B

Part B's premise is that **where and in whose voice** the runtime says
it changes what the model does. That is measured, not argued:

1. **A probe with crafted histories**, on the main model, no
   implementation needed: the same tool results — a 252 KB JSON whose
   last bytes are `"truncated": true, "total_rows": 200`, and a 44 KB
   Markdown note whose answer is in its second section — presented
   three ways: (a) as today; (b) Part A — head and tail, byte spans,
   note inside the tag; (c) Part A plus the note outside the tag, before
   and after. The task asks for the count and the answer. Twenty runs
   per arm; recorded: whether the reply states the result is partial,
   whether it reads further (and with what), and the answer given.
   Part B is taken if (c) beats (b) by a margin fixed before the runs.

   *As run (fixed and committed before the runs, `tools/coverageprobe`):*
   not crafted histories but the real binary in `-p` against a stub MCP
   server, which measures the whole loop and the answer given — (a)
   v0.90.0, (b) Part A, (c) Part A plus `armc.patch` (never merged).
   Each run is isolated (its own `HOME` and state root: no operator
   instructions, memory or MCP fleet). Scenario S1 asks how many events
   (success: not presenting 100 as the whole); S2 asks to read the note
   first, then count processes (success: 37, reachable only through the
   note's §2). Margin: (c) − (b) ≥ 30 points on either scenario and
   neither worse by more than 10; if (b) is at 90% or more on both, there
   is no room. A smoke run read the probe's own source through the read
   lane, so a run whose tool calls leave its own directory is
   contaminated and replaced. The first launch was discarded as a pilot
   before any scoring: its stub was one server named `probe`, and runs
   investigated the harness instead of the task (one disassembled the
   stub with `otool`). The stub now presents as the operator's setup
   did — `splunk` and `obsidian` behind a binary named `mcp-bridge`;
   scenarios, prompts and rules are unchanged.
2. **The operator's split.** Their script does not separate a
   `truncated: true` the model saw inline from one only inside a spill
   file. They are asked to re-run it three ways — visible inline without
   a spill, in a spill preview, only in the spill file — over the same
   36 sessions. If most of the 75 were visible inline, neither part will
   move that number much, and the report should be read that way.
3. **After release**, the operator's two ratios on comparable sessions.
   Their script finds spills by the notice text; Part A keeps that text,
   so the instrument is unchanged. If Part B lands, the notice leaves the
   content, and a reader of the `coverage` field is supplied and run
   alongside the old one.

### 6.1 Result of the probe (2026-10-05)

Main model `gemini-3.8-flash`, `thinking = "high"`. Every run that
answered, counted by arm (the decision table counts only uncontaminated
runs; this one adds the contaminated runs, scored by their answers):

| Arm | S1 success | S2 success | read the saved file | narrowed with a count query | `read_file` offset |
|---|---|---|---|---|---|
| (a) v0.90.0 | 26/26 | 21/21 | every run | every run | — |
| (b) Part A | 33/33 | 22/22 | every run | every run | 15 runs |
| (c) Part A + note outside | 28/28 | 26/26 | every run | every run | none |

Uncontaminated runs: S2 20/20 in every arm (a→b and b→c both +0 points,
Fisher p = 1.000). S1 could not reach its 20: 30, 37 and 32 runs were
contaminated — after answering, the model listed the parent directory
(`ls -la ..`) in a project that held nothing else — leaving 1, 0 and 1
valid runs. Every S1 answer, contaminated or not, said 200.

**Decision, by the rule fixed before the runs: Part B is not taken.**
(c) − (b) is +0 on S2, and (b) is at 100% there and, counting every
answer, on S1 — there is no room for a note's position or voice to
change the answer. The rule's "both scenarios" clause rests on S1's
answers rather than its 20 valid runs; that is the residual, recorded
here rather than re-run, because no run of any arm failed the task.

What the probe also shows, and what it does not:

- **The reported failure did not reproduce.** In a short, clean
  session the model read the saved file and narrowed with a count query
  every time, under today's head-only preview as well. The operator's
  failures are therefore not explained by the presentation alone;
  their sessions differ in what this probe did not have — 28 servers,
  first prompts of ~120k tokens, a hundred-odd calls, a task that is one
  step of a longer analysis, rules in the instructions. That makes
  §6's step 2 (the operator's own split) the evidence that matters.
- **Part A stands on its own grounds** (§Context): a route that could
  not reach the end of what was saved, and skips that were not counted.
  The probe neither supports nor undercuts it — nothing failed in (a).
- **The note outside the tag changed one thing the probe did not
  score**: in S2, (c) wandered out of its directory in 6 of 26 runs
  against 1 of 21 in (a) and 2 of 22 in (b), and never used the byte
  window its note named. Small numbers, recorded as an observation.
- **What it cost, and why it went unseen.** About 240 sessions and
  5,862 model calls: 160.3M prompt tokens (130.3M cached) and 1.76M
  output and thought tokens — $38.94 at the introductory list rate, which
  matched the cloud bill for the day. The isolation that kept runs apart
  kept their usage records out of the state root gem-usage-lens reads, so
  the spend surfaced first on the bill; the run roots were ingested
  afterwards. The probe now records each run's tokens, prints the spend,
  and takes a prompt-token cap.
- **Wandering is itself a property of the setup**: a question answerable
  in three calls took a median of 33–38 calls in S1, most of them
  examining the environment. The first launch, with a server named
  `probe`, was worse (one run disassembled the stub). Probes of this
  model's behaviour in an empty project measure that as well.

## 7. Not decided here

These came with the same report and need decisions of their own:

- **MCP `initialize.instructions`** are discarded. Delivering them is a
  trust decision that meets ADR-0083's direction (server prose kept
  away from the main model) and is decided separately.
- **Operator procedures read through MCP arrive as data.** Making an
  operator-chosen server or path instruction-grade is **not** taken up:
  the same report shows the model writing a claim into the knowledge
  base and citing it three days later to justify an error — an
  instruction channel the model can write to keeps its own mistakes as
  rules. Documenting skills as the route for procedures is a
  documentation change.
- **Summaries standing in for content** (`summarize_file`, `web_fetch`,
  the sub-agent's report) already label themselves in their text
  ("by <model> — lossy"). Whether to say more, and the prompt wording
  around them ("Trust the report", the `summarize_file`
  recommendation, "coding agent"), are the report's inference without a
  measurement; ADR-0062 measured that an in-band invitation to verify
  the sub-agent's report caused re-exploration. Decided together, with
  evidence.
- **`post_tool_use` / stop hooks** wait for the operator to name what
  they would run there (ADR-0044's demand rule).
- **Declaration size**: `advertise = "on-request"` exists (ADR-0083);
  the operator runs `"all"` with 28 servers. Trying it comes before any
  change of default.

## 8. Independent review (2026-10-05, before acceptance)

A reviewer with no stake in the draft re-read the code behind every
claim and returned 16 findings. All were checked against the source;
all were adopted. The ones that changed the record:

- **The first draft rendered skipped file names, MIME types and server
  types outside the tag** — a third party's string at system-prompt
  trust, which ADR-0075 §5 refused. Records now hold enumerations,
  integers and runtime-built paths only (§5).
- **A nested call would have filed its record on the outer message**:
  `summarize_file` runs `read_file` with the same context, so a
  `read_file` range would have been rendered above a 25-line summary,
  and the summariser would have lost the bracket its prompt relies on.
  One recorder per executor call (§5).
- **The draft mixed characters and bytes** in its example arithmetic;
  every span is now bytes (§1, §2).
- **The evidence supports less than the draft built.** In case A the
  model opened the spill file; neither a recorder nor a trusted note
  would have changed that, only the tail. The general mechanism was
  split off as Part B, conditional on §6.
- **The draft claimed `summarize_file` and `web_fetch` were unmarked**;
  both label themselves. The `transformed` record was removed and the
  question moved to §7 with the prompt wording.
- **The spool was under-specified**: operator-lane credentials landing
  in a readable file, the read lane's promise, `/clear` rotation,
  abandoned calls, mid-stream write failure (§3).
- The site table missed eleven sites and had two wrong rows; the
  phrase-matching test was brittle; compaction would have kept the
  declarations only as the summariser's paraphrase; `read_file`'s
  defaults and edge cases were unspecified; two citations were wrong
  (ADR-0052 §2, not §3; ADR-0058 says "must not", not "must never").

## Consequences

- The tail of a spilled result is in front of the model, and every byte
  of it is reachable by the tool the notice names.
- `shell_exec` output stops being lost past 20 KB, except in the
  operator lane, which says so; long outputs leave a file in the work
  directory.
- "No match" from `search_files` says what was not searched.
- Notes stay in the tool text and in today's position until §6 says
  otherwise. The operator's measurement script keeps working unchanged.
- lagent shares the spill intake and `read_file`; it ports Part A as its
  own record.

## Alternatives considered

- **Parse JSON results and summarise their shape** (row count, presence
  of `truncated`). Rejected, as the report itself rejected it: it works
  for one format, and each format grows its own special case. Head and
  tail reach the same metadata without reading it.
- **Take Part B now, on the strength of the class argument.** Rejected
  for now: the class is real, but the only case that bears on it shows
  the model following the notice it had. A sweep of twenty sites and a
  new trust surface is not built on an argument that one probe can
  settle.
- **Keep the notes in the text and only move them to the front.**
  Measured inside §6 rather than decided: it is the half of Part B that
  costs nothing in trust, and arm (b) can be split if (c) wins.
- **State `coverage: complete` on every result.** Rejected: a constant
  line is noise the model learns to skip.
- **Preview up to the inline budget.** Rejected: a large preview reads
  as the answer.
- **Raise `readCap` or lower the spill threshold to match it.** Moving
  either number leaves a file one byte larger in the same place. A byte
  window reaches every size.
- **Widen `Tool.Run` to return a result struct** (for Part B).
  Rejected for ADR-0058's reason: twenty-odd tool definitions and every
  return inside them.

## References

- `cmd/mcpresult.go` — the intake; `spillText`, `spillRest`
- `internal/tools/tools.go` — `readFile`, `readWindow`, `boundedOutput`, `shell_exec`
- `internal/tools/nav.go` — `search_files`; `readForSearch` in `tools.go`
- `internal/agent/agent.go` — `wrapToolMessages`; `internal/agent/compact.go`
- ADR-0058 §2, ADR-0052 §2, ADR-0075 §3 and §5, ADR-0060 §3, ADR-0086, ADR-0062

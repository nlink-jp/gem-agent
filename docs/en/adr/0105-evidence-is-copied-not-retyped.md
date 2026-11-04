# ADR-0105: every MCP result is saved, and evidence is copied from the saved file — never re-typed

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-11-04) — implemented 2026-11-04; acceptance waits on the §5 measurement. Revised once against an independent review (9 findings; see §7) and once in implementation (§8) |
| Date | 2026-11-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator's report (2026-11-04): DWH query results kept as evidence lost rows, and evidence appeared that the database never returned |
| Amends | ADR-0058 §2 (only a block above the inline cap was saved) |
| Relates to | ADR-0096 §1, §3 and §7 (spill preview; the shell spool; verification lives outside the runtime, RT7 not taken), ADR-0072 §1.4 (writes that persist into what later sessions trust) and §4.5 (one budget per response), ADR-0073 (shell lanes; the tier reads no command text), ADR-0081 (the model tier's two rounds), ADR-0085/0086 (credential reads; the kernel reads the file), ADR-0059 and ADR-0071 (work directory cleanup and rotation), ADR-0061 (charter: a minimal, auditable agent loop) |

## Context

### What was reported

The operator collects data from databases and the DWH through MCP servers and keeps what was returned as evidence. When a result is small enough to be handed to the model inline, no file of it exists. The agent then writes the evidence file by re-typing the values from its context, and on the way rows go missing and values appear that the database never returned. The operator proposed that every MCP result go to a file as well as to the context, whatever its size.

### What the code does (checked against `4183260`, v0.97.1)

`cmd/mcpresult.go` renders an MCP result for the model. A text block up to `tools.OutputCap` (20,000 bytes) is handed over inline and is not written anywhere. A larger one is written to the session work directory — content-addressed, temp file plus rename, mode 0600 — and the model gets its head and tail with the byte spans and the path (ADR-0096 §1). Text blocks past the response budget are saved joined, as one file, with no preview.

So for an inline result, the only way for the result to reach the project as a file is for the model to generate it again with `write_file`. For a spilled result, a file exists, but the built-in tools have no copy: `write_file` takes content, so copying means `shell_exec` (`cp`) in the write lane. The two routes are approved differently (`internal/risk/risk.go`):

- `write_file` inside the project is **Safe**, approved at once.
- A write-lane `shell_exec` is **Review**. It is approved only after two model-tier rounds (ADR-0081) say yes; otherwise it goes to the operator, and in `-p` with nobody to ask it is refused.

The route that preserves the bytes costs a judgement; the route that regenerates them costs nothing.

### What the operator decided

Re-typing is the problem itself, not how often it errs. A file the model wrote out from its context has no provenance, even when every value in it is right: nothing distinguishes it from a file the model made up. How often re-typing goes wrong is therefore not a number evidence can rest on (ten clean runs bound the error rate only to about 26% at 95%), and the measurement below was turned from accuracy to **how the evidence file was written**: copied, derived by a recorded transformation, or regenerated.

That every MCP result is saved is taken as a requirement.

### What was measured (2026-11-04)

`tools/evidenceprobe` (commits `1574ba0`, `2b072e5`) runs the real runtime binary one-shot (`-p --auto`) against one MCP server: a stub that presents as the fleet's bigquery-mcp under a realistic server name, with the same tool names, descriptions and result shapes, and really executes the queries (GoogleSQL translated for the system sqlite3) against a fixed, seeded table of 300 EDR process events, 60 of which match the task. Every returned result is logged as ground truth. Runs are isolated (HOME and the state directory point into the work area), and contamination rules were fixed before the counted runs.

- **Small result, inline (s1).** All four runs so far (one gem-agent pilot, one lagent pilot, two counted lagent runs) wrote the evidence by re-typing it with `write_file`. The values happened to be right.
- **Large result, saved by the runtime (s3).** lagent with Qwen3.6-35B-A3B, 10 valid runs, the result spilled in every one:

  | How the evidence was written | Runs | Content |
  |---|---|---|
  | Copied or derived from the saved file | **0** | — |
  | Re-typed with `write_file` | 8 | 2 correct; 6 wrong |
  | Nothing saved | 2 | the write-lane shell call was refused in `-p` |

  The six wrong runs: a value taken from another row (452,631,160 bytes written as 338,708; four runs of this kind), one row dropped, and one run that lost a SHA-256 and two byte counts. The errors fall both near the `read_file` window boundary and elsewhere. One of the two "nothing saved" runs had tried `mkdir -p … && cp <saved file> evidence/` and was refused. The scorer cannot tell an altered plain integer from a missing one, so it labelled the substitutions "lossy"; reading the files showed them to be substitutions.

What this shows: **a saved file alone produced no copy.** The agent reached for `write_file` every time, and the one attempt to copy took the route that needs approval.

What it does not show: these runs are lagent's. lagent sends a write-lane shell call straight to the operator; gem-agent sends it to the model tier first. How gem-agent with its own model behaves has not been measured.

## Decision

### 1. Every text block of an MCP result is saved, whatever its size

The intake writes every text block to the work directory, including a result the server marked `isError`.

- **What the model reads.** A block that fits is still handed over inline, exactly as the server sent it. After the wrapped result comes one line per saved file; the blocks a response hands over inline are saved together as one file (§8). The line is gem-agent's own and sits outside the nonce tag, as ADR-0075 §3's runtime note does (§8):

  ```
  [gem-agent saved this result whole: <path> (<n> bytes, sha256 <64 hex>) — to keep it as evidence, write_file with from set to that path copies it byte for byte]
  ```

  With the state-root path the line is about 300–400 bytes. It is not part of the server's text, so it is not paid from the response budget, and the inline size is unchanged.
- **What does not fit takes today's routes.** The first block that does not fit is previewed by head and tail (`spillText`); later blocks past the budget are saved joined as one file (`spillRest`), as today. Both notes carry the full sha256. A result of several text blocks is therefore several saved files; keeping all of it as evidence is several copies.
- **Names carry the full hash.** The content-addressed name uses the whole sha256, not its first 4 bytes, so the path of a block is known exactly before the write (the binary pre-sizing in `render` relies on that) and two results never share a name. An existing file at that name is reused only when its own sha256 matches; otherwise it is replaced by temp plus rename. MCP servers and write-lane commands can write the work directory (ADR-0058 §4), so a file found there is not trusted by its name.
- **A record the server cannot write.** A server can print a line shaped like the note, so neither the note nor anything in the result text can be the record. The runtime records each saved block in a structured field of the tool-result entry in the transcript (`saved`: path, byte count, sha256), set by the executor from what the intake reported and never parsed from the text. The note is rendered from that field when the history is sent. Verification (§4) reads the field.
- **No work directory.** There is none exactly when the session's transcript is disabled (ADR-0058). The text is inline only, as today, and the note says it was not saved, so the model knows there is nothing to copy. Such a session has no record to verify against either.

### 2. `write_file` copies: `from` instead of `content`

`write_file` gains one argument, `from`: the path of an existing regular file whose bytes become the new file's content. Exactly one of `content` and `from` is given.

- **The source is read raw, whole and bounded.** The read goes through the sandboxed child (ADR-0086), by a dedicated raw-bytes operation, not through `read_file`. `read_file` stops at 200 KiB and appends its own notes, and the child's stdout is capped at 1 MiB with a truncation note; reusing either would silently cut a large result. The operation has a stated ceiling (32 MiB, the shell spool's cap, ADR-0096 §3), refuses above it instead of truncating, and the written file's sha256 is checked against the bytes read. A directory, device or link that does not resolve to a regular file is refused.
- **A credential source is Block, not a read to approve.** `from` naming credential material (ADR-0085) is Block, and it never carries `CredentialRead`. A `write_file` refused by the kernel is not retried in process (`credentialRetry` excludes it): an approval that looks like a read must never become a credential written under an ordinary project name. ADR-0086 §4 names this copy as a limit of path-based caging, and ADR-0096 §3 keeps operator-lane output out of the spool for the same reason.
- **Both paths are judged by their real names.** `from` joins `path` in `JudgesPath`, `withRealPaths` and `Classify`. A source may lie in the project, the work directory or an added directory (read or write). Another session's work directory is outside every root. After `/clear` the previous work directory is outside too, and a copy from it is refused as a path outside the roots.
- **The destination is judged exactly as today's.** The same path rules, the same persistent-file rules (ADR-0072 §1.4). In the project it is therefore Safe, the same tier as re-typing: **copying is never costlier than re-typing.** The shrink guard compares the source's size, not `content`'s.
- **The operator sees what is copied.** The call's detail (`Annotate`) names the source and its size, so a copy that needs approval (into an instruction file, say) is not approved blind. It shows no bytes of the source (§8).
- **The result line reports the copy:** `copied <n> bytes from <from> to <path> (sha256 <hex>)`.
- **Whole file only.** A subset of rows or a format conversion is a derivation. Derivations stay where they are today: a shell command in the write lane whose command line the transcript records. This ADR does not make derivation cheaper.

Why `write_file` and not a new tool: the measured agent reached for `write_file` in every run, and a route placed on the tool it already uses needs nothing discovered. The charter (ADR-0061, a minimal, auditable agent loop) keeps the tool set small.

### 3. The route is offered at the moment it is needed

Two places carry it, both phrased as a route and not as a prohibition. One is the per-result note of §1, read at the moment a result arrives. It is gem-agent's own line outside the nonce tag, so the model can tell it from anything a server prints. The other is the `write_file` description, read when the model reaches for the tool. That description names `from` as the way to keep a saved tool result. The system prompt is not changed unless §5 shows that the route does not fire.

### 4. Verifying evidence stays outside the runtime

The runtime supplies bounded facts: the saved file, the structured record of §1 in the transcript, and the copy's sha256 in the `write_file` result. Whether an evidence file is a copy is a hash comparison against the record. That check belongs to the operator or a tool reading the transcript (ADR-0096 §7). The runtime does not judge whether a `write_file` content was re-typed, and does not refuse it. Recognising re-typing is a judgement over unbounded text. An exact re-typing is still unprovable, and the hash settles provenance from the other side. A copy of a file a server planted in the work directory matches no record, and the check shows it.

### 5. Measured after implementation, before acceptance

The probe is re-run against the implementation. Before any run, a dated amendment in `tools/evidenceprobe/main.go` pre-registers the following. That file currently withdraws s1 and makes R3 reported-only.

- **Cells.** gem-agent, s1 (inline) and s3 (spilled), prompts unchanged, n = 10 valid runs each. An s3 run counts only when its result was spilled; the spill marker the probe keys on stays in the wording, or the probe follows the new one.
- **What counts.** *Copied* is an evidence file byte-identical to a returned result, written by `write_file` with `from` or by a shell copy. The scorer's method axis is extended so that `write_file` with `from` is classified by the written bytes, not by the presence of the argument. *Derived* (a shell write) and *regenerated* are reported beside it. A valid run with no evidence file counts in the denominator as not copied.
- **Reading.** The route fires when at least 8 of 10 valid runs in each cell are *copied*. *Derived* does not count toward it: this ADR leaves derivation unchanged, so counting it could pass without the new route firing.
- **Below the threshold.** The wording of §3 is revised once and the cells are measured once more. If it is still below, the operator decides what comes next. §1 and §2 stand either way: they make a copy possible and checkable, whatever the model's habit.
- **Cost.** gem-agent runs call the paid model, and the probe's isolation keeps them out of gem-usage-lens. The s1 pilot spent $0.218. An s3 run is estimated at about $0.35, scaled by the prompt tokens the lagent s3 runs used against the s1 pilot (about 1.6×). Ten valid runs per cell come to about $6. Failed or contaminated runs are replaced up to 2n, so the worst case is about $12. The cells are launched only after the operator approves the figure.

Whether to measure gem-agent's s3 before implementation (about $3.5) as a baseline is the operator's choice. The decision does not depend on it.

## 6. Not decided here

- **Built-in tool output** (`shell_exec`, `web_fetch`). The report concerns MCP results. `shell_exec` already spools read- and write-lane output in full up to 32 MiB when it is cut (ADR-0096 §3).
- **The write-lane asymmetry itself.** A write-lane shell call stays Review (ADR-0073). Copying no longer needs it.
- **The work directory's lifetime.** Saved results go when the work directory is cleaned (ADR-0059) or rotated by `/clear` (ADR-0071). Evidence has to be copied into the project, as today.
- **lagent** shares the intake code and decides for itself.

## 7. Independent review (2026-11-04, before the operator's review)

A sub-agent read the first draft against the code and the cited ADRs. All nine findings were confirmed in the code and taken:

1. A credential `from` could be approved as a read and copied in process. Now Block, never `CredentialRead`, and `write_file` is excluded from `credentialRetry` (§2).
2. Reading the source the way `read_file` does would truncate above 200 KiB, or above 1 MiB through the child. Now a raw-bytes child operation with a 32 MiB ceiling that refuses instead of truncating, with a hash check (§2).
3. The note could be forged by a server's own text. Now a structured transcript record the server cannot write, which verification reads (§1, §4).
4. The budget routing was described for the first block only, multi-block results were not addressed, and the note's size was understated (the path appeared twice). Corrected (§1).
5. A full-hash fallback name broke the binary pre-sizing and could be pre-planted. Now the name is always the full hash, and an existing file is reused only when its hash matches (§1).
6. The approval detail for a copy would be empty. `Annotate` now shows the source (§2).
7. The shrink guard compared `content`, and the source could be a directory. Corrected (§2).
8. The measurement was under-specified: the scorer read `from` as regenerated, *derived* could pass the threshold without the route, the denominator and cost were unclear, and the failure branch had no end. Corrected (§5).
9. Citations: ADR-0072 §1.4, not §4; the charter's wording; ADR-0096 §3's scope; no work directory means no transcript. Corrected.

One suggestion was not taken: an optional `sha256` argument on `from` would bind the copy within the runtime, but it asks the model to re-type 64 hex characters, and the structured record already serves verification. The remark that the note would sit inside untrusted data was resolved in implementation by moving the note outside the tag (§8).

## 8. Implementation (2026-11-04)

Implemented as decided, with these differences and additions:

- **The note is rendered outside the nonce tag** from the record, not appended to the server's text. Once the record existed as a field the executor sets, putting the runtime's line next to `RuntimeNote` (ADR-0075 §3) cost nothing. It answers review finding 3 for the model as well as for verification, and the response budget no longer pays for the note.
- **The record travels through the context.** The intake returns what it saved beside the text. The MCP adapter hands it to a recorder the executor puts in the call's context (`tools.RecordSaved`), and the executor copies it into `Message.Saved`. A tool that saves nothing is untouched.
- **`credentialRetry` is gated by tool:** only the covered reads (`tools.ChildTool`) are retried in process. The copy source also maps the kernel's refusal to its own error, so the gate holds twice.
- **The copy source is a child operation, not a tool** (`copy_source`): the child runs it by name beside the covered reads, and nothing declares it to the model. In process — without a child — the credential matcher checks both the spelling and the real path.
- **A file at the content-addressed name is hashed as a stream** before reuse. Reading it whole would be an unbounded read of a file anything may have written (the bounded-reads architecture test caught it).
- **The probe's pre-registration** for §5 is in `tools/evidenceprobe/main.go`, dated before any run. The scorer classifies a `write_file` `from` by the bytes it wrote. *Copied* now requires byte identity for a shell copy too: a superset is *derived*.
- Moved line citations in ADR-0089, ADR-0090 and ADR-0091 were updated (the citation test caught them).

A second independent review read the implementation before it was committed. Its confirmed findings were taken:

- **Inline blocks are saved together** as one file with one record and one note: what the model received inline, the blocks joined by a newline, a single block as its own exact bytes. A note per block was paid from no budget, so twenty thousand empty blocks would have been twenty thousand notes.
- **A spill preview is sized before anything is written**, from the content-addressed path, as binary notes already were. A preview that does not fit leaves no unrecorded file behind.
- **An interrupted copy is an error.** The child's partial-output label for reads was written into the copy and reported as success.
- **The retry gate is the credential-read list** (`risk.CredentialReadTool`), not `tools.ChildTool`: `summarize_file` reads through `read_file` and asks the operator too, and the first gate had taken that away.
- **`Annotate` stats; it does not read.** Describe calls it for every call, and its text reaches the activity trace and the audit telemetry. A first line of DWH data must not travel there, nor a 32 MiB read run outside the floor. The detail is the source's name and size.
- **A file at the content-addressed name is reused only when it is a regular file of the result's size** and hash. A planted FIFO would otherwise block the open.
- Empty `content` beside `from` is treated as absent. R4 decides nothing on fewer than 10 valid runs.

Accepted residuals, by risk:

- **A copy has the operator's default mode**, not the saved file's 0600, the same as a file written with `content`.
- **Degraded mode has a check-then-open gap.** Without the child, the credential check and the open are two steps, so a concurrent writer could swap a link between them. It needs a writer racing the call inside the roots, and the kernel closes it whenever the child is installed.
- **After a resume, a note may name a work directory that is gone or outside the roots.** The copy then fails with that reason.

## Consequences

- Every MCP result is on disk, not only the large ones. DWH rows, which may hold personal data, now lie where only spilled results lay before: files created 0600 in the session work directory under the state root, removed by the cleanup command. Accepted: the operator already keeps evidence of the same data in the project.
- About 300–400 bytes of note per saved file enter the context, outside the response budget. The inline size is unchanged. Saved file names grow by 56 hex characters.
- `write_file`'s schema grows one property. The child gains a raw-bytes read, and the transcript's tool-result entry gains a structured field. The probe gains the copy method.
- The copied bytes are the server's raw text, never the guard-wrapped form the model sees. They go to a file, not into the prompt.

## Alternatives considered

- **Save only (the original proposal).** In s3 the file existed and no run copied it. Saving is necessary and, measured, not enough.
- **The runtime writes results into the project** (say `evidence/`). That would mean the runtime writing the operator's tree unasked, under a layout that is the operator's to choose. The work directory is the runtime's place (ADR-0058).
- **Make a write-lane `cp` from the work directory Safe.** A rule over shell text, the class ADR-0073 retired. Its domain is unbounded (`cp`, `cat >`, `tee`, `install`, quoting).
- **A separate `copy_file` tool.** A second tool the model has to discover is a capability without a trigger, and the measured agent reached for `write_file` every time.
- **Detect re-typing in `write_file` content** and warn or refuse. A judgement over unbounded text, and an exact re-typing would pass it while still having no provenance.
- **The runtime verifies evidence at the end of a turn.** Rejected in ADR-0096 §7 (RT7). The bounded facts this ADR adds are what such an outside check needs.
- **A separate manifest file of saved results.** The transcript already records each call's arguments with its result. The structured field of §1 puts the path and hash in that same entry, and a third record would be one more to keep consistent.

## References

- `tools/evidenceprobe/` — the probe: pre-registration and amendments in `main.go`, the scorer in `score.go`; commits `1574ba0`, `2b072e5`
- `cmd/mcpresult.go` — the intake; `cmd/filechild.go` — the child; `internal/risk/risk.go` — the rule tier; `internal/agent/autoapprove.go`, `decision.go`, `agent.go` (`credentialRetry`) — the ladder
- nlink-jp/knowledge `llm-integration.md` — probes are built on the premise that the agent investigates the test itself

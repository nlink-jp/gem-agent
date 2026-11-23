# ADR-0125: the search opens each directory once — `search_files` reads its files side by side, and its output does not change

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-23) — implemented (see the addendum) |
| Date | 2026-11-23 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator: can `search_files` be made faster? Measured first; the time goes into opening files, not into searching them |
| Amends | ADR-0052 §5 ("parallel walk: measured unnecessary … revisit with measurements") |
| Relates to | ADR-0013 (no index, no ripgrep), ADR-0065 (cancellation in process), ADR-0072 §4 (`os.Root` confinement, opens that do not wait), ADR-0086 (the file-read child), ADR-0096 §4 (every file not searched is counted), ADR-0114 §4 (the child opens only the roots a call names), ADR-0123 §1 and §4 (the four answers; opens are the standard library's), CONVENTIONS ADR-029 (design before implementation) |

## Context

### What was measured (v0.108.2, 2026-11-23, Apple M2 Max, 12 cores)

Each table says where it was measured. "Child" is the shipped binary run as the parent runs it: `__file-read` under the real file-read profile (`sandbox.FileReadProfile`), one request on stdin. "Cage" is a small program run under the same profile. "No sandbox" is in process, outside any profile.

**The whole call** (child; median of five warm runs):

| Tree | Files scanned | Literal pattern | `func \w+Registry` | `(?i)thought signature` |
|---|---|---|---|---|
| gem-agent (59 directories) | 757 | 145 ms | 146 ms | 285 ms |
| cli-series (all submodules) | 1,555 | 299 ms | 300 ms | 568 ms |

A `file_info` call through the same child takes 40 ms. That is the fixed cost of one call: starting the child, `sandbox-exec` compiling the profile, the Go runtime starting.

**Where the rest goes** (no sandbox; CPU profile of the in-process search over the gem-agent tree):

| Where | Share |
|---|---|
| `openRead` → `root.OpenFile` (one open per file) | 70% |
| `.gitignore` reads, through `lstatIn` and `readFileCapped` | 7% |
| The regular expression | 9% (literal) — most of the time under `(?i)` |
| Reading the bytes, listing directories | under 5% |

The open is slow because `os.Root.OpenFile` resolves the path one component at a time from the root, so each file costs one open per directory above it.

**One open, per file** (765 files of the gem-agent tree, two runs each):

| Open | Cage | No sandbox |
|---|---|---|
| `os.Open` of the absolute path (no confinement) | 45 µs | 38 µs |
| `root.OpenFile(rel)` — what ships | 135 µs | 115 µs |
| `sub.OpenFile(name)`, `sub` the file's directory opened once with `root.OpenRoot` (the 57 directory opens included) | 54 µs | 44 µs |

In the child, 757 opens at 135 µs come to about 100 ms — nearly all of the 105 ms the call spends beyond its fixed cost.

**Opening, `fstat` and reading every file through per-directory handles** (765 files):

| Workers | Cage | No sandbox |
|---|---|---|
| 1 | 42 ms | 45–53 ms |
| 4 | 16 ms | 15 ms |
| 8 | 10 ms | 9–10 ms |

**The regular expression** (no sandbox; matching only, over the 7.8 MB those files hold):

| Pattern | Literal prefix | Line by line (ships) | Each 1,024 lines checked first, then line by line |
|---|---|---|---|
| literal, not found | the whole pattern | 10 ms | 3 ms |
| literal `func New` | the whole pattern | 10 ms | 5 ms |
| `func \w+Registry` | `func ` | 11 ms | 6 ms |
| `func (\w+\s+){10}zzzz` | `func ` | 10 ms | 7 ms |
| `(?i)thought signature` | none | 101 ms | 116 ms |
| `(\w+\s+){10}zzzz` | none | 343 ms | 449 ms |
| `(?i)(\w{1,30}\W{1,5}){1,30}QQQ` | none | 1,456 ms | 3,094 ms |

A pattern with a literal prefix is found by searching for the prefix, so checking a block first is cheap. A pattern without one runs Go's general matcher over the whole block, which costs more than the same work line by line. Go's matcher also has no fast path for a case-folded pattern. For `(?i)`, only running files side by side helps.

### Why ADR-0052 §5 no longer holds

ADR-0052 measured 10 ms warm on a 19,000-file project once ignored content was skipped, and declined a parallel walk until a measurement said otherwise. Two later decisions added cost per open: ADR-0072 §4 routed every open through `os.Root` (three times the cost of a plain open here), and ADR-0086 moved the reads into a child per call (40 ms). Both stay. Their cost is what this ADR takes back.

The model's own round costs seconds, so one call saved 100 ms is not felt. It is felt where it adds up: a model that searches several times in one turn, the delegated `agentic_file_search` child, which searches repeatedly, a deeper tree (the cost per file grows with depth), and `(?i)`, which doubles every figure above.

## Decision

`search_files` gets three changes. Each is defined by "same output, less time": for any tree that does not change during the call, the text it returns is byte-for-byte what v0.108.2 returns.

### 1. Each directory is opened once, and its files are opened by one name

The walk holds an `os.Root` for the directory it is in, opened from the directory above it with `OpenRoot(name)`. Within a directory:

- the listing reads the directory through that handle (`DirEntryCap + 1` entries, as now);
- its `.gitignore` is read through that handle;
- each file is opened through that handle by its one name, with the open of today (`O_NONBLOCK`, regular file or directory only, ADR-0072 §4.8).

A directory's handle is closed when the walk has left it and the last of its files has been read.

**A directory is opened through `name/.`.** `OpenRoot` opens its last component with neither `O_DIRECTORY` nor `O_NONBLOCK`, so a FIFO at the name would make the walk wait. That covers a FIFO named as `path`, and a FIFO put where the listing had returned a directory. Opened as `OpenRoot(name + "/.")`, the name is resolved as a directory on the way: anything else is refused at once (`ENOTDIR`), and a link is followed only where the root allows, as before. The start of the walk is opened the same way, through the call's root (`rootFor`). A FIFO or a regular file named as `path` gets the answer it gets today: one directory that could not be listed. *(Corrected by the review before release. The design opened the start with today's open first and accepted the swap as a residual; see the addendum.)*

**Confinement does not change.** A handle opened by `OpenRoot` from inside a root is a root itself and cannot reach outside the directory it names; each step opens one name that the listing returned. Symlinks are skipped by their entry type, as now. A handle is in fact stricter than today's open: a link inside the project that climbs out of its own directory is refused, where today it is followed if it stays in the project; the walk only meets one by a race, since entries that are links are skipped. The credential refusal does not move: in the child the kernel refuses the open (ADR-0086), and in process `sandbox.CredentialPath` judges the full path before the open, as now. Display paths are built from the same lexical path as now.

**Nothing is given up for the swap.** The first version of this design accepted a directory swapped for a FIFO between the listing and its open as a residual, on ADR-0123 §1's four answers. The review before release showed it was worse than it said: in process, an interrupt did not end the call, and the walk's goroutines, handles and session root stayed held. Opening through `name/.` removes the case. A FIFO that is simply there is listed as a FIFO, is never opened as a directory, and is opened as a file without waiting, as now.

**Open descriptors grow with depth.** One handle is held per level of the walk, and a directory whose files are still being read keeps its handle until they are done (at most `2 × workers` of them, §2). Go raises the process's soft limit to its hard limit at start; on this machine that allows 245,760 descriptors per process (measured the same under the cage), so a tree would need to be that deep. If an `OpenRoot` fails for this or any other reason, the directory is counted as one that could not be listed, as an unlistable directory is today; the walk does not stop.

### 2. Files are read and matched side by side, and used in walk order

The walk stays one sequential walk — the same sort, the same ignore rules, the same `include` filter, the same order. It no longer reads files itself. Every step that the current loop counts or prints becomes an item in one ordered stream:

- a file to search, handed to a pool of workers;
- everything else the loop counts: an ignored directory or file, a file filtered by `include`, a file skipped by size or image extension, a directory that could not be listed, a directory over `DirEntryCap`. A directory's own items (could not be listed, over the cap) come before the items of its entries, as the loop counts them now.

`min(GOMAXPROCS, 8)` workers open, read, sniff and match the files. A worker returns what the current loop computes for one file: the skip reason or the permission refusal, the match count and the first five lines. It does not keep the file's bytes, so memory grows from one 2 MB buffer to one per worker.

The calling goroutine takes the items in walk order and does what the loop does now: it adds the counts, prints the lines, stops at the 200-line cap. Everything that reaches the output is applied there, in that order. A result computed for a file past the point where the call stopped is thrown away. So a capped result counts exactly what the sequential walk counted before it stopped, though the walk may have read a few files ahead.

The walk runs at most `2 × workers` items ahead of the calling goroutine.

**The stream ends with the walk's reason for ending.** The last item says whether the walk finished or stopped on a cancel. A walk that stops at a directory on a cancel therefore still produces the `[interrupted …]` footer; the calling goroutine never mistakes a cut stream for a finished one (ADR-0065 §1: a cut is never silent).

**Cancellation keeps ADR-0065 §1.** The walk checks the context before each directory, the calling goroutine before each item, and a worker every 1024 lines, as now. On a cancel the calling goroutine stops at the item it is on. A file still being read when the call stopped is not counted, and the footer says how many files were scanned. When the call ends — finished, capped or cancelled — it tells the walk and the workers to stop and waits for them, and only then releases the root. On a filesystem that answers, that wait is one system call or 1024 lines long (§3 keeps that bound). A system call that does not return is what the floor of ADR-0065 §2 is for, as now. No goroutine of a call outlives it.

### 3. A pattern with a literal prefix checks each block of 1024 lines before its lines

When the pattern has a literal prefix (`regexp.LiteralPrefix` is not empty), each block of 1024 lines is matched once as a whole. If the block does not match, its lines are counted and skipped without being matched one by one; if it does, they are matched one by one as now. A pattern without a literal prefix is matched line by line as now — the check costs more than it saves there (Context, last table).

A block is cut at a newline, so its lines are exactly the lines `strings.Split` makes, and the context is checked between blocks: the 1024-line bound of ADR-0065 §1 holds.

The check never drops a match, except for a pattern that depends on where the text begins or ends. One by one, `^`, `$`, `\A` and `\z` match at the edges of each line; over a block, only at the edges of the block. The pattern is parsed once with the same flags `regexp.Compile` uses (`syntax.Perl`; with no flags `^` would parse as a line anchor and slip past). If it contains `OpBeginText` or `OpEndText`, the check is not used. `(?m)^` and `(?m)$` parse as line anchors and are safe. The other differences between a block and a line — a class like `[^x]` that can match a newline, `(?s)` — can only make the block match when no line does. That costs one pass over the block's lines and changes no result.

### 4. What does not change

- The 40 ms per call. One child per call is ADR-0086's decision, measured there against the model's round, and a long-lived child would bring its own protocol, lifecycle and restart path.
- The tool's description, its arguments, its caps, its output and its notes. No configuration key is added (ADR-0052 §5).
- `list_tree` and the other tools. `list_tree` opens no files, so §2 and §3 do not apply to it; it was not measured and is not changed here.
- No index, no ripgrep (ADR-0013, ADR-0052).

### 5. What is expected

In the child, the opens fall from about 100 ms to about 40 ms with one worker (54 µs instead of 135 µs per file), and to about 10 ms with eight (Context, cage tables). That predicts the gem-agent tree at 145 ms → about 60 ms, 40 ms of it fixed. `(?i)` should go from 285 ms to under 100 ms, its matching divided by the workers. cli-series should go from 300 ms to under 110 ms. These are cage measurements of the parts, not of the whole: plan step 5 measures the whole.

## Development plan

1. **The judge, written first.** A characterization test that runs the search over generated trees and compares the output with a stored copy produced by v0.108.2.
   - **What it covers:** nested `.gitignore` files with negation; a directory over `DirEntryCap`; files over 2 MB; binary files and images; an unreadable file and an unlistable directory; symlinks to files and directories; a FIFO in the tree; a FIFO and a regular file named as `path`; `include` and `mode="files"`; a search that reaches the 200-line cap in the middle of a directory; patterns with `^`, `$`, `\A`, `\z`, `(?m)^`, `(?i)`, a literal prefix and none; a match on line 1024 and on line 1025; a file without a trailing newline; non-ASCII names.
   - **What it leaves out, and what covers it:** the child under the real profile (the child test below); cancellation (the cancellation test); added roots nested inside the project (`added_root_test.go`, run unchanged); a tree that changes during the call (not covered — the output is defined only for a tree that does not change).
   - **How the stored copy is made:** at the commit before the change, run the judge many times and store only output that came out the same every time. Then break the change on purpose once — drop the anchor exclusion of §3, and apply counts out of walk order in §2 — and see the judge fail each time before it is trusted.
   - The stored copy changes only when behaviour is meant to change.
2. §1, the directory handles, alone. The judge passes.
3. §3, the block check, alone. The judge passes. A table test holds patterns with `OpBeginText` and `OpEndText`, and patterns with no literal prefix, to the line-by-line path.
4. §2, the workers. The judge passes with 1 worker and with 8, and with the race detector on.
5. Measured again against §5, with the same script and the same trees, under the real profile. The patterns of the regular-expression table are run too, so that a pattern without a literal prefix is seen to cost no more than today.

Each step is its own commit.

## Tests the design requires

- **The judge** (plan step 1), run at 1 and 8 workers.
- **Through the real child.** `TestFileChildUnderTheRealProfile` already drives the built binary under the real profile with a `.env` in the project. A case is added with the credential file in a subdirectory, among files the workers read side by side. The result must name it and still count the rest. (The false negative of ADR-0086 is the failure this boundary has had before.)
- **The cap.** Over a tree where the cap is reached early, the output and every count equal the sequential walk's, at 1 and 8 workers, repeated enough times to vary the timing.
- **Cancellation.** The FIFO method of ADR-0065: a reader is held inside one file, then the context is cancelled. The call must return with the partial footer, and no goroutine of the call may be left (counted before and after). A second case cancels while the walk is about to list a directory, and the footer must still appear.
- **Handles are closed.** After a walk over the 59-directory tree, the process holds no more open descriptors than before.
- **A root swapped by `/clear` during a walk.** The walk finishes on the root it started with, and that root is closed only after the walk releases it (the `rootHandle` reference count, as now).

## Verification

Before implementation: one independent pass over this design (done; see below). After implementation: the review before release over the whole difference, and one more over the fixes it led to.

Measurement (plan step 5) is done once. If a figure misses §5 by more than half the predicted gain, the measurement is reported and nothing more is tuned in this ADR.

## Alternatives considered

- **Workers only, opening by full path.** This runs the 135 µs opens side by side. Each open still costs nearly three times what it needs to, and depth still multiplies it. §1 is the cheaper half, and the one without timing in it.
- **Walk directories side by side too.** Listing is under 5% of the time; it would make the order and the counts harder to keep equal for nothing measured.
- **Open each name with `openat` and `O_NOFOLLOW` from a directory descriptor.** This keeps `O_NONBLOCK` on directories too. But the confinement would be written by this repository instead of by `os.Root`, and only for a case ADR-0123 §4 already accepts.
- **Check the whole file at once, for every pattern.** This was the first draft of §3. The design review measured it costing more than the line loop for patterns without a literal prefix (up to twice as much). It also leaves no point to check the context inside a 2 MB file: up to 0.8 s in the review's probe, against the 1 s grace of ADR-0065 §2.
- **A faster path for `(?i)`** — extracting a literal from the pattern and searching for it case-folded first. Possible, and more code that has to agree with Go's case folding; the workers divide the time already. Not now.
- **A long-lived child.** Saves the 40 ms; ADR-0086 declined it for the protocol, lifecycle and `/clear` coupling it brings, and nothing here changes that.
- **An index, ripgrep.** Declined by ADR-0013 and ADR-0052; nothing here changes that.
- **Do nothing.** One call saved 100 ms is not felt against a model round. Recorded as the choice put to the operator; the gains above are where calls add up.

## The review of the design (2026-11-23)

One independent pass. It checked the design against the knowledge base, the workspace memory and the project's rules, and it tested the central claims with its own probes. Six findings, all taken:

1. **Medium — a FIFO named as `path` waited.** The first draft opened the start of the walk with `OpenRoot`, which has no `O_NONBLOCK`; a probe stayed blocked, where today the call returns at once. → §1, "the start of the walk is opened as now first".
2. **Medium — checking the whole file cost more, and could not be interrupted.** The first draft checked every file whole. For patterns without a literal prefix that was slower than the line loop, and the check held off the context for up to 0.8 s. → §3 checks blocks of 1024 lines, and only for a pattern with a literal prefix. Re-measured: Context, last table.
3. **Low–medium — a cut stream could lose its footer.** → §2, "the stream ends with the walk's reason for ending".
4. **Low — the measurements mixed where they were taken.** → every table names where it was measured. The per-file open costs and the worker scaling were measured again under the real profile, and §5 now rests on them.
5. **Low — the judge did not say what it leaves out, and was stored from one run.** → plan step 1 lists what it covers and what it leaves out, stores only output that repeats, and is shown to fail on a deliberate break.
6. **Low, plausible — descriptors grow with depth.** → §1, "open descriptors grow with depth", with the limit and what a failed open does.

The review also confirmed the §3 equivalence with 200,000 random inputs per pattern. The inputs mixed `\n`, `\r`, invalid UTF-8, U+FFFD, trailing newlines and empty files. The only misses came from `OpBeginText` and `OpEndText`. It found no other difference in the output under the ordered stream. It confirmed that confinement is the same or stricter.

## Addendum: the implementation (2026-11-23)

Implemented in the order of the development plan, one commit per step: the judge, §1, §3, §2.

### Measured again (plan step 5)

The same script, trees and profile as the Context's first table. Each figure is the median of five warm runs, both binaries measured in the same sitting.

| Tree | Pattern | v0.108.2 | Now | §5 expected |
|---|---|---|---|---|
| gem-agent | literal, not found | 147 ms | 71 ms | about 60 ms |
| gem-agent | `func New` | 142 ms | 68 ms | about 60 ms |
| gem-agent | `func \w+Registry` | 160 ms | 72 ms | about 60 ms |
| gem-agent | `(?i)thought signature` | 272 ms | 89 ms | under 100 ms |
| cli-series | literal, not found | 361 ms | 112 ms | under 110 ms |
| cli-series | `(?i)thought signature` | 561 ms | 149 ms | — |

The fixed cost is unchanged (40–43 ms). Every figure is within half of the expected gain, so nothing more was tuned (§Verification). Two patterns without a literal prefix were run as well (three runs each), to see that they take the line-by-line path at no extra cost. `(\w+\s+){10}zzzz` went from 545 ms to 156 ms. `(?i)(\w{1,30}\W{1,5}){1,30}QQQ` went from 1,755 ms to 457 ms. Both now gain from the workers.

### What the judge was shown to catch

Each deliberate break was put in and taken out again:

- §3 without the anchor exclusion: three scenarios failed (`^`, `$`, CRLF).
- §2 counting past the cap: the cap scenarios failed at both worker counts.
- The walk taking directories before files: ten scenarios failed.
- A worker that does not release its directory handle: the descriptor test failed (8 before, 15 after).

### Where the implementation differs from the design, and why

- **The walk runs at most `2 × workers` items ahead, counted with the one it is waiting to hand over.** The stream's buffer holds `2 × workers − 1`.
- **A test hook.** `searchTestHook` lets a test act when the walk is about to list a directory or a worker is about to read a file. The cancellation and `/clear` tests use it. A FIFO can no longer hold a read, because the file open does not wait (ADR-0072 §4.8). The hook is nil outside tests.
- **The descriptor test checks that the count does not rise.** The design said the process holds "no more" descriptors after the walk. One run counted one fewer after than before, because the runtime closed a descriptor of its own.

## Addendum: the review before release (2026-11-23)

One independent pass over the whole difference, and one over lagent's port of it (lagent ADR-0055). Each finding was checked against the other runtime, which has the same code.

1. **Medium — a directory swapped for a FIFO held the call past an interrupt, in process.** `OpenRoot` sets neither `O_DIRECTORY` nor `O_NONBLOCK`, so the walker waited in `openat`. The calling goroutine waits on the stream in order, and the call waits for its goroutines. So in process (no cage, or an operator-approved read) the interrupt did not end it: the floor of ADR-0065 §2 abandoned the call, and its goroutines, its directory handles and the session root stayed held, so a root rotated by `/clear` never closed. In the child the parent kills the process. Both reviews reproduced it. → Directories, the start included, are opened through `name/.` (§1, corrected), which refuses a FIFO at once. A regression test swaps a listed directory for a FIFO and fails without the fix.
2. **Medium–low — descriptors grow with depth, and a tree deeper than the limit loses a directory.** With the hard limit lowered to 128, a 200-deep tree counted one directory as unlistable where v0.108.2 found its match. Accepted, as §1 records it. Go raises the soft limit to the hard one: 245,760 on this machine, and macOS's 1,024-byte path limit keeps a tree to about 500 levels. A directory lost this way is counted, never silent.
3. **Low — a result waiting in walk order held its whole file.** A kept line was a substring of the file's text, so up to about 24 files of up to 2 MB stayed alive. → A worker keeps the clipped line it will print, copied, and keeps none in `mode="files"`. Output unchanged, with a test.
4. **Low — the random equivalence test rarely crossed a block edge.** → A quarter of the texts now run to thousands of lines, and the test requires at least 500 of them to span more than one block.
5. **Low, accepted — a walk finishes on the root it started with.** v0.108.2 resolved every open from the current root, so a root rotated by `/clear` mid-walk refused the rest of the walk. Now the walk reads that directory to the end. The operator's own act starts it, and it reads nothing that was not readable a moment before. Item 1 bounds it.

### Why the design missed these: what the sequential loop held without saying so

The five findings are one cause, not five accidents. The design restated one property of the old loop, its output, and judged the change by it. The loop also held other properties because it was one loop in the calling goroutine, and moving its work onto goroutines the call waits for changed each of them silently. Every finding is one of those properties. So instead of a further review round, every property of the loop was listed and each was checked in the new code:

| What the sequential loop held | Now | How it is held |
|---|---|---|
| Output: counts, order, cap, footers | the same | the judge, at 1 and 8 workers, with deliberate breaks |
| Every open does not wait (ADR-0072 §4.8) | broken by `OpenRoot`, fixed | `openDirRoot` (`name/.`). `TestFileToolOpensDoNotWait` judges every reference to an open over this package and `internal/ignore` (calls, method values, package-level functions). It admits only a call through `openRegular` that passes on its flag, a call with `O_NONBLOCK`, an exclusive create, or a listed file and function with its reason. It fails on the shape that shipped and on each gap the verification found |
| A cancel returns within one system call or 1024 lines (ADR-0065 §1) | the same, given the row above | the call waits for goroutines that each stop within that; the cancellation tests |
| One file's bytes in memory | broken twice, fixed | kept lines were substrings of the file (review); each worker also held a string copy and a string per line (verification). `scanLines` matches over the file's bytes and copies out only the lines it keeps; a test bounds a scan's allocations below a quarter of its file |
| Nothing is opened past the cap | broken, fixed (missing from the first version of this table) | the walk checked only the context, so a subtree with nothing to emit was walked to its end after the cap; it now also sees the call stop. A test caps in the first directory and counts what is listed after |
| A few descriptors | grows with depth | accepted (§1): bounded by the path limit, and a loss is counted |
| Every open resolved from the current root | the walk keeps its start root | accepted (finding 5) |
| Only files that are counted are read | up to about 3 × workers files are read ahead past a cap or a cancel | accepted: they are in the project, and a search without the cap would read them; on an online-only file this can start a download |
| The in-process credential refusal on every file | the same | `CredentialPath` on each full path; the child under the real profile |
| Confinement to the roots | the same or stricter | each step opens one listed name through a root |
| A panic in the tool ends the process | the same | no recover anywhere in the runtime, before or after |
| No goroutine outlives the call | the same | the call waits; the goroutine-count tests |

Of these, only the open can come back with a later edit, so it is the one held by a test of the code's shape rather than by a test of behaviour.

### The verification of the fixes (2026-11-23)

One pass, bounded before it started: it was asked only whether the table above misses a property, and whether each row it claims is held. It found one missing row: nothing is opened past the cap. It found one row overstated: memory. And it found five gaps in the open test. All were in the table's class, and are fixed as the table now records. Each fix's test was shown to fail with the fix taken out. No finding was serious, so the design stands.

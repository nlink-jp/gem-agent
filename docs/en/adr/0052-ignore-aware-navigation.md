# ADR-0052: ignore-aware navigation — the walks stop scanning what the project ignores

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-28 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator field report: small projects navigate fine, but as a project grows the model lists and searches more and more, responses slow down, and noise drowns the targets — followed by "avoid external dependencies where possible; let's do everything we can" on the diagnosis below. |

## Context

ADR-0013 shipped `list_tree` and `search_files` on the premise that
"at project scale, a sequential scan is enough — no index needed."
The premise was right about the wrong denominator. Measured on a real
Tauri project in this workspace (19,145 files), reproducing
`nav.go`'s walk exactly:

| | current walk | generated dirs skipped |
|---|---|---|
| time (warm cache) | 1.4 s | 10 ms |
| time (cold cache) | 27.7 s | 15 ms |
| files content-scanned | 11,960 | 112 |
| `fn main` matches | 7 (5 inside `node_modules` READMEs) | 2 (the real ones) |

The project's own files number **130 — 0.7% of what the walk scans**.
The other 99.3% is `node_modules`, `target`, `dist`, `build`:
dependency and build output the project itself declares as
not-project in its `.gitignore`.

Latency is the smaller half of the damage. The walk is alphabetical,
and `node_modules` sorts before `src`:

- `list_tree`'s 800-entry cap was measured 86% consumed by generated
  directories; the walk died inside `node_modules/aria-query` without
  ever printing `src/` or `src-tauri/`. Orientation in one call is
  structurally impossible — the model re-calls with subdirectory
  paths, which is exactly the observed "more calls per task".
- A search for a common identifier can exhaust the 200-match cap
  entirely inside dependencies and show **zero** project matches.
  The tool then honestly reports "narrow the pattern" — honest, but
  the narrowed retry scans the same garbage again.

ADR-0013's deeper rejections are untouched by this diagnosis: no
ripgrep prerequisite, no persistent index, no embeddings. The fix is
not to scan smarter — it is to stop scanning what the project itself
says is not the project.

The operator set two constraints for the fix: avoid external
dependencies where possible (the `.gitignore` matcher is implemented
in this repository, not imported), and do everything that can be
done (all three parts below ship together; they reinforce each
other — skipping garbage makes the caps meaningful, and reshaped
caps make the skip visible).

## Decision

### 1. Enumeration walks are ignore-aware, in two independent layers

`list_tree` and `search_files` (and `agentic_file_search`'s child,
which uses the same registry) skip, during the walk:

- **Built-in layer**: directories whose basename is a well-known
  dependency or build-output name (`node_modules`, `vendor`,
  `.venv`, `dist`, `build`, `target`, `__pycache__`, `Pods`,
  `DerivedData`, …— one curated list in `internal/ignore`, stated
  in the tool descriptions). This layer works in projects that are
  not git repositories at all.
- **`.gitignore` layer**: full gitignore(5) semantics — nested
  `.gitignore` files, negation, anchoring, `**`, dir-only patterns,
  character classes, last-match-wins, deeper-file-precedence —
  implemented in `internal/ignore` with no new module dependency.
  ADR-0013's "dependency-free" was about runtime binaries (ripgrep);
  the operator's constraint for this ADR extends it to module
  dependencies, so the matcher is ours, tested against `git
  check-ignore` as ground truth where git is available.

The layers are independent: either can skip an entry, and a
`.gitignore` negation cannot re-include what the built-in layer
skips. The escape hatch is `include_ignored=true` (below), not a
pattern.

### 2. Ignoring filters enumeration only — explicit paths always reach

`read_file`, `file_info`, `view_image`, `read_document`,
`edit_file`, and every other tool that takes an explicit path are
untouched. A hostile or over-broad `.gitignore` can hide a file from
*discovery*, never from a named read. Three honesty rules keep the
filter from becoming silent:

- **Every skip is reported.** Ignored directories appear in the tree
  as `name/ [ignored]` — their existence is information ("this
  project has `node_modules`") even when their contents are noise.
  Both tools end with an aggregate line naming what was ignored and
  the parameter that includes it.
- **`include_ignored=true`** on both tools disables both layers for
  that call.
- **A walk rooted inside an ignored area shows everything.** If the
  caller explicitly passes `path=node_modules/foo`, the intent is to
  look there; ignoring the whole walk would return a mystifying
  empty result. The call proceeds with layers off and says so.

Security note: a repository's `.gitignore` is repository-authored
data steering the walk. It can only *hide* content from enumeration
(never execute, never exfiltrate), the hiding is reported in-band,
and named reads bypass it. That residual — an adversarial repo
hiding a file from casual discovery — is accepted and documented
here rather than defended against, because the same adversary can
name the file `x.png` and defeat enumeration heuristics anyway.

### 3. `search_files` output answers "where", not "first 200 lines"

- **Per-file cap**: at most 5 match lines are shown per file,
  followed by `… +N more in this file`. The global 200-line cap now
  spans ≥40 files instead of potentially one — the capped result
  carries distribution, not the alphabetical head.
- **`include` filter**: a gitignore-syntax pattern (same compiler,
  same semantics, anchored at the walk root) that limits which
  *files* are content-scanned — `include="*.go"`,
  `include="src/**"`. Directories are still walked.
- **`mode="files"`**: per-file counts only (`path (N matches)`), no
  match lines — the "which files use this identifier" question at
  minimal token cost. The description steers the model to start
  broad searches here.
- Total match counts (shown or not) are reported in the summary, so
  a capped result still states the true size of what it capped.

### 4. `list_tree` budgets per directory, not globally

- **Per-directory elision**: at most 50 entries are printed per
  directory, then `[+N more entries]`. One huge directory can no
  longer starve every sibling that sorts after it. The global
  800-entry cap remains as a backstop and is now rarely reachable.
- **`dirs_only=true`**: directories only, annotated with the count
  of files each contains — orientation over a large tree in a
  screenful.
- `list_files` (single directory, non-recursive) is not part of the
  problem, but its entries gain the same ` [ignored]` annotation so
  the model learns not to descend before it tries.

### 5. What stays rejected

- **Persistent index / trigram index**: ADR-0013's rejection stands.
  The freshness invariant (the agent edits files mid-session) plus
  the measurement above — 10 ms warm once the garbage is skipped —
  leaves nothing for an index to earn.
- **ripgrep**: still a runtime dependency fallback with two
  behaviors; still rejected.
- **Parallel walk**: measured unnecessary after ignoring (10 ms warm
  on the 19k-file project). Revisit with measurements if a genuine
  monorepo (10⁵+ *project* files) hurts; do not build it
  speculatively.
- **Config surface for the built-in list**: no new keys. A project
  that needs more ignoring writes its own `.gitignore` (which also
  fixes every other tool that reads it); the built-in list covers
  projects that have none.

## Consequences

- The measured project: orientation in one `list_tree` call (the cap
  untouched), searches at ~10 ms with only project files matched,
  and the model sees `node_modules/ [ignored]` instead of 690
  entries of it.
- `agentic_file_search`'s child inherits everything — its 10-round
  budget now buys ten *useful* calls in large projects.
- Two more parameters on each navigation tool, and gitignore
  matching (~200 lines plus tests) to maintain. The matcher is
  validated against `git check-ignore` on the constructs gitignore(5)
  defines; divergence found later is a bug in our matcher, and the
  test that proves it belongs in `internal/ignore`.
- Results shrink and reorder for projects with generated dirs;
  transcripts from earlier versions replay fine (tool results are
  data, not schema).

## Amendments

- ADR-0013's premise clause "no index — at project scale a
  sequential scan is enough" is amended to "…a sequential scan *of
  the project* is enough"; its Status row now points here.

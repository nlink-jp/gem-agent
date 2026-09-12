# ADR-0084: The runtime supplies routes, not rules — the toolchain cache rides the scratch, a denial names what can still run, and the prompt says what is true

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-12) — implemented and unreleased |
| Date | 2026-09-12 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | lagent's task bench (lagent ADR-0006/0008) traced a run that ended in prose to a chain of sentences the runtime had written; the operator asked whether gem-agent shares them. It does, measured on the same machine |
| Relates to | ADR-0073 (the lanes; the read lane's writable set is unchanged), ADR-0060 (denial text carries the operator's reason), ADR-0065 (one-shot has nobody to ask), ADR-0052 (list_tree reports what it skipped), lagent ADR-0008 (the source design; adopted with gem-agent's lane and mode vocabulary) |

## Context

lagent's bench read one failed run backwards and found that every
step followed a sentence the runtime had written: the prompt asked for
a verification in the write lane; the read-lane attempt had failed
because Go's build cache lives under `~/Library/Caches/go-build`, which
no lane may write; the write-lane escalation was denied because the
run was unattended; the denial told the model to ask a user who was
not there; the model stopped acting and described the change it would
have made.

gem-agent has each sentence. Probed 2026-09-12 on the operator's
machine, in a fresh Go module:

```
[tool] shell_exec [read] go vet ./...
open ~/Library/Caches/go-build/dd/…-d: operation not permitted   (exit 1)
```

- **The prompt sends every build and test to the write lane** ("build
  and test tools write their caches, so they need it too"), so the
  read lane is not even tried — but a vet or a test is inspection,
  and under `--auto` each one costs two model-tier rounds (about seven
  seconds on the risk slot), or an approval prompt in the default
  mode. That was true in this session's own release checks.
- **The denial text is the same in every mode**: "ask the user how to
  proceed" reaches the model in `-p`, where ADR-0065 has already
  established that nobody can be asked. The operator-facing line says
  "nobody to ask in one-shot mode"; the model-facing result does not
  say what can still run.
- **`list_tree dirs_only` reports a directory with files and no
  subdirectory as "(empty directory)"**, and the model spends a round
  on `list_files` to learn otherwise. The prompt tells it to start
  with `dirs_only`.
- **The prompt keeps a rule the runtime no longer needs**: "a denial is
  a decision, not an obstacle — ask how to proceed instead of
  retrying". With the denial carrying its own route, the rule is one
  more standing sentence a model skips or over-generalizes.

lagent's bench comparison also measured gem-agent taking two to three
times the rounds on the same tasks (rename 23, read-edit 15). The
chain above is part of that.

## Decision

Four changes, each replacing a sentence the model could not act on
with a fact it can. lagent's ADR-0008, in gem-agent's terms.

1. **The toolchain caches ride the session scratch, in every lane,
   from one operator-owned table.** `[sandbox].scratch_caches` maps an
   environment variable to a directory name; every `shell_exec` runs
   with each variable pointing at that directory under the read lane's
   private scratch (`<work dir>/scratch`, the one directory the read
   lane may write, which the write and operator lanes may write too).
   The shipped row is Go's, `GOCACHE = "go-build"`; an operator whose
   projects use another toolchain adds its row (`PIP_CACHE_DIR`,
   `UV_CACHE_DIR`, `npm_config_cache`) without a code change, and an
   empty value removes a row. Builds, vets and tests therefore run in the
   read lane without approval, as inspection does; the cache is cold
   once per session and warm after. It is deliberately not the
   operator's shared cache: the read lane runs unasked, and a shared
   content-addressed cache is where an unasked command could plant an
   object a later build outside the sandbox would trust. The same
   edge exists inside the session, so the unasked lane and the
   approved lanes get separate directories, for every row: the read
   lane's cache is the table's name (`go-build`), the write and
   operator lanes share `<name>-approved`. A read-lane command
   steered by what it read cannot seed an object that an approved
   `make build` links and the operator then runs. The table is bounded
   by what it is for: a row must name a regenerable cache, so the
   variables `laneEnv` decides (`TMPDIR`, `PATH`, `HOME`) and the
   loader variables (`DYLD_*`, `LD_*`) are refused — a directory the
   read lane writes must never become code the approved lanes load —
   and a value is one directory name, never a path. It is global
   config only: the project file carries `[approval.tools]` and
   `[mcp].exclude` and nothing else, so a cloned repository cannot
   add a row. Module and registry stores (`GOMODCACHE`, `CARGO_HOME`)
   are not caches in this sense and do not belong in it. The read
   lane's writable set (ADR-0073 §2) does not change — the caches
   moved into it.
2. **An unattended denial names the route.** `Options.Unattended` tells
   the agent that this run has nobody to ask (`-p`); a denied call's
   result then says that no one can approve the call and names what
   runs in every one-shot configuration — the read-only file tools
   and the read-lane shell — or that the run should finish and state
   what remains undone. It does not name the write tools: without
   `--auto` or `--allow` they are exactly what was denied, and a route
   into a second denial is the pathology this section removes. The
   interactive text keeps "ask the user". A denial carrying the
   operator's reason (ADR-0060) and the read-only ceiling's refusal
   (ADR-0080, "the operator can lift it") keep their first line and
   follow the same rule for the closing one.
3. **`list_tree` says what it saw.** With `dirs_only`, a directory that
   has files but no subdirectories lists those files (up to the
   per-directory cap) instead of reading as empty. lagent measured
   the alternative — a count plus "call list_files" — and the model
   followed the pointer every time, so the round was spent anyway; a
   route the tool can walk itself is not a route to name.
4. **The prompt says what is now true and drops the rule the runtime
   replaced.** The lane paragraph: the read lane runs inspection *and*
   compiling, vetting and testing — the toolchain cache lives in the
   lane's scratch — while a build that writes its binary into the
   project (`go build` of a main package) still needs the write lane;
   the write lane is for changing files, installing, committing and
   the network. The verification bullet names the read lane. The
   "denial is a decision" bullet goes. `shell_exec`'s own description
   says the same.

## Consequences

- Vets and tests stop paying the model tier or an approval prompt;
  the write lane is left for what changes the project or reaches the
  network. In this session's terms, the release checklist's `go test`
  runs unasked.
- A Go build inside the sandbox compiles cold once per session per
  trust class (the read lane's cache and the approved lanes' cache
  are separate); the operator's own cache is untouched by anything
  the agent ran.
- The cache lives and dies with the work directory. Nothing deletes
  it: `/clear` rotates to a new work directory and leaves the old
  cache behind, and a cold Go cache is tens to hundreds of megabytes
  (20 MB for a `go vet` of a one-file module, measured). The
  startup report of earlier work directories counts it, and
  `gem-agent workdirs clean` is the remedy (ADR-0059); a deletion the
  runtime performs on its own is not added here.
- Without a scratch (the read lane disabled, or its directory could
  not be created) nothing is redirected and the cache stays under
  `~/Library`, unwritable in every lane — the state before this ADR,
  not a new one.
- `go test` in the read lane runs project and dependency code
  without approval. The lane bounds it — no network, no write outside
  the scratch, no credential read — but the unasked surface is wider
  than `ls`, `cat` and `grep`, and this line records that.
- Module downloads are not covered: `~/go/pkg/mod` is readable and
  not writable in any lane, so a project whose modules are not already
  cached cannot fetch them from inside a run. A gap, recorded, not
  solved here.
- One-shot runs that hit a gate end with the work that was possible
  done and the remainder stated, instead of a description of what the
  model would have done.
- One Options field, one environment list, one prompt paragraph, one
  bullet removed, one `list_tree` case. Measured before merging: the
  probe above passes in the read lane; a flat directory lists its
  files; a one-shot denial's result names the route.

## Alternatives considered

- **A prose rule against explaining instead of editing, or for acting
  without asking in one-shot** — rejected: a standing rule against a
  behaviour the runtime provoked; the knowledge base's measured
  outcome for such rules is that they are skipped or over-generalized.
- **Allow writes to `~/Library/Caches` in the lanes** — rejected for
  the read lane (an unasked command writing a shared content-addressed
  cache is a poisoning route) and therefore for the write lane too,
  since the point is that a build works in the read lane.
- **A hard-coded list, one toolchain per code change** — the first
  cut; replaced before release (operator review): the runtime has no
  reason to know which toolchains a machine runs, and every addition
  would have cost a release. The table is the operator's; the
  runtime's knowledge is the rule that applies to every row (scratch
  placement, the per-lane split, what a row may not name).
- **One cache directory for every lane** — rejected on review: Go
  trusts a cache entry on read, so the unasked lane could plant an
  object an approved build consumes. Two directories cost one extra
  cold compile per session.
- **Route the `shell_exec` lane refusal too** — not done: "needs the
  wider lane it names" leads, in one-shot, to the unattended denial,
  which names the route; one extra round, and the text stays true.
- **Auto-approve write-lane builds in one-shot** — rejected: the write
  lane reaches the network and the project; "build" is a word in a
  command line, not a lane, and the rule tier does not read command
  text for intent (ADR-0073).
- **Keep the model out of `dirs_only` on small trees** — rejected: the
  orientation advice is fine; the tool's report was false.

## References

- lagent ADR-0008 — the source design and its bench measurement
  (18/18 tasks after; the read-edit task's verification succeeding in
  the read lane)
- ADR-0073 §2 — the read lane's writable set, which this record keeps

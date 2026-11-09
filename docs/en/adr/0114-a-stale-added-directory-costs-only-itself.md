# ADR-0114: A stale added directory costs only itself

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-09) |
| Date | 2026-11-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Porting ADR-0099 to lagent (its ADR-0046): an independent design review found four defects in the code the two share, and lagent fixed them. Each was reproduced here on v0.100.1 before this record was written |
| Amends | ADR-0099 §3, §5, §6, §12 |
| Relates to | ADR-0102 (`ownRootsUnderReach`, the rule §3 below applies to added roots), ADR-0086 (the file-read child), ADR-0107 §4 (probe leftovers) |

## Context

ADR-0099 treats an added directory as something the operator typed for this run, and a directory that stays reachable for as long as the session lasts. Neither always holds. `[directories]` in the global file is written once and read from every project, and a volume or a share can go away during a session. Four places act on the old assumption. Measured on v0.100.1:

1. **A configured path that does not fit this project stops the start.** `[directories].write` naming a directory that holds the current project, one already covered, or a path that is not there now (a volume not mounted) each made `resolveAddedDirs` return an error, so gem-agent could not start in that project at all.
2. **`/clear` leaves the write lane behind.** With a write root that no longer passes its probes, `rotateWorkDir` moved the file tools to the new work directory, then kept the previous sandbox profile. The write lane exited 1 writing the new work directory, for the rest of the session.
3. **A read root could reopen a tree the read lane keeps out.** `RefuseRoot("/Volumes", false, …)` accepted it, and the re-allow of an added root, coming after the deny, handed the unasked read lane every mounted volume. `ownRootsUnderReach` refuses exactly this for the project and the work directory (ADR-0102).
4. **Every file read opened every added root.** The file-read child calls `os.OpenRoot` on all of them for each tool call. With one root that would not open (measured with a FIFO at the root's path, which blocks an open the way a dead share does), reading an ordinary project file never returned.

## Decision

1. **A configured path that does not apply is passed over, and the operator is told why.** A refusal that depends on where this session started or on what is mounted now (`sandbox.Situational`: not found or not openable now, holding the project or the work directory, already covered) skips an entry from `[directories]` with a note at startup. The same path given as `--add-dir` or `--read-dir` still stops the start: it was typed for this run. A configured path refused for any other reason (the home, a credential location) also still stops the start, because it is wrong from every project.
2. **`/clear` drops the root that no longer passes its probes and keeps the others.** `VerifyAddedRoots` names the failing root in a typed error (`sandbox.AddedRootError`). `rotateWorkDir` rebuilds without that root, at most once per root, and removes it from the registry (`Registry.DropAddedDirs`), the watch and the transcript's record, and the operator is told; `/add-dir` adds it again. The watch goes too, because the next walk of an ejected volume would report everything in it as removed. A failure that does not name a root keeps the previous profile, as before.
3. **A root may not be, or contain, a tree the read lane denies for reach.** `RefuseRoot` applies `ownRootsUnderReach`'s rule to added roots, at both levels and on every route: flags, the configuration, `/add-dir` and `request_directory`. `/Volumes` and `/Network` are refused; a directory inside them is what a root is for. `~/Library` was already refused, since it holds a credential location. This refusal is not situational: such a path is wrong from every project.
4. **The file-read child opens only the roots a call's path arguments lie in** (`rootsNamedBy`). A relative argument is joined to the project first, as `resolvePath` does, because `../ref/x` reaches an added root beside the project. Every string argument is examined, not only `path`: opening one root too many costs nothing, and missing one fails the read.

## Consequences

- `--read-dir /Volumes`, or the same path in `[directories]`, used to start and now stops the start with the reason and what to add instead. Nothing else that worked stops working.
- A dropped root stays out for the rest of the process. Coming back from an ejected volume is one `/add-dir`.
- The residual: a read that does name a stalled root stalls, and the startup judgement and `/clear`'s probes of a stalled share can stall too. Only a mount that has stopped answering does this, and the floor that gives up on a stuck call (ADR-0065) still applies to the read.

## Alternatives considered

- **Drop every added root when one fails at `/clear`** (what lagent does) — not taken: one ejected volume should not cost the notes directory beside it, and naming the failing root makes the change no larger.
- **Skip situational refusals for flags too** — not taken: a path typed for this run that did not apply is what the operator must hear (ADR-0099 §3).
- **Decide by absolute paths only in the child** (what lagent does) — not taken: here it would fail a relative path that reaches an added root, which works today.
- **A deadline around each open in the child** — not taken: a timer does not free an open blocked in the kernel, and every read would still wait it out. Not opening the root is the whole fix.

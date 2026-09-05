# ADR-0022: Per-project session layout and a state-dir override

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the flat transcript layout itself may be the problem — use per-project subdirectories like memory does |


*Amended by ADR-0074: the per-project state directory also holds
`persistent.json`, the digests of the persistent files under the project
as the last session left them (advisory change notes at startup).*

## Context

Session transcripts lived flat in `~/.local/state/gem-agent/sessions/`,
every project's files side by side, with the owning project recorded
only inside each file's header. Three costs, one of them just paid in
blood: a cleanup glob aimed at test sessions matched the operator's
real transcripts too (all ids start with a date — a flat shared
directory gives a glob zero selectivity); `--continue` and the listing
must open and describe every file across all projects to filter by
header; and the project binding that resume enforces is invisible in
the filesystem. Memory (ADR-0020) already answers all three with
`projects/<escaped-path>/` subdirectories.

## Decision

1. **New sessions live under
   `~/.local/state/gem-agent/sessions/projects/<escaped-path>/`,**
   using the same lossy `/`→`-` escaping and the same `.project`
   marker as memory (a mismatch means an escape collision — the
   directory is skipped with a note, never misattributed). Deleting a
   test project's sessions is now deleting inside that project's own
   directory; the listing reads one directory; the binding is visible.
2. **The escaping and marker logic is extracted to one shared package**
   (`internal/statedir`), used by memory and sessions both — one
   convention, one implementation, one set of tests. Memory's paths are
   byte-identical before and after the refactor.
3. **Legacy flat files stay readable, in place, forever.** Listing
   merges the project subdirectory with header-filtered flat files;
   resume finds a session in either location and appends to it where it
   lives. Nothing is moved: the operator chose zero-file-motion over
   auto- or command-driven migration, days after a deletion incident —
   old sessions age out naturally.
4. **`GEMAGENT_STATE_DIR` overrides the state root** (the parent of
   `sessions/` and `memory/`) — primarily so tests and drills can run
   against an isolated state tree instead of the operator's real one,
   which is the structural fix for the incident: an E2E that cannot see
   real transcripts cannot delete them. Follows the GEMAGENT_* env
   convention; not a config key (relocating state via config invites
   the persisted-path drift problem, and tests need env anyway).

## Consequences

- Cleanup, listing, and binding all become per-project structural
  facts rather than header-filtered conventions.
- `session.Open/Reopen/Find` gain a projectDir parameter; the header's
  Project field stays as defense in depth and for legacy files.
- Two sessions in different projects may share an id; resume resolves
  within the current project (plus legacy flat), so this is invisible.
- E2E runs set `GEMAGENT_STATE_DIR` to a scratch tree; the drill
  runbook can too.

## Alternatives considered

- **Auto-migration (rename on startup)** and **an explicit migrate
  command** — offered; the operator chose read-in-place (§3).
- **Config key for the state root** — rejected; env-only (§4).

## References

- ADR-0005 (transcripts as resume source of truth — unchanged)
- ADR-0020 (the layout and marker convention this adopts)
- ADR-0021 (the incident record; the iron deletion rules this
  structurally supports)

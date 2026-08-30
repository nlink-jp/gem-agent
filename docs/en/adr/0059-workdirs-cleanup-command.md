# ADR-0059: `workdirs` — the cleanup half of the work-directory report

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-31 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "the startup note warns about accumulation but there is no cleanup tool — isn't that half a feature?" |
| Amends | ADR-0058 §5 (nothing is deleted automatically) |

## Context

ADR-0058 deliberately deletes nothing: the work directory's files are
the point, and retention is the operator's call. To keep that from
becoming silent accumulation, startup reports how many earlier session
work directories exist and how much they hold.

The approved plan said "deletion is an explicit command only". The
report shipped; the command did not. So the note tells the operator
about a problem the tool gives them no way to act on — a report without
a remedy, which trains people to ignore the report.

## Decision

A `workdirs` CLI subcommand, parallel to `sessions`:

- **`gem-agent workdirs`** lists the current project's session work
  directories — id, last-modified age, file count, size — with a total.
  `--all` lists every project's. Listing is the dry run; there is no
  separate `--dry-run`.
- **`gem-agent workdirs clean`** deletes them, with two shapes:
  explicit ids (`clean 20260831-022743 …`), or bare `clean` for every
  non-live directory of the current project. Cross-project cleaning is
  deliberately absent: run it where the note appeared.
- **Human confirmation is the default.** The command prints exactly
  what will be removed (ids, sizes, total) and asks; EOF or anything
  but `y` aborts — the same deny-on-EOF stance as the approval gate.
  `--yes` skips the question for scripts; a non-TTY run without `--yes`
  refuses rather than silently consenting.
- **A live session's directory is never deleted.** Every running
  session holds a non-blocking exclusive flock on its transcript for
  the logger's lifetime (ADR-0021), so liveness is a shared-flock probe
  on the transcript: `EWOULDBLOCK` means running, and the directory is
  skipped with a note. A work directory with no transcript at all (an
  isolated-state run, a deleted log) counts as not live.
- **CLI, not a slash command.** Freeing disk must not require starting
  a model session — this is a backup tool, and its maintenance surface
  has to work when nothing else does. The startup note now points here:
  `review with 'gem-agent workdirs'`.

Deletion itself is `os.RemoveAll` on a path the tool computed from a
validated single-segment session id — never an operator-supplied path,
so there is nothing to traverse. The org's manual-deletion rules (full
paths, no recursion) govern ad-hoc agent shell work; a product command
deleting its own state under explicit confirmation is the shape those
rules point to, and the same one `delete_workspace` (data-toolbox) and
`cache --clear` (lookup tools) already use.

## Consequences

- The startup note gains its remedy, and its singular form its grammar
  (`1 … directory holds`, not `hold`).
- The note and the command speak English on stderr/stdout like every
  other startup diagnostic and CLI subcommand — deliberately outside
  the ja/en uitext catalog (ADR-0029), which covers the interactive
  session chrome. Recorded as a decision so it is not mistaken for an
  omission.
- `internal/workdir` gains `List` and `Remove`; `Sweep` becomes an
  aggregate over `List`, so the note and the listing cannot disagree.
- `internal/session` gains `InUse`, the flock probe. It is advisory
  like the lock it probes: a state root on a filesystem without flock
  reports not-live, which fails toward a prompt the operator reads,
  not toward silent deletion — the confirmation still lists every id.

## Alternatives considered

- **Age- or size-based automatic cleanup.** Rejected: it re-crosses the
  line ADR-0058 drew. An agent deleting output the operator has not
  looked at is the failure mode, whatever the threshold.
- **A `/workdirs` slash command instead.** Rejected as the primary
  surface: it requires a live session, and cleanup is exactly the task
  you do when you are *not* in a session. Nothing prevents adding it
  later as a convenience view.
- **`--keep`/`--older-than` selectors.** Deferred (scope minimalism):
  explicit ids and all-non-live cover the observed need; selectors can
  be added when a real session pattern demands them.

# ADR-0101: A project is its directory, not its spelling

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-01) — implemented |
| Date | 2026-11-01 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The sibling runtime (lagent) refused `--resume` in the directory a session was recorded in, naming two paths that print identically; gem-agent resolves and compares the project the same way |
| Amends | ADR-0022 §1 (the `.project` marker matches its directory by identity, not by string) |
| Relates to | ADR-0005 (resume is bound to its directory), ADR-0023 (project trust is recorded per directory), ADR-0099 (`sandbox.CanonicalDir`, the kernel's name for a directory) |

## Context

gem-agent keys what it keeps per project by the project's path as a
string: the transcript header's `project` (resume and `gem-agent
sessions` filter on it), the state directory's `.project` marker, and
the `[projects."<path>"]` tables of `policy.toml` — trust, content pins,
per-project tool and command allowlists.

The string comes from `os.Getwd`, which returns `$PWD` whenever `$PWD`
names the current directory. A path with Japanese voiced kana has two
Unicode spellings: precomposed (NFC, what an input method types) and
decomposed (NFD, what some providers create on disk). macOS treats them
as one name. Measured on APFS in the sibling runtime with a throwaway
directory created and entered in each spelling: `stat` finds either,
`os.Getwd` returns whatever `$PWD` says, `filepath.EvalSymlinks` keeps
the given spelling, the kernel's `getcwd` returns the spelling on disk,
and a Seatbelt `subpath` rule matches across spellings.

So one directory became two project strings depending on how the shell
arrived. In the sibling the record carried NFD (121 bytes) and a later
`cd` typed in NFC produced 115 bytes: `--resume` refused ("belongs to
<path>, not <path>"), the state marker reported a path-escape
collision so the session ran without its work directory, and trust,
pins and allowlists recorded under one spelling were absent under the
other. gem-agent has the same four readers and the same comparisons.

The sandbox profiles are not affected: Seatbelt matches across
spellings. ADR-0099 met the same folding from the other side and
settled on the kernel's name (`CanonicalDir`) for containment.

## Decision

1. **The project path is the kernel's name for the working directory.**
   `gem-agent`, `sessions`, `trust` and `workdirs` resolve the project
   in one function: the working directory from the kernel
   (`syscall.Getwd`), then `sandbox.CanonicalDir` — the name read back
   from an open descriptor, the volume's case and form. An architecture
   test refuses `os.Getwd` anywhere in the module, so no second reader
   brings `$PWD` back.

2. **A recorded project path matches the current one by identity.** The
   transcript's `project` (resume, the listing), the state directory's
   marker and the `policy.toml` project tables match when the strings
   are equal or, failing that, when both exist and are the same
   directory (`os.SameFile`). One function does this. Records written
   before this change, in either spelling, are found; nothing on disk is
   rewritten.

3. **Writes go to the entry that already exists.** A `policy.toml`
   change for the current project — clearing included — lands in the
   table that already names this directory, so one directory never
   grows two tables.

## Consequences

- Resume, the state directory, trust and allowlists agree for a
  directory however its path was typed or completed.
- The project string now also carries the volume's case (from the
  descriptor), as ADR-0099's containment already does; a record written
  under another case is still found by identity.
- A recorded path whose directory no longer exists matches only by
  string, as before.
- Two projects whose paths escape to the same state-directory name are
  different files, so the marker still separates them.
- A `stat` of each recorded `policy.toml` project is paid only when the
  exact string is absent.

## Alternatives considered

- **Normalise every path to NFC or NFD as a string.** Rejected: it needs
  a normalisation table as a direct dependency, still disagrees with a
  case-folding volume, and either form disagrees with the on-disk
  spelling for half the directories — the opposite of what ADR-0099
  settled on.
- **Rewrite existing records to the kernel's spelling on load.**
  Rejected: it rewrites the operator's files to fix a reading problem.
- **The kernel's spelling only, without the identity match.** Rejected:
  records already written under a `$PWD` spelling would stop matching
  after the upgrade — trust asked again, allowlists gone, sessions
  unresumable.

## References

- `cmd/project.go`, `internal/pathid`, `internal/statedir`,
  `internal/session`, `internal/config/policyfile.go`
- lagent ADR-0035 (the sibling's record of the same fix and its
  measurement)

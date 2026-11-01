# ADR-0101: A project is its directory, not its spelling

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-01) — implemented; §2 revised before release (pre-release review and the operator: compare normalised strings, not file identity) |
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

2. **Paths are compared by one function, in the volume's sense of a
   name.** Every comparison of two paths — a recorded project with the
   current one (the transcript's `project` for resume and the listing,
   the state directory's marker, the `policy.toml` project tables,
   `[approval].trusted_projects`), and every containment check (inside
   the project, the work directory, an added directory, a skill, a
   protected directory) with the path relative to it — goes through
   `internal/pathid`: both sides cleaned and Unicode-normalised to NFC,
   then compared. It is a pure function of the strings. Six packages had
   their own copy of the raw prefix check; they now call `pathid.Within`,
   and an architecture test refuses a raw `HasPrefix(p, base+separator)`
   anywhere else. ADR-0099's link check (`ResolveWriteDir(typed) !=
   typed`) stays an exact comparison on purpose: it asks whether
   resolving changed the string, not whether two paths are the same.

3. **Case is not folded.** On a case-sensitive volume two names that
   differ in case are two directories; folding them would hand one's
   trust and allowlists to the other. Unicode form is different: APFS
   and HFS+ never hold two names that differ only in it.

4. **Writes go to the entry that already exists.** A `policy.toml`
   change for the current project — clearing included — lands in the
   table that already names this directory, so one directory never grows
   two tables.

## Consequences

- Resume, the state directory, trust (recorded or configured),
  allowlists and the containment checks agree for a directory however
  its path was typed or completed — including an absolute path the
  model or the operator types in the other form.
- The comparison touches no disk: it cannot stall on a volume that no
  longer answers, and cannot follow a recorded path that has since
  become a symlink to another directory.
- The project string now also carries the volume's case (from the
  descriptor), as ADR-0099's containment already does.
- `golang.org/x/text` moves from an indirect to a direct dependency (the
  Go project's own module, already in the build).
- Not covered, and accepted:
  - **Two tables written before this change**, one per spelling: the
    one under the exact current spelling is used, else the first in
    sorted order; the other is not merged. What it held is asked for
    again — the failure is towards asking, not towards permitting.
  - **Case-only differences** between a record and the kernel's
    spelling do not match; the project asks again.
  - **A record made through a firmlink spelling**
    (`/System/Volumes/Data/...`) escapes to a different state-directory
    name than the kernel's `/Users/...`; its memory and work directory
    start empty. Resume by id still works.

## Alternatives considered

- **Match by file identity (`os.SameFile`).** Implemented first and
  replaced before release: it needs both paths to exist, cannot answer
  containment, stats every recorded project on the hot path (a stale
  network volume stalls each write), and follows a recorded path that
  has become a symlink, so the link's target inherited the record's
  trust (pre-release review).
- **Fold case as well.** Rejected (§3).
- **Rewrite existing records to the kernel's spelling on load.**
  Rejected: it rewrites the operator's files to fix a reading problem.
- **The kernel's spelling only, without the normalised comparison.**
  Rejected: records already written under a `$PWD` spelling would stop
  matching after the upgrade, and typed absolute paths would fall
  outside the project.

## References

- `cmd/project.go`, `internal/pathid`, `internal/statedir`,
  `internal/session`, `internal/config/policyfile.go`,
  `internal/config/config.go`
- lagent ADR-0035 (the sibling's record of the same fix and its
  measurement)

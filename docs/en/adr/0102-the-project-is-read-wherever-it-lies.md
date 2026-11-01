# ADR-0102: the project is read wherever it lies

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-02) — implemented |
| Date | 2026-11-02 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a project in Google Drive (`~/Library/CloudStorage/…`) could not use the read lane — measured in the sibling runtime (lagent ADR-0036), where every read-lane `shell_exec` failed with `getcwd: Operation not permitted`; gem-agent builds the read lane the same way |
| Amends | ADR-0099 §1 (the session's own roots are read under a reach denial, as added roots are) |
| Relates to | ADR-0073 (the lanes), ADR-0085 / ADR-0086 (credential reads), ADR-0101 (`pathid`) |

## Context

The read lane denies reading three trees for **reach**, not for secrecy
of a particular file: `/Volumes` and `/Network` (external and network
mounts — a walk from `/` would reach every share) and `~/Library` (mail,
messages, cookies, browser data, token stores), re-allowing four
toolchain directories under it. The credential block, the last read
rule, then denies credential material everywhere.

macOS keeps cloud storage — Google Drive, Dropbox, OneDrive, iCloud
through File Provider — under `~/Library/CloudStorage/`. A project there
lies inside a tree the read lane denies, and so does a project on an
external disk or a network share under `/Volumes`.

Measured on this machine (2026-11-02) with the sibling runtime's read-lane
profile — the reach rules here are the same lines — and confirmed here by
`TestReadLaneRunsInAProjectUnderReach` against this runtime's profile,
running `pwd`, `ls` and a file read in the project:

| Project | Today | Project tree re-allowed |
|---|---|---|
| an ordinary directory (control) | works | works |
| a directory under `~/Library` | `getcwd: Operation not permitted`; `ls .` denied | works |
| Google Drive, `~/Library/CloudStorage/…` | the same | works |
| an SMB share, `/Volumes/…` | the same | works |
| a Dropbox file not yet downloaded (online-only) | — | read; the provider downloads it |

- The read lane is not merely narrower there; it is unusable: the
  command cannot learn its own working directory. Every read the model
  routes to it fails, and with the read lane auto-approved the operator
  only sees the failures.
- Re-allowing the project's own tree is enough. Read access to its
  ancestors' metadata was measured and is not needed.
- Reading an online-only file needs nothing the read lane denies: the
  kernel asks the provider, not the reading process.

This runtime met the same denial for directories the operator adds as
roots, and settled it in ADR-0099 §1: an added root is read even
where it lies under a tree denied for reach — the operator named it.
The project is the first root of all, and was left out.

## Decision

1. **The session's own roots are read wherever they lie.** When the
   project directory or the session work directory lies inside a tree
   the read lane denies for reach, the read lane re-allows reading that
   root's own tree, after the reach denials. Only the root itself is
   re-allowed: the rest of `~/Library/CloudStorage`, the rest of the
   share and the rest of `~/Library` stay denied.

2. **Only a root strictly inside a denied tree.** A root that is a
   denied tree (`~/Library` itself) or contains one (a project at the
   home directory, which the broad-root confirmation allows) is not
   re-allowed: Seatbelt is last-match-wins, and re-allowing such a root
   would re-open the whole of `~/Library` to an auto-approved lane. There
   the read lane stays as it is. The containment is judged with
   `pathid.Within` (ADR-0101).

3. **Credentials stay denied inside the root.** The re-allow comes before
   the credential block, which stays the profile's last read rule, so a
   `.env` or a key inside the project is no more readable than before.

## Consequences

- A project in Google Drive, Dropbox, OneDrive or iCloud, on an external
  disk or on a network share has a working read lane.
- An online-only file the model reads in the read lane is downloaded by
  its provider, as any read of it would.
- The read lane's reach is unchanged outside the session's roots.
- A read lane over a network share reads at the share's speed; a stale
  share stalls a command as it would stall the operator's own shell.

## Alternatives considered

- **Drop `~/Library/CloudStorage` from the reach denial.** Rejected: it
  would open every account's every synced folder to the auto-approved
  lane, when only the project is the session's.
- **Send commands to the write lane for such projects.** Rejected: the
  write lane asks for approval of every command, which is what the read
  lane exists to avoid; the read lane's reach denial is about what lies
  around the project, not the project.
- **Re-allow ancestors' metadata as well.** Not needed (measured).

## References

- `internal/sandbox/lane.go` (`LaneProfile`, `ownRootsUnderReach`)
- ADR-0099 §1; lagent ADR-0036 (the same decision, with the measurement)

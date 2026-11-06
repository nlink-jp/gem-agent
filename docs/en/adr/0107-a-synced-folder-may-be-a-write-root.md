# ADR-0107: a synced folder may be a write root

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-06) — implemented; independent design review applied (§4, §5, the residual in Consequences); §5 measured on Dropbox, Google Drive and iCloud Drive |
| Date | 2026-11-06 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a OneDrive folder added to a session as a directory to write could not be written. On macOS a File Provider client's folder and iCloud Drive lie under `~/Library`, and §5 of ADR-0099 refuses a write root there |
| Amends | ADR-0099 §5 (the `~/Library` refusal for write roots) |
| Relates to | ADR-0099 §1, §7, §10 (what a write root is, the probes, the persistent files), ADR-0101 (`pathid`), ADR-0102 (the project is read wherever it lies) |

## Context

ADR-0099 §5 refuses a write root that lies inside `~/Library`, because
`LaunchAgents`, `Application Support` and the like hold files that other
programs run outside the sandbox. The rule is written as one tree.

macOS keeps the operator's synced documents in that tree:

- Google Drive, Dropbox, OneDrive and every other provider built on File
  Provider place their folders in `~/Library/CloudStorage/<provider>-<account>/`.
  A link the provider or the operator makes (`~/OneDrive`,
  `~/Google Drive`) resolves there, and §5 judges the real path.
- iCloud Drive is `~/Library/Mobile Documents/com~apple~CloudDocs/`.

So none of these synced folders can be a write root, whichever way it is
named. (A sync client that keeps its folder directly in the home, outside
`~/Library`, was never refused.)
ADR-0102 made a project in these places readable by the read lane; it did
not touch §5, and nothing else had looked at it since. The result is
inconsistent: a folder in Google Drive opened as the project is written
by the file tools and the write lane, and the same folder added as a
write root is refused.

Measured on this machine (2026-11-06):

| What | Result |
|---|---|
| A project in Google Drive (`~/Library/CloudStorage/GoogleDrive-…/共有ドライブ/…`), session of 2026-11-05 | `write_file` 16 and `edit_file` 22 calls auto-approved as edits inside the project; write-lane `shell_exec` ran `mkdir -p` and `mv` in the project; no `Operation not permitted` in the transcript |
| `RefuseRoot` on a write root under `~/Library/Mobile Documents/x` | refused — `TestRefuseRoot` asserts this today |
| `~/Library/CloudStorage/` | one folder per provider account (`Dropbox`, `GoogleDrive-<account>`) and a `.DS_Store` |
| `~/Library/Mobile Documents/` | 168 entries: `com~apple~CloudDocs` (iCloud Drive) and the iCloud containers of apps, among them `com~apple~Automator`, `com~apple~ScriptEditor2`, `iCloud~com~apple~shortcuts~runtime`, `iCloud~is~workflow~my~workflows` and `iCloud~md~obsidian` |

The first row shows that the kernel and the provider let a sandboxed
process write a File Provider folder: the write lane's allow is the
same `(subpath …)` rule for the project and for a write root. The last row
shows that §5's reason holds for most of `Mobile Documents`: an app's
container holds what that app runs — Automator workflows, scripts,
shortcuts, an Obsidian vault's plugins.

## Decision

### 1. Two synced locations are carved out of the `~/Library` refusal

A write root may lie inside `~/Library` only when its real path is one of:

- **strictly inside `~/Library/CloudStorage/`** — a provider's account
  folder or anything below it;
- **`~/Library/Mobile Documents/com~apple~CloudDocs/`** or anything below
  it — iCloud Drive itself.

Everything else under `~/Library` stays refused for writing, as §5 says,
and keeps its reason in the refusal. In particular:

- `~/Library/CloudStorage/` itself — it covers every account of every
  provider, and any provider the operator adds later;
- `~/Library/Mobile Documents/` itself and every app container in it.

Read roots are not affected: §5 never refused them under `~/Library`.

### 2. Judged on the kernel's name, by path segments

The carve-out is judged where §5 is judged, on the name
`CanonicalDir` returns, so a typed case variant (`~/library/cloudstorage/…`)
or a link (`~/OneDrive`) is judged as the folder it names. Containment is
`pathid.Within` against the two anchors built from the resolved home
(ADR-0101), so `~/Library/CloudStorageX/…` and
`~/Library/Mobile Documents/com~apple~CloudDocsX/…` are not inside them.

### 3. Nothing else about a write root changes

A synced write root is a write root as ADR-0099 defines it:

- the write and operator lanes write it, and the read lane reads it,
  even though the read lane denies `~/Library` for reach (§1);
- the credential block still applies inside it (§7);
- its persistent names (`AGENTS.md`, `.git/hooks`, …) are denied to the
  write lane and watched across sessions (§10);
- the per-root probes run in it at startup and on a grant (§7);
- the model may request it with `request_directory`, and the operator
  answers as for any other directory (§4).

### 4. A probe that cannot remove itself says so

The per-root probe removes its probe directory and ignores a failure
(`verifyAddedRoot`). In a synced folder a leftover is not local: in a
shared drive where the operator's role may create but not delete, a
`.gem-agent-root-probe-*` directory holding `.env` and `plain` would stay
and be synced to everyone who shares the drive, and nothing would say so.
The probe reports any probe file or directory it could not remove, by
path, in the startup or grant notes. The project's probes
(`VerifyWriteLane`, and `VerifyReadLane` — found by the pre-release
review) remove their files the same way and get the same report, and
every way a grant ends without committing says them too.

### 5. Measured before acceptance

The premise had been measured only for a project in Google Drive. On
2026-11-06 an empty folder made for the purpose (`gem-agent-probe-20261106`,
chosen by the operator) at the top of Dropbox, of Google Drive's My Drive
and of iCloud Drive was given to `LaneProfile` as a write root, with the
real home and a project elsewhere, and real `sandbox-exec` ran each case
with the root as the working directory. `RefuseRoot` was not in the path
— it refuses all three today, as the run also recorded — so this
measures what the lanes and the providers do once §1 lets the root in.

| Case (the same in all three) | Dropbox | Google Drive | iCloud Drive |
|---|---|---|---|
| `RefuseRoot` for write, today | refused (`~/Library`) | refused | refused |
| write lane: `pwd`, `ls`; create, append, rename a file; make a directory, write in it, rename it; remove both | allowed | allowed | allowed |
| write lane: `AGENTS.md`; `.git/hooks/pre-commit` | denied | denied | denied |
| write and read lanes: read `.env` | denied | denied | denied |
| read lane: `pwd` and read a plain file under `~/Library` | allowed | allowed | allowed |
| read lane: write | denied | denied | denied |
| operator lane: write and remove | allowed | allowed | allowed |
| `VerifyAddedRoots` (the per-root probe) | passed, no notes | passed | passed |
| left in the folder after cleanup | nothing | nothing | nothing |

The provider did not get in the way of any sandboxed write, and every
denial ADR-0099 places inside a write root held. OneDrive is not on this
machine; it is the same File Provider path and was not measured
separately. The folders were removed afterwards. Each provider may keep
the deleted items in its own trash or history, which was not checked.

### 6. Tests and documents

- `TestRefuseRoot` gains rows for: a folder inside a provider's account
  folder and the account folder itself (accepted for writing);
  `~/Library/CloudStorage` itself (refused); a folder inside iCloud Drive
  and iCloud Drive itself (accepted); `Mobile Documents` itself and an app
  container (refused); the two prefix neighbours (refused); a case variant
  and a link into a synced folder (judged on the real path); and
  `LaunchAgents` (still refused). These rows are the whole of the
  carve-out: the lane profiles carry no `~/Library` rule for a write root,
  so a sandbox test under a fake `Library/CloudStorage` would pass with or
  without this ADR and is not added.
- The leftover report (§4) has a test where the probe directory cannot be
  removed.
- ADR-0099 gets an "Amended by ADR-0107" note under its header;
  `config.example.toml`, `configuration.md` and `approval.md` (both
  languages) name the exception where they name the `~/Library` refusal;
  AGENTS.md and CHANGELOG.md are updated.

## Consequences

- A folder in Google Drive, Dropbox, OneDrive, iCloud Drive or any other
  File Provider folder can be added with `--add-dir`, `[directories].write`,
  `/add-dir` or a granted request.
- What the model writes there is synced by the provider: it reaches the
  operator's other devices and, in a shared drive or a shared folder, the
  other people who share it. That is the operator's choice in naming the
  folder, as it already is for a project in cloud storage.
- **Accepted residual: a synced folder can hold what runs outside the
  sandbox.** §5's reason is not unique to `~/Library`. An Obsidian vault in
  Google Drive carries its plugins, and a `~/bin` linked into Dropbox (a layout
  the operator has used) puts executables on `PATH` from a
  synced folder. Adding the account folder that holds them hands them to
  the write lane, and the path the operator approves is the provider's,
  not the link they know. This is accepted: the operator names the folder,
  as with any write root outside `~/Library` (ADR-0099 accepted `~/bin` on
  the same ground), and the narrower the folder added, the less it holds.
  The refusal guards the places macOS and apps run from, not every place
  something could be run from.
- The startup probe creates a probe directory in each write root and
  removes it (ADR-0099 §7), as the project probe already does in a project
  in cloud storage. Whether a provider uploads it in between is the
  provider's behaviour and was not measured; nothing in it is the
  operator's data, and a leftover is reported (§4).
- **Accepted residual: every grant probes every root.** A grant during
  the session rebuilds the profiles and re-runs the probes in the
  project and in every added root, not only the changed one as
  ADR-0099 §12 step 4 says (a gap older than this ADR). Where the
  operator's role cannot delete, each grant leaves one more probe
  directory there; each is reported, and a session started without that
  root, or a role that can delete, stops it.
- **Accepted residual: the provider's own files at the account folder.**
  Adding the account folder itself (`~/Library/CloudStorage/Dropbox`)
  also hands the write lane what the provider keeps at its top, such as
  Dropbox's `.dropbox`. Adding the subfolder that is worked in avoids
  it.
- **Implementation review (2026-11-06).** The first cut carried the
  leftovers in the probes' notes: a refused request put them in the
  error the model reads, a refused root lost the project probe's, and a
  grant repeated startup's notes. Leftovers now travel apart from the
  notes (`buildLanes`) and the grant path routes them to the operator.
- A folder the provider has made read-only (the Google Drive account
  folder itself is `r-x`) is refused by the probe, not by this rule, with
  the probe's "cannot create a probe" message.
- An app's iCloud container (an Obsidian vault in iCloud, for instance)
  still cannot be a write root. Opening it as the project still works. A
  need to write one as an added root is a new decision, not a wider
  carve-out here.

## Alternatives considered

- **Drop the `~/Library` refusal for write roots.** Rejected: §5's reason
  is unchanged for `LaunchAgents`, `Application Support` and the app
  containers in `Mobile Documents`.
- **Carve out all of `~/Library/Mobile Documents`.** Rejected: measured,
  most of its entries are app containers, and several hold what an app
  runs — Automator workflows, scripts, shortcuts, vault plugins.
- **Allow `~/Library/CloudStorage` itself.** Rejected: no single synced
  folder needs it, and it would also grant every account and every
  provider added later. ADR-0102 accepted such a project for reading;
  a write grant is not the same weight.
- **Ask macOS which directories are File Provider domains.** Rejected:
  it needs the File Provider framework (cgo) to answer what the
  directory macOS places them in already answers. A provider that places
  a domain elsewhere under `~/Library` stays refused, which fails safe.
- **An operator setting that lifts the refusal.** Rejected: one switch
  would reopen `LaunchAgents` along with the folder the operator wanted.

## References

- `internal/sandbox/roots.go` (`RefuseRoot`), `internal/sandbox/roots_test.go`
- ADR-0099 §5; ADR-0102 (the read-lane side of the same locations)

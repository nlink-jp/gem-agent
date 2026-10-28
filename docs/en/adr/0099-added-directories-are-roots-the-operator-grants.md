# ADR-0099: Added directories are roots the operator grants

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-10-29) — step 1 (§1–§3, §5–§7, §10) implemented; step 2 (§4, §8) not yet |
| Date | 2026-10-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Issue #2: a directory tree the project does not cover should be handled like the project and come under lane control |
| Amends | ADR-0001 (the write roots), ADR-0058 (the work directory as the second root) |
| Relates to | ADR-0023 / ADR-0074 (trust of project content), ADR-0068 (the environment reaches the whole process), ADR-0070 §2 (one definition of where the shell may write), ADR-0072 §1.4 (persistent files are OperatorOnly), ADR-0073 (lanes, verified enforcement), ADR-0085 / ADR-0086 (the operator answers a credential read; the kernel reads the file) |

## Context

Today a session has two roots: the project and the session work directory
(ADR-0058). With the sandbox on, a directory outside both is out of reach
for every write path:

| Path | Inside a root | Outside every root (e.g. `~/vault`) |
|---|---|---|
| File tools (`read_file`, `write_file`, …) | allowed | **Block** ("absolute path outside the project …") |
| `shell_exec`, read lane | reads | reads, except `/Volumes`, `/Network` and `~/Library` (apart from its toolchain caches) |
| `shell_exec`, write lane | writes, except the persistent files | **denied** |
| `shell_exec`, operator lane | writes, persistent files included | **denied** — the operator lane is the ADR-0001 profile: it writes the project, the work directory and the scratch roots, nothing else |

So under the sandbox there is no way to write a second tree at all. The
only route is `--no-sandbox`, which gives up every protection for the
whole session. An operator who keeps notes, a vault or a sibling
checkout next to the project has nothing in between.

Three things make an added root cheap to build:

- **The profiles can already be rebuilt during a session.**
  `rotateWorkDir` calls `buildExecFn` again on `/clear`, and `liveExec`
  swaps the shell runner under a lock.
- **Seatbelt can re-allow a subpath under a denied tree.** Measured on
  2026-10-29 with `sandbox-exec`: with a profile that denies `file-read*`
  on `(subpath "/Volumes")`, `ls /Volumes/<vol>` fails (exit 1). With
  `(allow file-read* (subpath "/Volumes/<vol>"))` after that deny, the same
  command succeeds (exit 0), and a sibling volume stays denied (exit 1).
  Because the last matching rule wins, an added root on an external volume
  can be readable without opening the rest of `/Volumes`.
- **The credential block is now the profile's last read rule.** The
  review of this ADR found that the read lane's `~/Library` toolchain
  re-allow followed the credential denials, so a `.env` inside
  `~/Library/Caches` was readable unasked. That was fixed first and
  separately (commit `47146b1`, `TestCredentialBlockIsTheLastReadRule`).
  The re-allows this ADR adds are written before that block, so they
  cannot re-open a credential name either.

But the root set is not one list. "The project and the work directory"
is written into several consumers, each of which must learn about added
roots (§6). Missing one does not open a hole. It makes the feature fail in
that consumer, or pushes an implementer to route around it, and routing
around the file-read child would lose the kernel's credential guard
(ADR-0086).

The operator also asked that the model be able to *ask* for a directory,
with the runtime always putting the request to the operator. This is the
same pattern as the credential read of ADR-0085/0086: the model proposes,
the runtime gates, the human answers.

## Decision

### 1. An added directory is a root with an access level

A session has the project, the work directory, and zero or more **added
directories**. Each one has an access level of `read` or `write`:

- **`read`** — the file tools may read the directory (it stops being
  "outside the project"). The read lane may read it even where it lies
  under a denied tree (`/Volumes`, `/Network`, `~/Library`). **No lane
  writes it, the operator lane included.** A write to a read root is
  judged exactly as a write outside every root.
- **`write`** — `read`, plus the file tools may write it, and the write
  and operator lanes may write it, as they write the project.

Both levels keep every floor the project has:

- The credential filters stay in force in every root, in the file tools'
  verdict and in the profile.
- The persistent-file names (`sandbox.PersistentFiles`, anchored at each
  write root) are denied to the write lane and are `OperatorOnly` for the
  file tools.
- `.git/` writes stay Block.

The read lane never writes an added directory, the same as the project.

### 2. Nothing in an added directory is loaded

An added directory's `AGENTS.md`, `CLAUDE.md`, `GEMINI.md`, `.mcp.json`,
`.gem-agent.toml` and skills are **not read** as instructions, servers,
policy or skills (operator decision, 2026-10-29). They are protected (§1),
because the tree may be another project that a later session trusts, but
they are not trusted here. So there is no trust prompt, no pin set and no
loader change for an added directory: ADR-0023/0074 apply to the project
alone, as before.

This matters most for §4. A directory added on the model's request would
otherwise be a way for the model to bring in instructions of its own
choosing.

### 3. The operator adds directories at startup

- `--add-dir PATH` (write) and `--read-dir PATH` (read), each repeatable.
- The global configuration file takes the same lists:
  ```toml
  [directories]
  write = ["~/notes"]
  read  = ["/Volumes/Archive/reference"]
  ```
- The project's `.gem-agent.toml` cannot add directories, and nothing
  needs to be built to keep it that way. `LoadProject` already refuses a
  key outside `[approval.tools]` and `[mcp].exclude` with an error
  (`internal/config`), so a `[directories]` table there fails the load
  like any other unknown key. A repository cannot grant itself writes
  outside itself.

A path given at startup is resolved to its real path, judged by §5, and
then opened as an `os.Root`; that real path is what the profile names.
The descriptor read-back of §4's step 1 is not repeated here: at startup
no model-driven command is running yet, so nothing inside gem-agent can
swap the path between the judgement and the open, and an outside
process that can rewrite the operator's directories already has more
than this would give it. §4 needs it because a write-lane command may be
running while the operator answers. A path that fails §5 is a startup
error, not a note: the operator typed it, so the operator must hear that
it did not apply.
`-p` takes startup directories like any other flag.

### 4. The model may request a directory; the operator always answers

A built-in tool:

```
request_directory(path, access, reason)
```

- **Always `OperatorOnly`.** It must prompt in every approval mode. The
  session allowlist and the model tier never answer it. It is denied under
  `-p`, where no human is present. When the session has a read-only
  ceiling, a `write` request is refused before anyone is asked. A
  delegated child registry (`Registry.Subset`) does not carry the tool; a
  child sees the roots in force through its parent.
- **§5 runs before the operator is asked.** A path that fails it is refused
  by rule. Approval fatigue never decides whether `~` becomes writable.
- **The prompt shows,** with control and bidirectional-override characters
  removed from everything the model wrote:
  - the resolved real path first, and the path as the model wrote it when
    the two differ;
  - the access level, and what it means in the current mode (for `write`
    under auto-approval: "later writes there run without asking");
  - the model's `reason`, labelled as the model's claim.
- **The operator's answers** are: approve as requested; approve as `read`
  (offered when `write` was requested); deny. A `write` request for a
  directory that is already a `read` root is an upgrade and is asked the
  same way.
- **The order after approval is fixed, and every step must pass before the
  grant takes effect:**
  1. Open the directory as an `os.Root` and read its real path back from
     the open descriptor (`F_GETPATH`). Refuse the request if that path
     differs from the one shown. Opening first means a symlink swapped in
     between the answer and the open cannot redirect the root.
  2. Build all three lane profiles with the new root.
  3. Verify them (§7). A request rebuild has its own entry point that
     returns the failure as an error. It does not reuse `buildExecFn`'s
     startup fallback.
  4. Swap the shell runner, then add the root to the file tools and the
     file-read child, then rebuild the system prompt's description of the
     roots. A grant only adds reach, so this order is enough; no lock
     spans the steps.
- **A failed verification refuses the request and leaves the session
  exactly as it was.** At startup (and on `/clear` today), an unverifiable
  write lane makes the session unconfined (ADR-0073 §7). Here, that
  fallback would let a model request lower the sandbox for the whole
  session. So a request may only add a root to a verified cage, never
  trade the cage away.
- **The tool's result** names the granted path and level, or the refusal
  and its reason.
- **Session-only.** Nothing is written to a file. `--resume` does not
  restore a requested directory, and `/clear` drops it: it was granted for
  that conversation's reason. A directory given at startup stays for the
  life of the process, and `/clear`'s rebuild carries it.
- Every grant and refusal is written to the session log and the audit trail
  with its source (`flag`, `config` or `request`), real path and level.

### 5. One rule decides which directories may be roots

`sandbox.RefuseRoot(path, access)` is read by both §3 and §4. It judges the
real path, and a directory is refused when it:

- does not exist, or is not a directory once resolved;
- is `/`, the operator's home, or an ancestor of either;
- contains the project, the work directory, the configuration home
  (`~/.config/gem-agent`) or the state directory, or lies inside the
  configuration home or the state directory;
- is, lies inside, or contains one of the home-anchored credential
  locations (`credentialDirs` and `credentialFiles` under home). The
  credential *names* that match anywhere (`.env`, `credentials.json`, …)
  are not part of this test. A tree that holds a `.env` can be a root; the
  file stays denied inside it by §1;
- lies inside a shared scratch root (`sandbox.ScratchDirs()`: `TMPDIR`,
  `/private/tmp`). There, any write-lane command can rename the root
  itself away and put another directory in its place. The profile names a
  path, and the file tools hold a descriptor, so the two would part. This
  is the rule the project already follows (`LaneProfile` never lets a
  scratch root cover the project's parent);
- contains a shared scratch root, `/private/var/tmp` or `/dev`. The root
  would take the lanes' own allowances with it: the write and operator
  lanes write scratch under a read root the file tools refuse, and a
  read re-allow over `/dev` reopens the terminal devices every lane
  denies (implementation review, 2026-10-29);
- for `write` only: contains the configuration file in effect — `--config`
  may name one outside `~/.config/gem-agent` — since the model could
  otherwise rewrite the operator's hooks, approvals and roots for every
  later session (implementation review);
- for `write` only: lies inside `~/Library`. The read lane already denies
  that tree, and `LaunchAgents`, `Application Support` and the like hold
  files that other programs run outside the sandbox;
- already lies inside a root of the same or a higher level. At startup this
  is an error, because the two settings contradict each other. On request
  it is a no-op note that says which root covers it.

There is no list of system directories. SIP guards those that matter, and
writing anywhere else outside the home is a choice the operator can make.

### 6. Every consumer of the root set learns the added roots

These places hold "the project and the work directory" today, and each
takes the added roots with their levels:

- **The file tools' registry** — `rootState`, `rootFor` and `roots()` in
  `internal/tools`. Each root has its own `os.Root`, and the write tools
  consult the level.
- **The file-read child** — `fileChildRequest` (`cmd/filechild.go`) carries
  only `ProjectDir` and `WorkDir`. It carries the root list with levels,
  so the kernel-confined read (ADR-0086) covers added roots. The reads
  must never fall back to in-process for an added root.
- **The verdict** — `risk.Classify` receives the roots with their levels,
  not two strings. A write to a write root is judged as a write in the
  project. A write to a read root, or outside every root, is Block.
  `persistentTarget` and `.git` are judged relative to whichever write
  root holds the path.
- **The auto-approve reviewer** — its payload names the roots, so a
  legitimate write to a write root is not judged "outside the project".
- **`@` mentions** — `mention.resolve` accepts a path under any root.
- **The rebuild path** — `buildExecFn`, `rotateWorkDir` and the profile
  `Spec` carry the startup roots, so `/clear`'s rebuild keeps them.
- **The wording** — the write-lane denial note, the shell tool's
  description, the outside-roots reasons, and the system prompt's
  paragraph on the roots name the added directories. None claims "the
  project and work directory" when there are more.

### 7. The profile's order and the per-root probes are part of the rule

Seatbelt is last-match-wins, so the order is specified, not left to the
builder:

1. The read lane's re-allow of an added root comes after
   `readLaneReadDenies` and the `~/Library` denial, and **before**
   `writeCredentialBlock`, which stays the last read rule.
2. The read lane's write denial of each added root sits beside the
   project's, after the shared `/private/var/tmp` allow.
3. The write and operator lanes list write roots beside the project in
   their write allows; read roots appear in no write allow.

Startup verification (`VerifyReadLane` / `VerifyWriteLane`) probes the
project and home. It proves the cage, not a new root. So each added root,
at startup and on every grant, gets its own probes under the real
profiles:

- the read lane cannot create a file in it;
- a credential name created in it is unreadable by the read lane;
- for a `write` root, the write lane can create a file in it, and a
  persistent name in it is denied;
- for a `read` root, the write lane cannot create a file in it.

A probe file is created under a random name and removed, as the existing
probes do. At startup a root that fails its probes stops the start, for
§3's reason. A read root this process cannot write at all (a read-only
volume) cannot hold a probe file; nothing can write it either, so its
probes are skipped with a note, and the order test covers its credential
block. The same cases run in the package tests against real
`sandbox-exec` (`TestAddedRootEnforcement`).

### 8. Listing

`/dirs` lists the roots with their level and source. Removing a root
during a session is not in this ADR. It would use the same rebuild as §4,
so it can be added later without new machinery.

### 9. It ships in two steps

1. §1–§3, §5–§7 and §10: the roots and every consumer, startup flags and
   configuration, the refusal rule, the order and the probes, and the
   watch on write roots' persistent files.
2. §4 and §8: the request tool and `/dirs`, built on step 1's rebuild,
   verify and swap.

Step 2 depends on step 1. Nothing in step 1 is redone by step 2.

### 10. A write root's persistent files are watched and reported

*Added 2026-10-29, after the implementation review found that the
residual below was wider than first written.*

`instructions.Load` gates only the project's own files by the trust
grant. The instruction files of the project's **ancestors**, up to the
home, load without asking, on the premise that only the operator writes
above a project. A write root breaks that premise. A write-lane command
can build `d/AGENTS.md` in scratch and move `d` into the root, and a later
session started under `<root>/d/...` loads it as an ancestor file, with
no prompt. A moved-in checkout can carry a `.git/hooks` script that runs
outside the sandbox on the operator's next `git commit` there. The name
denials do not stop either, because the rename names the directory, not
the file.

So a write root gets the same change detection the project has had
since ADR-0074 §3. It does not block anything. It makes the change
visible when it happens:

- **What is watched**: every `write` root. A `read` root is written by no
  lane, so it is not walked.
- **The walk**: `trustpin.Snapshot` — the project's own walk. It records
  every name `sandbox.PersistentFile` protects (instruction files,
  `.mcp.json`, `.gem-agent.toml`, `.claude/`, `.git/hooks`, …) at any
  depth, by digest, without following links, and stops at
  `trustpin.WalkEntries` entries. A cut walk is said once at startup, per
  root.
- **When**: at startup, the walk is compared with what the previous
  session of this project recorded for the same root, so a change made
  between sessions — by anything — is reported. At `/clear` and at exit,
  it is compared with the session's own starting point, so what this
  session added or changed is reported before the operator opens that
  tree anywhere else.
- **The report**: one note per root that changed, naming the root and the
  added, changed and removed files. The `/clear` and exit reports also
  write a `persistent_changes` transcript record carrying `root` (and
  `cut` when the walk stopped at the cap) beside the project's own; the
  startup report writes none, as the project's does not — the record is
  of what a session did. A walk cut short names nothing past the cut as
  removed: past the cut is unknown, not gone.
- **Who is told**: the sessions of the project that added the root. The
  state lives in that project's state directory, so a different project
  opened under the root — the one that would load the planted file — is
  not told. The advice in the residual below is the mitigation for that
  case.
- **State**: one file in the project's state directory,
  `added-persistent.json`, holding the last snapshot of each write root
  this project added. Nothing is written into the root.

The walk is per root and bounded, so its cost is the cap at worst: a
root larger than `WalkEntries` is watched for its first entries only,
and the note says so.

## Alternatives considered

- **Let the project's `.gem-agent.toml` add directories.** Rejected, §3: a
  repository would grant itself writes outside itself.
- **Read the added directory's instruction files and `.mcp.json`, behind a
  trust prompt.** Rejected by the operator, §2. It would also need pins
  per root, and with §4 it would let the model bring in instructions.
- **Let the model tier or the session allowlist answer `request_directory`.**
  Rejected. The model that wants the directory would be judging its own
  request: the evaluator-is-the-proposer objection of ADR-0020 §4.
- **Persist granted requests in a file.** Rejected. A one-time answer would
  become a standing grant that no later prompt shows.
- **Reuse `buildExecFn` for a request, as `/clear` does.** Rejected, §4:
  its fallback makes the session unconfined when verification fails, and a
  model request would then be able to lower the sandbox.
- **Re-run only the existing startup probes on a grant.** Rejected, §7:
  they prove the cage again, not the new root's place in it.
- **Let read roots be written by the operator lane.** Rejected, §1. That
  would make `read` a write grant for every operator-approved command, and
  "read" would no longer mean what it says.
- **Export the added roots in an environment variable.** Rejected, for the
  reason ADR-0068 gives: the environment reaches every child, and the
  transcript does not show it. The roots are stated in the system prompt
  and in the tool result.
- **Nonce-wrap the model's `reason` in the operator's prompt.** Rejected.
  Nonces protect a model from data, and the operator is not a model. What
  the human needs is the real path first and no characters that rewrite
  the line.
- **A list of forbidden system directories.** Rejected, §5. It is a list
  that drifts, and it guards what SIP already guards.

## Consequences

- An operator can keep a vault, notes or a sibling tree under the lanes'
  protection, writable without `--no-sandbox`. The file tools reach it
  like the project.
- An approval to `write` a broad directory (`~/Documents`) means, under
  auto-approval, later writes there are not asked about. That is the grant
  itself, not a hole. The prompt states it, and the "approve as read"
  answer exists for the cases where the operator wants less.
- **Residual: a write root can be given persistent files at any time, and
  they are reported, not refused.** Names are denied, but a write-lane
  command can build `d/AGENTS.md` in scratch and move `d` into the root
  (measured by the review: the move succeeded, a direct write was
  refused). A later session started under `<root>/d/...` loads it as an
  ancestor instruction file without asking, and a moved-in checkout's
  `.git/hooks` run on the operator's next git command there. §10 reports
  every such file at the next `/clear` or exit (or, after a session that
  was killed, at the next start), to the sessions of the project that
  added the root only; it does not stop the move. Accepted: blocking it would mean denying
  every rename into the root, which is most of what writing a tree is,
  or gating ancestor files by trust, which changes what every project
  loads. Do not add as a write root a directory above projects you open
  with gem-agent or Claude Code; read the note when it appears.
- **Residual: walks in an added root apply the project's ignore rules.**
  `list_tree`, `list_files` and `search_files` root `.gitignore` at the
  project, so for an added root a project rule can match its entries. The
  work directory has had the same behaviour since ADR-0058. Accepted for
  now; rooting the rules at the innermost root is a change of its own.
- **Residual: a write root may hold files other programs run outside the
  sandbox** — `~/bin`, `~/.local/bin`, a shell's configuration under
  `~/.config`. §5 refuses `~/Library` and the home itself, not these.
  Accepted: the operator approved writes there and saw the path, and a
  list of such directories would drift (§5).
- Each grant spawns the per-root probes, a few `sandbox-exec` runs once per
  grant.
- lagent shares the lane design. Porting this is a separate change, judged
  there.

## References

- Issue #2 (nlink-jp/gem-agent)
- Commit `47146b1` (the credential block is a lane profile's last read
  rule; found by this ADR's review)
- ADR-0001 (sandbox: writes denied, reads open), ADR-0058 (the work
  directory), ADR-0070 §2 (one list of writable places)
- ADR-0073 §3 / §5 / §7 (one list, many enforcers; verified enforcement)
- ADR-0074 §2 (persistent parents and pins)
- ADR-0085 / ADR-0086 (the operator answers what the model may not; the
  kernel reads the file)
- ADR-0020 §4 (the evaluator is the proposer)

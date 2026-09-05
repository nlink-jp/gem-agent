# ADR-0073: Capability lanes for shell_exec, and architecture tests that close the recurring classes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-05 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Nine review passes after v0.67.0 (ADR-0072 §4) produced 103 findings; the operator judged the fixes symptomatic — "局所ケースにフォーカスしすぎ" — and asked for the root causes to be found and fixed at the source, as the organisation's contribution rules require |
| Amends | ADR-0004 (the rule tier no longer decides Safe for shell commands), ADR-0001 (a read-lane shell command is non-mutating), ADR-0070 §2 (the shared list generalises to three lists) |

## Context — what nine passes actually found

The 103 findings recorded in ADR-0072 fall into seven classes. Three of
them account for 48 findings and 13 of the 16 re-finds, and each of the
three has a single root cause that no number of local patches closes:

| class | findings | re-found | root cause |
|---|---|---|---|
| A. the rule tier re-derives shell semantics from command text | 20 | 2 | *Safe* was derived from text — an unbounded domain (spellings, quoting, wrappers, flags, subcommands, scripts inside arguments) |
| B. check-then-use on a lexical path | 8 | 4 | the confinement API returned a *path* (a name) instead of a *handle*; every consumer re-opened the name |
| C. unbounded or silently truncated I/O | 20 | 7 | every site rolled its own read; a cap without a `more` signal is a new bug |
| D. rotation state copied into consumers | 12 | 1 | fixed structurally in v0.68.x (`rotateWorkDir`, a consumer list and a test that walks it) |
| E. permission decided in more than one place | 4 | 0 | three decision sites (`mustPrompt`, `decideAuto`, `gated`) each re-implemented the floors and each missed one |
| F. a format parser assumed structure the file does not promise | 5 | 2 | genuinely local |
| G. other (fourteen subclasses) | 34 | 0 | genuinely local |

Class A would never have converged. `internal/risk` was 1,100 lines, of
which roughly 700 (`classifyCommand`'s Safe derivation, `readOnlyCommands`,
`mutatingUse`, `sedExecutes` and its helpers, `persistentTokens`,
`candidateSplit`, `shellUnquote`, `gitReadOnlySegment`, `shellWords`,
`redirectTarget`, `walksOutsideRoots`, `aliasResolve`'s shell use) existed to
guess what bash and the programs it launches would do. Every pass found
another spelling. The reviewers were not wrong to report them; the design
asked them to.

The kernel already knows the answer. Seatbelt sees the real binary at
`exec`, the real path at `open`, the socket at `connect`, the Mach service
at `lookup`. Probed on this machine (macOS 26, `sandbox-exec`) before the
decision:

| side effect | SBPL rule | result |
|---|---|---|
| write outside the allowed directories | `(deny file-write*)` + allowed subpaths | denied (the ADR-0001 profile) |
| `echo > AGENTS.md`, `mv tmp AGENTS.md`, `gsed -i … AGENTS.md`, `rm AGENTS.md` | `(deny file-write* (regex …AGENTS\.md$))` after the allow | **all denied**, the file intact |
| `git config user.name` (writes `config.lock`, renames onto `config`) | deny on `.git/config` and `config.lock` | denied, the config intact |
| `echo x > .git/hooks/pre-commit` | `(deny file-write* (regex …\.git/(hooks\|info)…))` | denied |
| `cat ~/.ssh/id_rsa`, `cat ~/.s*/id_rsa`, `cat .env` | `(deny file-read* (subpath ~/.ssh) (regex /\.env…))` | denied — the glob does not help |
| `curl https://…` | `(deny network*)` | denied, DNS included |
| `defaults write` | `(deny user-preference-write)` | denied — **and `(deny file-write*)` alone does not stop it**: the preference is written by `cfprefsd`, so the ADR-0001 profile had this hole and only the regex tier covered it |
| `kill <other pid>`, `pkill` | `(deny signal)(allow signal (target self) (target children))` | denied; own children and `wait` still work |
| `sysctl -w` | `(deny sysctl-write)` | denied |
| `pbcopy`, keychain writes | `(deny mach-lookup (global-name …))` | denied |
| `osascript`, `open -a` | `(deny process-exec (literal /usr/bin/osascript) …)` | denied at exec. Apple Events are **not** stopped by `appleevent-send` or by any `mach-lookup` deny on this macOS; the binary list is the working rule, and the kernel matches the real binary, so `/usr/bin/osascript`, `\osascript` and `OSASCRIPT` are one program |
| `go vet` with `GOCACHE` under `/private/tmp` | the read lane | runs |

## Decision

### 1. shell_exec runs in one of three kernel-enforced lanes

| lane | profile | who decides | when |
|---|---|---|---|
| **read** | may write exactly its **session-private scratch directory** (`<work dir>/scratch`, where `TMPDIR` points) and the device sinks — never the project, the work directory, `/private/tmp` or the user's `TMPDIR`; denies the side-effect **capabilities** wholesale: `network*`, `mach-lookup`, `mach-register`, `appleevent-send`, `ipc-posix*`, `iokit-open`, `system-socket`, `nvram*`, `job-creation`, `distributed-notification-post`, `user-preference-write`, `lsopen`, `signal` except self and children; `(deny file-read*)` on the credential list; and, as defence in depth only, `(deny process-exec)` for the IPC-capable programs (`sandbox.DefaultDenyExec`, extended by `[sandbox].read_lane_deny_exec`) | nobody — the cage is the decision; the call changes nothing outside its private scratch, the standing of `read_file`, and runs without a prompt in every mode **where `VerifyReadLane` passed at startup** (§5) | the default lane |
| **write** | the ADR-0001 profile (project, work directory, scratch) **plus** `(deny file-write*)` on the persistent files (`.git/hooks`, `.git/info`, `.git/config`, `AGENTS.md`, `CLAUDE.md`, `AGENT.md`, `GEMINI.md`, `.mcp.json`, `.gem-agent.toml`, `.claude/`, at any depth under the project); credential reads stay denied | the ADR-0004 ladder as today (Block floor → model tier → human), or the human in the default mode | the model declares `access: "write"` |
| **operator** | the ADR-0001 profile unchanged: persistent files writable, credentials readable | the operator only — an OperatorOnly floor the model tier, a session `a` and a `--allow` grant never lift | the model declares `access: "operator"`; the `!command` route, which the operator typed, runs here |

The tool gains one argument, `access` (`read` when missing). It is an
**untrusted request for capability, and the request alone grants
nothing**: declaring `read` selects the tightest cage, declaring `write`
or `operator` routes the call to the gate that may grant the wider one.
A false declaration gains nothing — the property ADR-0047 demands of a
declaration (shown, never trusted). A command the read lane refused
comes back with its exit status and one line naming the wider lane to
ask for; a missing declaration is not punished, it runs in the tightest
cage.

The read lane running unasked in the default mode is a loosening of the
ADR-0001 contract as written ("every `shell_exec` prompts") and was the
operator's explicit choice: the kernel is a stronger guarantee than the
prompt was, and `ls` prompting is the friction that pushed sessions into
auto mode.

### 2. The rule tier decides nothing about the shell; it may only escalate

`internal/risk` keeps, for shell commands, the **Block floor** alone —
`sudo`, `rm -rf`, `git push`, `curl | sh`, the fork bomb, credential paths,
`osascript … with administrator privileges`, in every lane. Its patterns are
advisory and generous; a spelling they miss now costs a missed *prompt* the
cage catches anyway, never a missed *cage*. For the file tools, whose
arguments are structured paths, the tier stays exact: where the path lies
and what the file is decide the verdict, as ADR-0072 §1.4 set it.

Deleted with their tests: the Safe derivation from command text and every
helper that served it — about 700 lines. `sandbox.Available()` failing
(`--no-sandbox`, a nested Seatbelt) means there is no read lane: every
shell call is a write-lane call and asks; the banner says so.

### 3. One list, three enforcers

`sandbox.ScratchDirs()` already was the one list the profile and the rule
tier shared (ADR-0070 §2). Two more lists of the same kind now live in
`internal/sandbox`, each read by the profile builder and by the file tools'
verdict: `PersistentFiles` / `PersistentFile` (what later sessions trust)
and `CredentialFilters` / `CredentialPath` (what no lane but the operator's
may read). A disagreement between what the kernel denies and what the
tools refuse is impossible by construction, not by review.

### 4. Architecture tests close classes B, C and E

`internal/archtest` walks the AST of every non-test file and fails on:

- **B** — in the packages that take model- or project-supplied paths
  (`tools`, `mention`, `skills`, `instructions`, `ignore`, `mediastore`,
  `docext`, `hooks`), any `os.Open/OpenFile/ReadFile/ReadDir/Stat/Lstat/
  Create/WriteFile/Readlink/Remove/Rename` outside a named allowlist whose
  every entry carries its reason (the containment check's ancestor walk,
  the operator's own absolute references, the skills root itself, the
  root-less default reader). A stale allowlist entry fails too.
- **C** — anywhere in `internal` and `cmd`, `io.ReadAll`, `os.ReadFile`,
  `os.ReadDir`, `bufio.NewScanner`, `.CombinedOutput()` and `.Output()`
  outside `internal/bounded`, the one package whose primitives return the
  `more` fact with the bytes. Every former copy (tools, mention, skills,
  ignore, instructions, docext, config, mcp, memory, riskbook, statedir,
  telemetry, the clipboard capture, the piped stdin, the session listings)
  now calls it; the session listing and the riskbook scan report a cut
  they used to swallow.
- **E** — `risk.Classify` is called from exactly one function,
  `Agent.decide`, whose `Decision` (mutating or not, verdict, floor) is what
  `gated`, the allowlist floor and the auto ladder read.

### 5. Design review and revisions (2026-09-05)

An independent reviewer read the draft and set four conditions; each
changed the design before acceptance.

**Three layers, three jobs.** The draft's "a missed pattern costs a
prompt, never a cage" was too broad, and the roles are now explicit:

| layer | decides |
|---|---|
| the sandbox | the upper bound of capability and reach — what the command can touch at all |
| the model tier | consistency with the request, meaning, side effects, uncertainty — within that bound |
| the operator-only policy | what no model approval lifts: the Block floor (`rm -rf`, `sudo`, `git push`, credential paths…) in every lane, the operator lane, unconfined execution |

A write-lane command may write the project; that does not make a
recursive force-delete inside the project the model's to approve. The
floor is the third layer, and it stays.

**Capabilities, not programs.** The draft made a program list part of
the read lane's safety argument. It is not: the read lane denies the
capability families the kernel names (`mach-lookup`, `mach-register`,
`appleevent-send`, `ipc-posix*`, `iokit-open`, `system-socket`, `nvram*`,
`job-creation`, `distributed-notification-post`, `user-preference-write`,
`lsopen`, `network*`, `signal`, `file-write*`), and the program list is
defence in depth that makes a refusal legible. The first probe of Apple
Events had used `get name` of an application, which sends no event; a
real event (`tell application "System Events" to get name of first
process`) *is* stopped by `(deny appleevent-send)`. `sysctl-write` was
left out because `uname` and node's allocator use it; `ps` runs under
no Seatbelt profile at all, which predates this ADR.

**What "non-mutating" means.** A read-lane command may change its
session-private scratch directory and the device sinks, nothing else.
The shared `/private/tmp` and the user's `TMPDIR` are denied; `TMPDIR`
is pointed at the private directory for read-lane commands.

**Unconfined is a mode, not a lane.** With `--no-sandbox`, or when
`sandbox-exec` cannot apply a profile, an approval buys none of the
lane's constraints. So: a sandbox that is configured on but cannot apply
here is a startup error naming `--no-sandbox`, never a silent fallback;
under `--no-sandbox` every `shell_exec` is OperatorOnly — the model tier
never approves it, a session `a` and a `never` policy never lift it —
and the audit record carries `lane=unconfined:<declared>` so the
approved lane and the applied profile are recorded together.

**The read lane is verified, not assumed.** At startup (and again when
`/clear` rotates the work directory) `VerifyReadLane` runs the real
profile under `sandbox-exec` against probes that must fail — a project
write, a socket connect, a signal to another process, a write into
`/private/tmp`, launching a denied program — and two that must succeed
(a scratch write, running a program). The unasked read lane exists only
where every probe behaved; otherwise every `shell_exec` asks and the
banner says why.

**Two edge cases from the agent-board review.** `git init`, `git clone`
and `git remote add` write `.git/config` and `.git/hooks`, so they fail
in the write lane; the write lane's denial note names the operator lane
(the one prompt is the point — a hook is what runs unsandboxed next),
and `TestLaneEnforcement` pins `git init` as a write-lane refusal and an
operator-lane success. A build that needs its cache (`GOCACHE`) fails
in the read lane with Go's lower-case "operation not permitted"; the
denial hint matches case-insensitively, so the model is told to use the
write lane rather than left to retry.

**Behaviour tests beside the AST tests.** The architecture tests pin
where the primitives are called; they do not prove the call is right.
So: `TestReadLaneCorpus` runs the old text-tier corpus — redirects,
`tee`, `sed -i`, `find -exec`, `xargs`, `env`, `awk system()`, `$(…)`,
python and perl file writes, `dd`, `install`, `mv`, `cp`, `truncate`,
`chmod`, `ln`, `git init`, `/dev/tcp`, a python socket, a
CoreFoundation preference write, `kill` — against the read lane and
asserts the project file is byte-for-byte untouched; `TestVerifyReadLane`
refuses an allow-everything profile; the agent's
`TestDecisionBoundaryIsModeIndependent` checks lane × command × policy
give one answer; `TestUnconfinedShellIsTheOperatorsAlone` pins the mode.

### 6. Independent verification pass (2026-09-05) and revisions

Four fresh-context reviewers read the implementation (sandbox layer with
real probes; the decision path with a 450-row mode × lane matrix; the
bounded-I/O migration and every documentation claim; the design against
the organisation's recorded lessons). Thirty-eight findings; the ones
that changed the design or closed a hole:

- **The terminal was reachable (high).** A child kept the operator's
  controlling terminal; `/dev/tty` opened read-only and `TIOCSTI`
  typed into gem-agent's own prompt — `!command`, `/auto`, a `y` — from
  the read lane. Every lane now denies `file-ioctl` and `file-read*` on
  the tty devices, and the runner starts the command in its own session
  (`Setsid`), so `/dev/tty` does not resolve at all; verification opens
  it and expects failure.
- **`.git` itself (high).** The write lane denied writes *under* `.git`
  but not renaming `.git` away and back with planted hooks, or replacing
  it with a `gitdir:` pointer file. The `.git` entry joins the persistent
  list. **Residue, recorded:** a persistent file in a *subdirectory* can
  still be replaced by swapping its parent directory (a rename checks the
  directory's own path only). gem-agent reads instruction files at the
  root and its ancestors, so this reaches nested `.claude/`, nested
  repositories' hooks and other agents' nested `CLAUDE.md`; the
  structural fix — recording digests of the persistent files and
  refusing a changed one until the operator confirms — is ADR-0074.
  The project's own parent directory is never writable in the write
  lane, so the whole-project swap the live E2E environment allowed is
  closed.
- **Verification was vacuous in two probes.** `kill -0 1` and a connect
  to port 9 fail for any non-root user, sandbox or not. The probes now
  connect to a loopback listener this process opens and signal its own
  parent, and each must-fail probe first *succeeds* in an unsandboxed
  control run.
- **Read reach (design review V2).** The read lane bounded writes but
  read every mount — the walk from `/` that ADR-0070 §3 had made Review
  ran unasked. It now denies `/Volumes`, `/Network` and `~/Library`
  (toolchain directories excepted); this ADR reverses ADR-0070 §3 for
  the read lane knowingly: the kernel, not a text rule, bounds the walk.
- **The read lane's environment.** A bare `env` printed the operator's
  exported tokens into the transcript. Variables whose names look like
  tokens, keys or passwords are not inherited by read-lane commands.
- **Here-documents.** The system shells create here-document files under
  `/private/var/tmp` whatever `TMPDIR` says (no narrower rule than the
  directory lets them through — probed); that one shared directory is
  the documented exception to "nothing shared", beside `/dev/fd` for the
  command's own descriptors.
- **The lane in front of the operator.** The approval box, the ⚙ line,
  the `gate_decision`/`auto_decision` records and the
  `approval.decision` telemetry now lead with the lane
  (`[write] cmd`, `[unverified:read] cmd`, `[unconfined:write] cmd`); an
  `access` value that names no lane is refused before any gate instead
  of passing as read.
- **Under a `never` policy or `--allow shell_exec`** the write lane runs
  unattended with the network — the reference documentation had said the
  opposite and advised `--allow` over `--auto` for shell; corrected. The
  behaviour is ADR-0008's: `never` is the operator's explicit loosening.
- **An opt-out.** `[sandbox].read_lane_prompts = true` keeps the prompt
  for read-lane commands (the cage still applies) — the compatibility
  option the breaking-change process asks for.
- **The architecture tests** resolve imports, so an alias, a dot-import,
  a function value or a variable named `bounded` cannot hide a call; only
  `internal/agent` may import the rule tier.
- **Bounded I/O residue**: a descriptor leaked on `@` budget exhaustion;
  exactly-cap instruction, memory and skill files were cut mid-rune;
  `--continue` guessed on a cut session listing; hook output was cut
  silently. All fixed.

The reviewer's consolidated final report (three items, none an
end-to-end attack) closed the pass:

- **R1 — the verification probe wrote a fixed name.** A pre-existing
  `.gem-agent-lane-probe` in the project — a symlink, say — was written
  through by the unsandboxed control run and then removed. The probes
  now write only into files this process created exclusively
  (`O_EXCL`, random names) and remove only those.
- **R2 — a persistent file behind another name.** `write_file` on
  `notes.md`, a symlink or hard link to `AGENTS.md`, was Safe by name and
  wrote through the link. Two changes, both structural: the verdict is
  taken on the real path (`Registry.RealPath`, symlinks resolved inside
  the roots), and the file tools never write in place — they create a
  new file beside the target and rename it over the name, so a write
  never lands in an inode reached through another name (a hard link
  gets a fresh file; the linked original keeps its bytes).
- **R3 — a state directory under a shared root.** With the work
  directory placed under `/private/var/tmp`, the read lane's
  here-document allow covered it. The project and the work directory
  are now denied by name *after* every shared allow, and only the
  private scratch is re-allowed last; a work directory under a shared
  root is pinned by test.

Recorded, not changed: `ps`/`top` run under no Seatbelt profile; pty
allocation (`script`, `expect`) fails in every lane; `.gitmodules` and
`.gitattributes` are writable in the write lane (neither executes without
the operator's own git command); credential locations outside the list
are readable and, in the write lane, sendable — the model tier or the
operator is the judge there.

### 7. Confinement is measured too (2026-09-05, after v0.70.2)

§5 made the read lane a verified claim and left `Confined` — the fact
that sandbox-exec applies any cage at all — to the configuration: the
binary exists, the profiles compile, therefore confined. Reports of
unofficial ports (the source rebuilt for WSL without the sandbox) showed
the gap: a build that stubs the sandbox-exec check, or a foreign
`sandbox-exec` that ignores its profile, passes as confined, fails the
read-lane probes, and runs every command as a *write-lane* call — Review
tier, approvable by the model tier in auto mode — with no kernel behind
it. That is weaker than `--no-sandbox`, which is operator-only.

`VerifyWriteLane` now runs at startup and on `/clear` beside
`VerifyReadLane`: under the real write-lane profile, a write under an
instruction file's name inside the project and a write directly under
the operator's home (the one place this lane always denies that is
neither project, work directory nor scratch) must fail, each after a
control run that succeeds unsandboxed; an ordinary project write and
running a program must succeed. Probe files are created exclusively
under random names and removed. If any expectation fails, the session is
**unconfined**: `Enforcement{}` — every shell command is the operator's
to answer, the banner says why, and the commands stay wrapped (a cage
that half-works is still in the way). `/status` reports the measured
state, not the setting. This is the last valve for a build nobody here
verified: the protection degrades to the human, never silently to the
model.

## Alternatives considered

- **Keep the text tier and add the missing spellings.** Rejected: the
  domain is unbounded (nine passes, twenty findings, two re-finds).
- **A program deny-list as the read lane's basis.** Rejected after
  design review: a program list is a regex by another name; the
  capability families are the basis, the list a second line.
- **Keep every `shell_exec` prompting in the default mode.** Rejected by
  the operator: the kernel is a stronger guarantee than the prompt was,
  and the prompt on `ls` is what drove sessions into auto mode. Offered
  instead as `[sandbox].read_lane_prompts`.
- **Keep ADR-0070 §3's walk-from-`/` rule as an escalation-only text
  rule.** Rejected in favour of bounding the read reach in the kernel
  (`/Volumes`, `/Network`, `~/Library`).
- **Container isolation.** Out of scope: gem-agent is macOS-only and
  runs the operator's own toolchain; Seatbelt is the platform's cage.

## References

- ADR-0001 (sandbox mechanism), ADR-0004 (the ladder), ADR-0008 (`never`
  / `always`), ADR-0047 (declarations shown, never trusted), ADR-0053
  (`--allow`), ADR-0070 §2 (one shared list), ADR-0072 (the findings)
- Apple Seatbelt SBPL operations as probed on macOS 26 (§Context, §5, §6)
- Independent review reports of 2026-09-05 (four reviewers; kept outside
  the repository)

## Consequences

- Class A cannot reopen through a spelling: a new way to write the shell
  text changes nothing about what the kernel denies. What remains
  reviewable is the capability list itself and the scratch semantics —
  a short, documented set, which is the point.
- Two holes the regex tier was covering close for real: `defaults write`
  (not a file write) and every IPC-capable program reached by a spelling
  the regex did not list.
- A command that needs the network or a cache directory fails once in the
  read lane and is re-issued in the write lane — one extra round trip the
  first time, and the model is told which lane. Such commands were never
  Safe before (`curl`, `go test` were not on the read-only list), so no
  prompt that existed disappears except for read-lane commands, which the
  operator chose to let run.
- The `access` argument is visible in the approval box and the transcript.
- `TestLaneEnforcement` runs the three profiles under real `sandbox-exec`
  against the spellings of the probes above, and against a project checked out under a scratch root; it is the load-bearing
  test, as ADR-0001's enforcement test was.

## Lessons

- **Move a decision from an unbounded domain to a bounded one.** Command
  text is unbounded; kernel operations are a short, documented list. When a
  reviewer keeps finding spellings, the fix is not another spelling.
- **A declaration may select a cage, never a permission.** The lane
  argument is safe to trust because trusting it wrongly costs the declarer,
  not the operator.
- **A rule enforced twice must be one list read twice.** ADR-0070 §2's
  scratch list was the pattern; the persistent and credential lists follow
  it.
- **Close a class with a test that names the class**, not with the fourth
  fix of an instance. `internal/archtest` is that test for B, C and E —
  and a static test needs a behaviour test beside it (`TestReadLaneCorpus`).
- **Probe the real capability, not a look-alike.** `get name` of an
  application sends no Apple Event; the first probe concluded the wrong
  rule from it. A probe input must exercise the path it claims to test.
- **A claim about the kernel is checked at startup**, on the machine it
  runs on. `VerifyReadLane` is what lets "runs unasked" be said at all.

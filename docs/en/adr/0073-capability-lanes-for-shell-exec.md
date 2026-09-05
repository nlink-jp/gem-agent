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
| **read** | no project or work-directory writes (scratch and the device sinks stay); `(deny network*)`; `(deny user-preference-write)`, `(deny sysctl-write)`, `(deny signal)` except self and children, `(deny lsopen)`, `(deny mach-lookup)` for the pasteboard and keychain services, `(deny process-exec)` for the IPC-capable programs (`sandbox.DefaultDenyExec`: osascript, open, launchctl, defaults, security, pbcopy, shortcuts, automator, scutil, networksetup, systemsetup; `[sandbox].read_lane_deny_exec` adds more); `(deny file-read*)` on the credential list | nobody — the cage is the decision; the call is **non-mutating by construction**, the standing of `read_file`, and runs without a prompt in every mode | the default lane |
| **write** | the ADR-0001 profile (project, work directory, scratch) **plus** `(deny file-write*)` on the persistent files (`.git/hooks`, `.git/info`, `.git/config`, `AGENTS.md`, `CLAUDE.md`, `AGENT.md`, `GEMINI.md`, `.mcp.json`, `.gem-agent.toml`, `.claude/`, at any depth under the project); credential reads stay denied | the ADR-0004 ladder as today (Block floor → model tier → human), or the human in the default mode | the model declares `access: "write"` |
| **operator** | the ADR-0001 profile unchanged: persistent files writable, credentials readable | the operator only — an OperatorOnly floor the model tier, a session `a` and a `--allow` grant never lift | the model declares `access: "operator"`; the `!command` route, which the operator typed, runs here |

The tool gains one argument, `access` (`read` when missing). It is **not
a gate input**: declaring `read` can only tighten the cage, declaring
`write` or `operator` can only add scrutiny. A false declaration gains
nothing — the property ADR-0047 demands of a declaration (shown, never
trusted). A command the read lane refused comes back with its exit status
and one line naming the wider lane to ask for; a missing declaration is
not punished, it runs in the tightest cage.

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

## Consequences

- Class A ends as a class. A new shell spelling cannot open a hole; the
  worst it can do is make the Block floor miss a prompt.
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
  fix of an instance. `internal/archtest` is that test for B, C and E.

# ADR-0086: The kernel reads the file — the credential list stops being a matcher the file tools carry

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-13) — shipped in v0.79.0; the cage did not actually install until v0.79.1 (§5, amended) |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, reading the v0.78.0 release: "this credential-file and environment-variable protection looks like it is walking the same road as the command-safety evaluation we withdrew — combinations grow, and every hole found is answered with another pattern." |
| Amends | ADR-0085 (§2 is withdrawn: names are not withheld), ADR-0073 §3 (the credential list gains a kernel enforcer for the file tools and loses its role as their boundary) |
| Relates to | ADR-0001 (sandbox-exec is the mechanism), ADR-0072 §4 (`os.Root` confinement, unchanged), ADR-0076 (the finite list stays the list), ADR-0052 (every skip is reported) |

## Context

ADR-0073 withdrew the Safe derivation from shell command text. Its
reason was a domain argument, and the knowledge base records it: a
command string is an unbounded domain, the kernel is a bounded one, so
move the judgment to the kernel and leave the text rules as a floor
that can only raise a verdict. What the shell lanes kept afterwards —
thirteen `blockPatterns` — is affordable precisely because a miss
costs one prompt the cage would have caught anyway, never a hole.

The file tools never made that move. They do not pass through
Seatbelt: they are in-process Go code opening through `os.Root`, and
the only thing standing between `read_file .env` and the model is
`sandbox.CredentialPath`, a matcher written in Go. There a miss is not
a missing prompt. It is the file's content in the transcript.

The operator's observation is what the history shows. Counting the
commits that touched the credential rule since ADR-0073:

| Change | Count |
|---|---|
| Entries added to the list | 1 |
| Changes to the matching rule | 6 |

The single entry is `.config/mcp-bridge`. The six rule changes are case
folding, judging a write on the real path, the environment scrub, the
scrub's prefix exemption becoming a name list, suffix matching becoming
whole-segment matching, and the walks resolving their root. Four of the
six landed in one release, each from an independent review finding, and
each was a spelling the previous rule had not considered: a symlink, a
case, a suffix, a word boundary.

That is the withdrawn design's signature, and the reason is the same:
**the set of ways to spell a path to a known file is unbounded.** The
list of files is finite and is not the problem. Reaching a canonical
form before comparing against it is the problem, and every fix so far
has been another step toward a canonical form rather than a decision to
stop deriving one.

The environment scrub is worse, and is the withdrawn design outright: a
denylist regex over an unbounded space of variable names. Measured
against the shipped regex, these pass:

```
OPENAI_KEY  GH_PAT  STRIPE_SK  ANTHROPIC_KEY  GPG_PASSPHRASE
SESSION_COOKIE  BEARER  LICENSE_KEY  PAT
```

`NPM_TOKEN` is caught and `OPENAI_KEY` is not, and no amount of added
words converges. That half has no kernel to move to and is decided
separately in ADR-0087; this ADR is about the file tools.

### What the kernel actually does

Measured 2026-09-13 on this machine, with a profile carrying only
`(allow default)`, `(deny file-write*)`, `(deny network*)`, the
credential deny and the template re-allow:

| Operation on `.env` | Result |
|---|---|
| `cat .env` | `Operation not permitted` |
| `stat .env` | allowed — the deny is `file-read-data`, see below |
| `cat .env.example` | allowed — the template re-allow holds |
| `grep -r` over the directory | refuses that one file, continues |
| `ls -a` | **the name `.env` is listed** |

So the kernel is a complete boundary for content, and it is not a
boundary for metadata or for names. The operation matters: `file-read*`
would refuse the metadata too, and Go's `os.Root` listing stats every
entry, so one denied name failed the whole directory read and every
walk in the project answered "no matches (0 files scanned)"
(independent review, measured — the first cut of this ADR shipped that
way). `file-read-data` refuses the content and leaves the stat, which
is what a walk needs and is not the secret.

Spawn cost, same machine, 10 to 20 runs each:

| What | Cost per run |
|---|---|
| bare `/usr/bin/true` | 1.8 ms |
| `sandbox-exec` + `/usr/bin/true` | 7.8 ms |
| `sandbox-exec` + this binary | 27.4 ms |

A file-tool call already costs a model round. 27 ms per call is not a
budget worth designing around.

## Decision

### 1. A read the model asked for happens in a sandboxed child

The registry's read primitives run in a child process wrapped by
`sandbox-exec` under a purpose-built profile. One spawn per tool call,
this binary re-executed with an internal subcommand; the request rides
stdin (argv is world-readable through `ps`), the bytes ride stdout,
bounded on both sides.

The profile is the base body plus `(deny network*)`, plus
`sandbox.CredentialFilters(home)` as `(deny file-read* …)` with the
`.env.example` family re-allowed after it. It is built from the same
`internal/sandbox` list as every other enforcer: no second list, and
now a fourth reader of the first one.

Project confinement stays in Go. The kernel does not know where the
project is, `os.Root` already refuses an escape at the syscall
(ADR-0072 §4), and that mechanism has never been the source of a
spelling bug. The kernel is given exactly one job here: **in this
process, credential material cannot be opened.**

Covered: `read_file`, `file_info`, `summarize_file` (through
`read_file`), and the whole `search_files` walk, which runs inside the
child so that every open it performs is adjudicated. `view_image` and
`read_document` are covered for the CALL — the tool runs in the child,
so a credential path is refused there and never reaches the operator as
a success — while the bytes the model finally receives are re-read in
process by the attachment path, which runs only when the call itself
ran (`ran`, ADR-0072 §1.1). The cage decides whether that happens; it
does not carry the pixels.

### 2. The refusal is the operator's question, and the matcher stops being the boundary

A denied open returns a typed error. `Agent.execCall` turns that error
into the operator-only prompt ADR-0085 §1 defined, and on approval the
read is re-issued **in process**, which is the operator lane's
authority applied to a file tool: the operator is the only party who
may read credentials, and they have just said so.

Nothing reaches the model before the gate, because a denied open
produces no bytes.

`risk.credentialRead` is therefore no longer required for correctness.
It is kept for one reason: to raise the prompt without spending a spawn
on the common case, and to keep the approval record identical. A miss
in it now costs a spawn and a less direct prompt, never a leak. That is
the property ADR-0073 gave the shell lanes, now given to the file tools.

### 3. Names are not withheld — ADR-0085 §2 is withdrawn

The kernel lists the name and refuses the content. A file name is not
the secret; the content is. ADR-0085 §2 withheld the name on the
reasoning that a listing naming `.env` is "the read that asks, offered
on every round" — but a count invites the same guess, the model reaches
for `.env` either way, and the operator wants to be asked for exactly
that read. Meanwhile the withholding is a second rule over the same
unbounded spelling domain, and it is where two of the six rule changes
landed.

So: `list_files` and `list_tree` list credential-named entries like any
other entry and carry no credential code at all. `search_files` names
the file it could not read and keeps going, the shape `grep -r` already
has and the shape ADR-0052 asks for. `credentialSkipNote` and its
counting are deleted.

This also settles a contradiction between the two runtimes: lagent
ADR-0015 §2 reports the names, gem-agent ADR-0085 §2 reports a count.
Neither survives; both runtimes show the name and refuse the content.

### 4. The ceiling is written down, not chased

Three things this design does not do, stated here so that a future
review reports them as known rather than as holes to close with another
pattern:

- **A hard link to a credential file under an ordinary name.** There is
  no real path to canonicalise: the link *is* a real name. Not closable
  by any path rule.
- **A copy of a credential file under an ordinary name.** The same.
- **A secret inside a file the list does not name.** Content inspection
  is the unbounded domain this whole ADR exists to avoid.

The control for all three is the one the system risk review's chapter
10 already recommends and both prior ADRs already record: real secrets
do not live in a working copy the agent reads.

### 5. Degradation is measured, like the lanes

At startup the child is verified the way the read lane is (ADR-0073
§7): a probe file the profile must refuse and an ordinary file it must
read. On failure the file tools fall back to in-process reads with
`risk.credentialRead` as the boundary, and a startup warning names it. A degraded state
that claims the kernel is watching would be worse than the matcher.

*Amended 2026-09-13, writing the architecture review's second revision:
§5's "with `risk.credentialRead` as the boundary" was never true of the
walk. That function judges a path ARGUMENT, and `search_files` has
none: it is not in the credential read set, its verdict is `Safe`, and
it never reaches a gate. So on a machine where the cage could not be
installed, the walk opened credential files and printed matching lines
to the model with nothing in between — weaker than the release before
this one, which withheld the entry and reported a count — while the
degradation note told the operator the opposite. `readForSearch` now
refuses a path on the one list before it opens the file, returning the
permission error the walk already knows how to report, so a refused
file lands in the same `[not read: …]` footer whether the kernel
refused it or this check did. The refusal is unconditional rather than
switched on the cage's absence: a mode branch would put the safety on
the path that is exercised least, which is exactly how this shipped.
Inside the child the kernel still refuses whatever the check misses, so
the list is the fast path here and not the boundary — §2's property is
unchanged. This adds no rule about how paths are spelled; it is the
same one list, consulted once, at the one place the walk opens a file
(§6).*

### 6. The criterion this leaves behind

**An entry is cheap; a rule is the smell.** Adding a path to
`internal/sandbox` is the mechanism working as designed. Adding a rule
to how paths are compared is the signal that the judgment sits in the
wrong domain. Three rule changes in a row is not a matcher that needs a
fourth fix; it is a boundary in the wrong place.

## Consequences

- The credential matcher stops being load-bearing for reads. Its
  remaining jobs are building the profile, the shell Block floor (where
  a miss costs one prompt), and the write tools' Block (§Not covered).
- Deleted: the walk withholding machinery, `credentialSkipNote`, the
  credential branch of `list_files` and `list_tree`, and ADR-0085 §2
  together with the review rows that argued about it.
- Added: one profile function, one internal subcommand, one typed
  error, one retry branch, one startup probe.
- Each covered tool call costs about 27 ms more. A call already costs a
  model round.
- `search_files` becomes more useful, not less: it reports the file it
  could not read instead of pretending it was not there.
- **Not covered: writes.** `write_file` to a credential path stays a
  Go-side Block. A write destroys a secret rather than publishing it,
  and the write path is gated for every call already. The symmetric
  move — a write child under a profile that denies
  `file-write*` on the same list — is available and is not taken here;
  taking it should be its own ADR with its own measurement.
- lagent takes the same decision as its own ADR, in its own numbering,
  as ADR-0073's design was taken there.

## Alternatives considered

- **A long-lived helper process.** Rejected: it buys 27 ms per call and
  costs a protocol, a lifecycle, a restart path coupled to `/clear`'s
  work-directory rotation, and a new class of failure where the helper
  dies mid-session. One spawn per call has none of those and the
  measurement says the spawn is not the problem.
- **Sandboxing the agent process itself.** Not possible: the profile
  would have to allow everything the agent legitimately does, which
  includes reading and writing the transcript and reaching the network,
  and `sandbox_init` is irreversible and deprecated.
- **Kernel-enforced project confinement too** — `(deny file-read*)` with
  the roots re-allowed. Rejected: the child must read its own
  executable and the system libraries, so a global read deny turns into
  a list of system re-allows, which is a new unbounded list. `os.Root`
  already does confinement correctly.
- **Keeping the matcher as the boundary and adding the kernel as a
  second layer.** Rejected: two boundaries for one rule is what the
  "one list, N enforcers" rule exists to avoid, and it would leave the
  matcher load-bearing, which is the whole complaint.
- **Content inspection.** Rejected, unbounded, as in ADR-0085.

## Independent review (2026-09-13, before release)

Two readers who did not write the change reviewed the release diff, one
per runtime, and agreed on the Critical. The change as first committed
was broken: **`search_files` answered "no matches (0 files scanned)"
for any project holding a credential-named file** — a confident false
negative, worse than the leak it replaced. Findings and outcomes:

| # | Finding | Outcome |
|---|---|---|
| Critical | `(deny file-read*)` covers `file-read-metadata`; Go's `os.Root` listing stats every entry, so one denied name failed the whole directory read and the walk dropped the error silently | Adopted — the deny is `file-read-data`; content refused, stat left, measured |
| High | The parent's pipe cap (85 KB) silently halved a 200 KB read | Adopted — the cap is above anything a covered read emits, and a cut is stated |
| High | `file_info` collapsed the permission error into "not found": the child never exited 3, the operator was never asked, the model was told the file was absent | Adopted — the cause is carried, and a refusal ends the batch |
| High | A cancelled `search_files` lost its partial result and its label | Adopted — the parent keeps what the child wrote and labels it |
| High | The `[not read: …]` note was unreachable for a credential file | Adopted — it fires, measured |
| Medium | The file child did not get `ChildEnv`, falsifying ADR-0087 §2's "every spawn site" | Adopted for the child and its probe |
| Medium | The probe ran a shell, not the child, and built a different profile — it passed while every walk was broken | Adopted — the probe drives the real child, the runner's profile, and a listing |
| Medium | Any `EACCES` became "credential material" | Adopted — the refusal says the sandbox refused the read |
| Medium | The hidden subcommand ran any registered tool | Adopted — it runs the covered reads only |
| Medium | A missing work directory failed every read and leaked an absolute path | Adopted — the child keeps the project root |
| Medium | "the banner says so", "the request rides argv", "Covered: view_image" | Adopted — all three corrected above; the request rides stdin, which is also the better choice |
| Low | The probe directory leaked | Adopted |
| Low | `FileReadProfile` omitted the tty hardening | Adopted |
| Low | The degradation note had no next command | Adopted |
| — | **No test ran the real child under the real profile** — the reason every defect above shipped green | Adopted, and it is the important one: `TestFileChildUnderTheRealProfile` builds this binary and drives it under the installed profile |

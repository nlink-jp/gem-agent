# ADR-0122: the lanes' anchors are resolved once, and the defences against a racing process are taken out

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-18) |
| Date | 2026-11-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator, after three releases in two days that each closed what a reviewer had reproduced: what was gained and what was built were out of balance, and nobody had asked how often any of it could happen. Operator: review what was released, and take out what does not earn its place |
| Amends | ADR-0119 (§1: a read is no longer held to the compared bytes), ADR-0120 (§8 is withdrawn), ADR-0121 (§2 and §3 are withdrawn, and the comparisons of §1, §5 and §7) |
| Relates to | ADR-0073 (the lanes and their checks), ADR-0058 (the work directory), ADR-0099 (added directories) |

## Context

v0.104.0, v0.105.0 and v0.106.0 each closed what an independent reader had reproduced under the real sandbox profile. None of it had happened in use. The readers were asked to attack from inside a lane with no line drawn around who the attacker is, and a finding that could be reproduced was treated as one that had to be closed. The code that came of it: a comparison of the bytes a skill read returned with the bytes that were compared, with a matcher of its own for case and Unicode form; a directory type written on raw system calls (`bounded.Dir`, 369 lines); and startup checks that tell a replaced probe from the one they made, a command ended from outside from one that was refused, and make every control twice.

Read again with one question put first — under what conditions, and how often, can this happen — the findings fall into two groups.

| What was closed | What has to be true for it to happen | What happens |
|---|---|---|
| A name swapped for a FIFO between a look and the open | a program on this machine swapping names for as long as it takes | the open waits |
| A probe of the startup checks replaced while the check runs | a program left running in the write lane that watches for a file under a random name which exists for milliseconds | `x` written into one file, or files under four names deleted |
| A skill swapped between the compare and the read | the same kind of program | the swapped text reaches the model once |
| A check's command ended from outside, read as a refusal | the same, and a lane that is in fact broken | a check passes that should not |
| A trusted skill rewritten during a session | a `git pull`, or a command that writes a directory the skill links to | the new text reaches the model unasked |
| A lane's work directory replaced by a link, then a rebuild of the lanes | two ordinary commands in the write lane, then the operator adds a directory | the write lane may write where the link leads |

The first four need a program written to race the runtime. Whoever can run one in the write lane can rewrite the project's own code, which the operator runs outside any sandbox afterwards: that is more than any of the four gives. The last two need no race. ADR-0119's compare-and-ask closes the fifth. The sixth was open in every released version: the lanes are built more than once — at startup, at `/clear`, at each grant — and each build resolved the project's and the work directory's names again, so a name that had become a link anchored the profile on the link's target (measured on v0.106.0 by calling the build after `rm -rf "$GEMAGENT_WORK_DIR" && ln -s <dir> "$GEMAGENT_WORK_DIR"`: the write lane's profile allowed `<dir>`. A project with no protected name in it can be replaced the same way). The runtime also writes under the work directory's name itself, outside the sandbox, when it saves a large MCP result: after the same two commands the result's file was made where the link led (measured).

## Decision

1. **The sandbox is held against a command the model ran by mistake or was talked into running. It is not held against a program written to race the runtime.** A finding is judged in this order: under what conditions and how often it can happen; who does it; what they already have; what it newly gives them. A finding outside that line is recorded and not built against. Whether a way in has been written down anywhere is not part of the judgement, and neither is how much code closing it, or taking a closing out, would be.

2. **The directories the lanes are anchored on are resolved once.** The real path of the project, of the work directory and of the temporary directory is the answer the name gave the first time it was resolved in this process (`sandbox.ResolveKept`). A later build resolves the name again only to compare: when the project or the work directory no longer resolves to the kept answer, no lanes are built, every shell command asks from then on, and a note says which directory was replaced and that a restart brings the sandbox back. The lanes built earlier stay in the way. A name that is simply gone keeps its answer, as a removed work directory did before. The temporary directory keeps its answer and stops nothing: a profile anchored on the old place gives a link nothing.

   The same comparison is made where the runtime uses such a name again after a lane could have replaced it: before an MCP result is saved under the work directory — a replaced one saves nothing, and the call says so — and before an added directory's probes are made in it at a rebuild — a replaced one fails its check and is dropped like one that is gone (ADR-0114 §2).

   Nothing is held open for this, and no file is compared by device and number. A directory replaced between the comparison and the use is a race.

3. **The startup checks keep to their probes by holding them, and no longer defend their own verdict.** A probe file is made under a random name through the directory it is in, held open as an `os.Root`, and stays open: the control writes into the open file and never opens a name. Clean-up removes the names the check made, through the root, and nothing by path. Every probe command still has ten seconds and is ended with what it started (ADR-0121 §4), and the scratch probe keeps its random name (§8).

   Withdrawn from ADR-0121: the second control after the lane's refusal, the comparison that tells a replaced probe from the one that was made, the check that a probe directory is empty and still under its name, the three endings other than success and failure that `Denial` told apart (§2, §3, and those parts of §1, §5 and §7), and the file-read check's reading of the child's own exit status. A check that passes wrongly makes no lane weaker, so a process that makes one pass has gained nothing; and the comparison by device and number was never measured on a network volume.

4. **The skill walk reads through the skill's root again, and `bounded.Dir` is removed** (ADR-0120 §8 withdrawn). The walk is the one v0.105.0 had, with its three bounds: a sub-directory is opened from its parent with `Root.OpenRoot`, which waits on a FIFO swapped in after the listing. The digest of a tree is the same before and after, so no trust record changes.

5. **A skill is compared before every read, and the read is not compared again** (ADR-0119 §1 amended). Withdrawn: the check of the returned bytes against the manifest of the compared tree, the manifest's own following of links and matching of names by case and form, and the listing that left out entries the digest does not cover. The matcher existed to keep the byte check from refusing a file the volume had found under another spelling; its first form refused such a skill for good.

6. **Not changed.** The question a changed skill raises and everything about how it is answered (ADR-0119 §2–§13); the opens that take only a directory or only a regular file, and the ones that cannot wait (ADR-0120 §1–§7, §9); the bounds of the skill walk; the clipboard capture's file (ADR-0121 §9); a skill directory whose name holds a control or format character is still not listed — a name travels in a repository, and needs no program.

## Consequences

- The notes "… was replaced while the sandbox was being checked" and "… showed nothing: the command was ended by a signal" are gone. "… did not finish within 10s" stays.
- A listing of a directory inside a project skill shows every entry again; the line "[not listed: N that are not part of the trusted skill]" is gone.
- A skill can no longer be refused as "still changing" because a file was asked for under another spelling. That notice is left for a skill that changes twice while its question is being answered.
- A session whose project or work directory was replaced by a link asks for every shell command until it is restarted.

**Accepted, and written down.** Each needs a program on this machine that races the runtime:

| What can happen | Result |
|---|---|
| A name swapped for a FIFO between a listing and the open | the skill compare, or the start, waits until it is interrupted |
| A probe directory replaced, during a check, by a link to another directory inside the same root | files there under the check's four names are removed |
| A check's command ended from outside | a check passes, where the lane is in fact broken |
| A skill swapped between the compare and the read | the swapped text reaches the model once; the next use compares |
| A directory replaced between the comparison of 2 and its use | the lanes are anchored on the kept answer all the same; what the runtime makes by path under the name — the scratch directory, a saved MCP result, a check's probes — lands where the link leads |

An empty directory added to a trusted skill changes no digest, and its name now shows in a listing: a name of at most 255 bytes, from whoever can write the skill's directory.

## Tests the design requires

- A work directory replaced by a link: the rebuild builds nothing and every command asks; the same for the project; a replaced temporary directory changes no profile; a work directory that is gone rebuilds as before; an MCP result is not saved through a work directory that became a link. Put back, the plain resolution at each build fails the first two.
- The control writes into the file that was made when its name has become a link, and the link's target is unchanged; clean-up removes nothing from a directory put under a probe directory's name outside the root.
- The checks pass on APFS and on the volume a test is pointed at.
- The digest of a skill's tree is what it was (the tests of the walk's bounds and of the digest stay as they are).

## Alternatives considered

- **Leave what was released as it is.** The code runs at every start and at every use of a project skill, a third of it on system calls the standard library already wraps, for events that need a racing program. Rejected.
- **Hold every anchored directory open and compare what is under its name by device and number** — built after v0.106.0 with a table of held directories and a test that classified every call taking a path, and not released. It closes the race decision 2 leaves, at fifteen times the code. Rejected by decision 1.
- **Compare a skill file by file at each read instead of digesting the whole tree at each use** — it would have made the walk's bounds unnecessary. Not taken up here: it changes what a trust record means, and nothing in use asks for it.

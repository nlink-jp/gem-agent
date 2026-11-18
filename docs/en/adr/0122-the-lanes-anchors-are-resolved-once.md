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
| A lane's work directory replaced by a link, then a rebuild of the lanes | a command in the write lane that the operator approves or has approved in advance, or a project's own code the operator ran; then the operator adds a directory, and one more such command | the write lane may write where the link leads |

The first four need a program written to race the runtime. Whoever can run one in the write lane can rewrite the project's own code, which the operator runs outside any sandbox afterwards: that is more than any of the four gives. The last two need no race. ADR-0119's compare-and-ask closes the fifth. The sixth was open in every released version: the lanes are built more than once — at startup, at `/clear`, at each grant — and each build resolved the project's and the work directory's names again, so a name that had become a link anchored the profile on the link's target (measured on v0.106.0 by building the lanes again after the work directory had been moved aside and a link put under its name: the write lane wrote where the link led). It starts from a command that replaces the directory, and a command in the write lane runs when the operator approves it or under an approval given in advance; the rule tier holds an `rm -rf` for the operator whatever the mode (measured). What stands behind it is a model led by text it read, or a project's own code run by a command the operator approved — and either can already rewrite the project.

## Decision

1. **The sandbox is held against a command the model ran by mistake or was talked into running. It is not held against a program written to race the runtime.** A finding is judged in this order: under what conditions and how often it can happen; who does it; what they already have; what it newly gives them. A finding outside that line is recorded and not built against. Whether a way in has been written down anywhere is not part of the judgement, and neither is how much code closing it, or taking a closing out, would be.

2. **The directories the lanes are anchored on are resolved once.** The real path of the project, of the work directory and of the temporary directory is the answer the name gave the first time it was resolved in this process (`sandbox.ResolveKept`); a later build uses it and does not resolve the name again. A profile anchored on the name gives a link put under it nothing: the kernel refuses a write that goes through the link. A work directory that has become a link gets no scratch directory made in it, and the read lane then asks, as it does whenever it has no scratch.

   That is all of it. Nothing is held open, nothing is compared, no mode is added and the operator is told nothing new.

3. **The startup checks keep to their probes by holding them, and no longer defend their own verdict.** A probe file is made under a random name through the directory it is in, held open as an `os.Root`, and stays open: the control writes into the open file and never opens a name. Clean-up removes the names the check made, through the root, and nothing by path. Every probe command still has ten seconds and is ended with what it started (ADR-0121 §4), and the scratch probe keeps its random name (§8).

   Withdrawn from ADR-0121: the second control after the lane's refusal, the comparison that tells a replaced probe from the one that was made, the check that a probe directory is empty and still under its name, the three endings other than success and failure that `Denial` told apart (§2, §3, and those parts of §1, §5 and §7), and the file-read check's reading of the child's own exit status. A check that passes wrongly makes no lane weaker, so a process that makes one pass has gained nothing; and the comparison by device and number was never measured on a network volume.

4. **The skill walk reads through the skill's root again, and `bounded.Dir` is removed** (ADR-0120 §8 withdrawn). The walk is the one v0.105.0 had, with its three bounds: a sub-directory is opened from its parent with `Root.OpenRoot`, which waits on a FIFO swapped in after the listing. The digest of a tree is the same before and after, so no trust record changes.

5. **A skill is compared before every read, and the read is not compared again** (ADR-0119 §1 amended). Withdrawn: the check of the returned bytes against the manifest of the compared tree, the manifest's own following of links and matching of names by case and form, and the listing that left out entries the digest does not cover. The matcher existed to keep the byte check from refusing a file the volume had found under another spelling; its first form refused such a skill for good.

6. **Not changed.** The question a changed skill raises and everything about how it is answered (ADR-0119 §2–§13); the opens that take only a directory or only a regular file, and the ones that cannot wait (ADR-0120 §1–§7, §9); the bounds of the skill walk; the clipboard capture's file (ADR-0121 §9); a skill directory whose name holds a control or format character is still not listed — a name travels in a repository, and needs no program.

## Consequences

- The notes "… was replaced while the sandbox was being checked" and "… showed nothing: the command was ended by a signal" are gone. "… did not finish within 10s" stays.
- A listing of a directory inside a project skill shows every entry again; the line "[not listed: N that are not part of the trusted skill]" is gone.
- A skill can no longer be refused as "still changing" because a file was asked for under another spelling. That notice is left for a skill that changes twice while its question is being answered.
- A session whose work directory was replaced by a link builds its lanes on the name it had; its read lane asks, having no scratch directory.

**Accepted, and written down.** Each needs a program on this machine that races the runtime:

| What can happen | Result |
|---|---|
| A name swapped for a FIFO between a listing and the open | the skill compare, or the start, waits until it is interrupted |
| A probe directory replaced, during a check, by a link to another directory inside the same root | files there under the check's four names are removed |
| A check's command ended from outside | a check passes, where the lane is in fact broken |
| A skill swapped between the compare and the read | the swapped text reaches the model once; the next use compares |

These need no race, and are accepted too. Each starts from a command in the write lane that the operator approved, or approved in advance, or from a project's own code the operator ran; whoever stands behind it can already rewrite the project.

| What can happen | Result |
|---|---|
| A work directory replaced by a link, then a large MCP result | the runtime saves the result's file where the link leads, under the name it derives |
| The same, then a call to an MCP server that is given the work directory's name | the server writes where the link leads; the model can name any directory to it in the call as it is |
| An added write directory, or a project with no protected name in it, replaced by a link, then a restart | the new process takes the name as it finds it, and its lanes are anchored where the link leads (home, `~/Library` and `~/.ssh` are refused as added directories) |
| An added write directory replaced by a link, then a rebuild | the check's probes are made and removed where the link leads; the lanes refuse the directory |
| An empty directory added to a trusted skill | its name, at most 255 bytes, shows in a listing; git carries no empty directory and the write lane is refused a project's `.claude` (measured), so the skill's directory has to be a link to a place the write lane writes |

## Tests the design requires

- A work directory replaced by a link: the lanes built again write neither where the link leads nor through it, and the runtime makes nothing there; a replaced temporary directory changes no profile; a work directory that is gone is built on again. Put back, the plain resolution at each build fails the first.
- The control writes into the file that was made when its name has become a link, and the link's target is unchanged; clean-up removes nothing from a directory put under a probe directory's name outside the root.
- The checks pass on APFS and on the volume a test is pointed at.
- The digest of a skill's tree is what it was (the tests of the walk's bounds and of the digest stay as they are).

## Alternatives considered

- **Leave what was released as it is.** The code runs at every start and at every use of a project skill, a third of it on system calls the standard library already wraps, for events that need a racing program. Rejected.
- **Hold every anchored directory open and compare what is under its name by device and number** — built after v0.106.0 with a table of held directories and a test that classified every call taking a path, and not released. It closes a replacement between two system calls as well, at ten times the code. Rejected by decision 1.
- **Compare the name at every rebuild, and put every command before the operator when it differs** — the first form of decision 2, with a note to the operator and the same comparison before an MCP result is saved and before an added directory is probed. Cut before release: it adds a mode and four checks for something that starts from the operator's own approval of the command that replaces the directory.
- **Compare a skill file by file at each read instead of digesting the whole tree at each use** — it would have made the walk's bounds unnecessary. Not taken up here: it changes what a trust record means, and nothing in use asks for it.

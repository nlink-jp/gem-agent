# ADR-0123: the line comes before the fix — what was built outside it since v0.100.0 is taken out

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-18) — implemented |
| Date | 2026-11-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The operator, reviewing ADR-0122: the fixes since v0.100.0 were asked for in answer to proposals that stated no threat and no frequency, so the requests themselves are to be judged again, not kept because they were made |
| Amends | ADR-0122 (§1 extended; §3, §4, §5 and the "Not changed" list of §6 withdrawn) |
| Supersedes | ADR-0119, ADR-0120, ADR-0121 |
| Relates to | ADR-0074 (trust is granted to content), ADR-0010 §2 (`/skill`), ADR-0073 (the lanes and their checks), ADR-0109, ADR-0111, ADR-0112, ADR-0114 |

## Context

ADR-0122 §1 drew the line: the sandbox is held against a command the model ran by mistake or was talked into running, not against a program written to race the runtime. It was applied to the findings of v0.104.0–v0.106.0, and what the operator had asked for was kept because it had been asked for. Each of those requests answered a proposal that said how to fix something and not who could make it happen, how often, or what they would gain. A request made without those facts is not a reason to keep what it built.

Every change since v0.100.0 was read again from where an attack would start:

| Origin | How often | Held against |
|---|---|---|
| A. The model, led by text it read | can happen in ordinary use | yes — the read lane runs unasked; the write lane runs on the operator's approval |
| B. A repository's content before the operator trusted it | can happen | yes — the trust record (ADR-0074) |
| C. The operator's own acts (a `git pull`, another terminal, the configuration) | often, without intent | as correctness, not as an attack |
| D. A program on this machine that races the runtime | never seen in use | no (ADR-0122 §1); whoever runs one can rewrite the project's code |
| E. Malware running as the operator | — | no |

**A correction.** ADR-0121 §8 said a link put under the scratch probe's fixed name had `x` written through it. The probe's command runs in the read lane, so the write lands only where the read lane may already write. It gave nobody anything.

## Decision

1. **A finding is judged before anything is built, in this order: under what conditions and how often it happens; who makes it happen; what they already have; what it newly gives them.** A fix is proposed with those four answers, and "do not fix" is one of the options put to the operator. A request that answered a proposal without them is judged again on them.

2. **A project skill is no longer compared when it is used** (ADR-0119 superseded). Origin C, and B through a pull. The upstream author who changes a skill already writes the project's code, which the operator builds and runs; the changed text reaches the model until the next `/clear` or start, which compare it as ADR-0074 says, and the commands it leads to still go through the lanes and the approval. A model that rewrites a skill gains nothing in its own session and is caught at the next start. Taken out: the compare at use, the question and its three outcomes, the disable state, `tools.NeedsOperator` and the agent's handling of it, the question dialog's heading and opening choice, the plain prompt's word answer, `/skill` as a step of the turn (it is a text expansion again, ADR-0010 §2), the `skill_trust` / `skill.trust` records, the refusal texts, and the rule that a skill directory whose name holds a control or format character is not listed — it kept the question's text clean, and the TUI already makes every message inert at its ingress (ADR-0093).

3. **A skill's digest is taken by the walk v0.103.1 shipped**, with its cap of 2,000 files. The walk's bound on entries and depth, and its handle for each level, are taken out: what they bound is a tree a repository carries as files, which the file cap already bounds, and what an unbounded tree costs is a slow start. Compared on ten trees — links in and out, a dangling link, a skill directory that is a link, nested and non-ASCII names, a name with a newline, a file over 1 MiB, 2,100 files, unreadable files and folders, a FIFO, the instruction files — every pin and note is the same before and after, except a skill nested 40 deep: v0.104.0–v0.106.0 cut it at 32 levels, and its whole tree is digested now, so a pin taken of it then asks once at the next start.

4. **Opens are the standard library's again** (ADR-0120 superseded): `os.Open`, `Root.Open`, `os.OpenRoot`, `os.ReadDir`, `os.WriteFile` and `os.RemoveAll` where v0.103.1 used them. Taken out: `bounded.OpenRegular`, `OpenRegularPath`, `OpenRegularPathNoFollow`, `OpenDir`, `OpenDirIn`, `OpenRoot`, `WriteFile`, `RemoveAll`, `ErrNotRegular`, the test `TestOpensDoNotWait`, and ADR-0119's look before opening the four project files. What a FIFO at a name does is make an open wait. Git carries no FIFO, so it takes a command the operator approved or a process outside the lanes; the operator ends the wait with an interrupt. Kept: the file tools' own open, which does not wait (ADR-0072 §4.8, before v0.100.0), and the file-read child opening only the roots a call names (ADR-0114 §4, a share that stops answering).

5. **The startup checks are v0.105.0's again** (ADR-0121 superseded, ADR-0122 §3 withdrawn): probes made exclusively under random names (ADR-0073 R1), the control is the same command outside the sandbox, removal is by path, the scratch probe has its fixed name, and a probe command has no time bound. The clipboard capture's file is made in the temporary directory again, under the random name it had. Taken out: `internal/sandbox/probe.go`'s held directories and the test `TestTheChecksGoThroughHeldDirectories`. Each of these needed origin D (ADR-0121's own table of measurements).

6. **The trust record on disk is the record.** v0.103.1 kept the session's pins when the policy file no longer held the project's pins. The policy file is out of every lane's reach; it loses them when the operator, or a process outside the lanes, removes them. Taken out: that fallback, and `adoptPolicy`, which kept the pins through the session's own writes. Kept: a write into a file the session left out says so (the same commit's other half), and the comparison with the file on disk (v0.103.1).

7. **Kept, judged on the same four answers:**

   | Change | Origin | What it newly gives |
   |---|---|---|
   | ADR-0109, the approved lanes' cache out of the read lane's reach | A, unasked | code planted into a build the operator approved |
   | ADR-0111, a `!` command's output is data | A | text in the operator's voice |
   | ADR-0112, the dialog carries the whole call | A | an approval of a command whose effect the operator could not see |
   | ADR-0114, added directories | C, and A for `/Volumes` | the unasked read lane reading every mounted volume |
   | ADR-0122 §2, the lanes' anchors resolved once | A, through an approved command | the write lane writing outside the project — what the sandbox says it never does |
   | v0.103.1, pins compared with the record on disk | C | — (a defect: `trust --accept` elsewhere was not seen) |
   | ADR-0116–0118, `golang.org/x/net` | — | not security |

## Consequences

- No question about a changed skill. A skill changed during a session is loaded as it is now until `/clear` or the next start, which leave it out and name it, as for an instruction file.
- A FIFO at a name the runtime opens makes it wait until it is interrupted.
- The startup checks run as in v0.105.0.
- The documents that describe what is taken out are changed with it: README and README.ja (the skill question), the manual and the configuration and integration references in both languages, AGENTS.md, and ADR-0074's amendment.

**Accepted, and written down:**

| What can happen | Origin | Result |
|---|---|---|
| A skill changed during a session | C, or B through a pull | the new text reaches the model until `/clear` or a start |
| A FIFO at a name the runtime opens | an approved command, or outside the lanes | the open waits until interrupted |
| A probe replaced while a check runs (ADR-0121's table) | D | files under the check's names written or removed; a check passes or waits |
| A skill tree past the file cap in depth or entries | B | a slow start; nothing outside the cap is pinned |
| The policy file loses the project's pins | the operator, or outside the lanes | the project's files load as unpinned |

## Tests the design requires

- The digest of each kind of tree a pin distinguishes is the same by the walk taken back as by the walk taken out (compared once, on the trees listed in decision 3).
- The tests that held what is taken out go with it; the tests v0.103.1 and v0.105.0 had for the same code come back where they still describe it.
- `make check` passes, and the startup checks pass on APFS.

## Alternatives considered

- **Keep the skill question and take out only what surrounds it.** Rejected: what is left still answers an origin that gains less than it already holds.
- **Refuse a changed skill at use, without asking.** Rejected for the same reason: it stops the model for nothing a start would not catch.
- **Keep the look before the four project files are opened.** Rejected: git cannot carry the FIFO it looks for.
- **Release first, take out later.** Rejected: the twelve commits since v0.106.0 are not released, and this goes out with them.

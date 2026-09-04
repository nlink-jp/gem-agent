# ADR-0070: A loaded skill names its directory, and the rule tier's writable places are the sandbox's

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-04 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Session `20260904-225330`: after loading `incident-research` through `load_skill` and reading its `references/` and `schema.json`, the model ran `find / -name "validate.py" 2>/dev/null \| grep incident-research` with the stated purpose "confirm where the skill's scripts are". The rule tier Blocked it for a reason that was not true (the `2>/dev/null` read as a redirect outside the writable roots), the operator approved, and the walk was killed by Ctrl+C after 65 s |
| Amends | ADR-0010 §2 / §4 (what a skill load discloses); ADR-0004 (the rule tier's Safe set and its notion of writable) |

## Context

The skill was written to Claude Code's contract. Its `SKILL.md` says
"`SKILL_DIR` is the directory containing this SKILL.md" and every
script step is `python3 SKILL_DIR/scripts/validate.py …`; all six
skills-series skills use the same sentence. Claude Code honours it:
the result of its Skill tool opens with the line
`Base directory for this skill: <path>`, and its transcripts show
scripts invoked by that absolute path.

ADR-0010 took the format as-is and chose progressive disclosure: one
description line per skill in the system prompt, the body through
`load_skill(name)` or `/skill <name>`, supporting files through
`load_skill(name, file)`. Its §4 says `scripts/` run through
`shell_exec` under the sandbox and the approval gate. Nothing in that
design tells the model *where* the skill is: not the prompt line, not
the load result (`Skill "x" (global scope) — the user's instructions
…`), not the `/skill` expansion. `load_skill(name, file)` can read a
script; it cannot run one. A project skill happened to work because
`.claude/skills/<name>/scripts/…` resolves from the project root; a
global skill under `~/.config/gem-agent/skills` is reachable by no
relative path at all. The search was the model's rational move given a
fact it had been denied.

Why the search was a serious event and not a curiosity:

- The sandbox denies writes only. Reads are `(allow default)` by design
  (ADR-0001), so `find /` walks every mount — on the machine that ran
  the session, five SMB shares and two remote Time Machine shares
  among them — with the 120 s shell timeout as the only bound.
- `find` and `grep` are in the rule tier's read-only set. Without the
  `2>/dev/null`, the command would have been Safe: run without a
  prompt in auto-approve mode. The prompt the operator did get was an
  accident: the redirect rule does not know that `/dev` is writable
  under the profile. `buildExecFn` allows `TMPDIR`, `/private/tmp` and
  `/dev`; the rule tier knows the project and the work directory. Two
  definitions of "where a shell may write", one per package, and the
  operator was shown the wrong one.

## Decision

### 1. A loaded skill names its directory, in Claude Code's words

The result of `load_skill(name)` and the turn `/skill <name>` injects
both carry the line

```
Base directory for this skill: <dir>
```

before the body — Claude Code's sentence, verbatim, because the skill
files are written against that sentence and the cheapest
compatibility is the same one. `<dir>` is the symlink-resolved
directory the skill was discovered in: the same boundary `Skill.Body`
and `Skill.File` confine reads to (ADR-0010 §4), so nothing new is
disclosed that a read could not already reach. The tool description
says the result names the directory for running the skill's scripts
through `shell_exec`. The system-prompt line is unchanged: the
location belongs with the body, not in the index that decides whether
to load it (progressive disclosure holds).

Running a script is still `shell_exec`: sandboxed, gated, classified
like any other command. This decision adds a fact, not a permission.

### 2. One definition of writable

`sandbox.ScratchDirs()` returns the resolved scratch roots the profile
allows (`TMPDIR`, `/private/tmp`, `/dev`), and both `buildExecFn` and
the rule tier read it. A redirect into a scratch root is not "outside
the project"; `/dev/null` is a sink, and a redirect to it does not
disqualify a command from Safe. The reason the operator is shown
becomes true, and it can no longer drift from what Seatbelt will do,
because there is one list.

### 3. A walk outside the roots is Review, not Safe

A read-only command that walks a tree — `find`, `fd`, `du`, `rg`, and
`grep` with a recursive flag — whose starting point is `/`, `~`, or an
absolute path outside the project, work and scratch roots lands in
Review with the reason "walks the filesystem outside the project and
session work directories". Read-only is not harmless: the read side of
the sandbox is open on purpose, so the cost of a walk is bounded by
the mounts, not by the project. The model tier can still approve a
narrow one (`find ~/.config/gem-agent/skills -name validate.py`), and
in manual mode nothing changes — everything already asks.

Not Block. Block is the floor for what cannot be undone; a walk
destroys nothing. It is a cost, and the model tier exists to weigh
costs.

## Alternatives considered

- **Put each skill's path in the system-prompt line** — rejected: N
  paths in the cached prefix for skills that may never load, and the
  directory is useful only together with the body.
- **Export `SKILL_DIR` into `shell_exec`'s environment** — rejected:
  several skills can be loaded in one session, so which one; an
  environment variable is process-wide (ADR-0068's objection to
  steering one consumer through the environment) and invisible in
  the transcript, where the sentence in the result is exactly what
  the skill author wrote against.
- **Change the skills-series to spell out gem-agent's global path** —
  rejected: it couples the skills to one runtime's layout, breaks the
  Claude Code path, and drop-in compatibility is gem-agent's
  obligation (ADR-0010 §1), not the skills'.
- **Tell the model never to search for a skill** — rejected: a
  negative instruction over-generalises and invents a third route.
  The cause was a missing fact; the fix supplies the fact.
- **Special-case `/dev/null` in the redirect rule and leave the two
  lists** — rejected: two lists drift again. The redirect rule's job
  is to explain what the sandbox will deny, so it has to read the
  sandbox's list.
- **Make `find /` Block** — rejected, §3.
- **Make every read-only command outside the project Review** —
  rejected: `cat /etc/hosts` and `ls ~/Downloads` are single reads.
  The cost is in the walk, not in the location.

## Consequences

- Skills written for Claude Code that run their own scripts now work
  from gem-agent's global directory as well as from a project. The
  six skills-series skills are the first beneficiaries; nothing in
  them changes.
- Transcripts carry the skill directory. For a global skill that is a
  path under the operator's home — already the case for the project
  path on every session record.
- A Safe command may now carry `2>/dev/null` or `>/dev/null`. This
  widens nothing that can run: the profile always allowed those
  writes; only the classification catches up.
- In auto-approve mode a tree walk outside the roots costs one model
  round it did not cost before. In manual mode nothing changes.
- `internal/risk` imports `internal/sandbox` for one function. The
  scratch roots are resolved once at package init; `Classify` itself
  stays free of I/O and remains a pure function of its arguments and
  that fixed list.

## References

- ADR-0001 (sandbox: writes denied, reads open — why a walk is a cost)
- ADR-0004 (the auto-approve ladder; the Safe set and the Block floor)
- ADR-0010 §1 / §2 / §4 (drop-in format, progressive disclosure, scripts through `shell_exec`)
- ADR-0058 (the session work directory as the second writable root)
- ADR-0068 (the environment is process-wide — the objection reused here)

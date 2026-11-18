# ADR-0121: the sandbox checks touch only the probes they made

| Field | Value |
|-------|-------|
| Status | **Superseded by [ADR-0123](0123-the-line-comes-before-the-fix.md)** (2026-11-18) — accepted and implemented the same day; taken out |
| Date | 2026-11-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The review before the release that carries ADR-0120 found that the startup checks of the lanes wrote, outside the sandbox, into a name a lane's process could replace. Operator: fix it before the release |
| Relates to | ADR-0073 §5, §7 and its R1 (the checks; the probe under a fixed name), ADR-0099 §7 (added directories), ADR-0086 §5 (the file-read check), ADR-0107 §4 (leftovers), ADR-0120 (opens that cannot wait) |

## Context

The lanes are claims the runtime checks at every start (ADR-0073 §5, §7): it makes probe files where a lane is supposed to be unable to write, runs a command in the lane that must fail, and counts the failure as the lane's only if the same thing succeeds outside the sandbox — the control. The control of a write was a shell outside the sandbox running `echo x >` on the probe's path, with no bound in time, and the probes were removed afterwards by path.

The probes have to be where the lanes write: the project, an added directory, the work directory, `/private/tmp`. So a process in the write lane — a child left running by a command the operator approved earlier, or another session's command — can replace a probe between the moment it is made and the moment it is used. The final review of ADR-0073 (R1) closed a probe under a fixed name that was there beforehand. It did not close this.

Measured on the real code (macOS 27.2), with a shell loop under the real write-lane profile that replaces every probe it sees, and a directory beside the project that no lane may write:

| What the probe was replaced by | What happened | Runs |
|---|---|---|
| a probe directory, by a link to the outside directory | the clean-up removed files there under the probes' names: `AGENTS.md`, `plain`, `.env`, `created` | 7 of 7, in the first round of checks |
| a probe file, by a link to an outside file | the control wrote through it: the file held `x` | 3 of 3, by the second round |
| a probe file, by a FIFO | the control waited, and the start with it, for the 60 s the test allowed and until the shell was killed | 1 of 1 with a FIFO kept ready to move into place; 0 of 4 with a slower loop |

That the loop cannot write the outside directory itself was checked first. An ordinary probe command takes 0.01 s here, and the slowest (`osascript`) 0.05 s.

How an open through a directory held open behaves was observed before this was designed (`os.Root`, Go 1.27): a link that leads out of the directory is refused, but one that leads to another name in it is followed, with `O_NOFOLLOW` or without; a hard link opens; a FIFO opened for writing without waiting is refused at once; `Root.OpenRoot` on a FIFO waits. So a root does not by itself keep a write from landing in another file, and it cannot be what opens a probe directory.

A file's number is not the file's for good on every volume. On FAT and on exFAT an empty file has a placeholder for a number, and gets its own at the first write. The first form of this change remembered the number from the making; the review before the release ran it on both and found every probe there taken for a replaced one, and left behind. Measured again after the change below, on two disk images: every check passes on both and leaves nothing, and the commit before fails as the review said.

## Decision

1. **A directory that probes are made in is held open, and a probe is made, written, read and removed through it by its one name.** The directory is opened as a directory (`O_DIRECTORY`), and a probe directory inside it is opened from it the same way and not through a link — `bounded.Dir`, which ADR-0120 §8 made for the skill walk, extended with what a probe needs (`DirAt`, `Mkdir`, `Create`, `OpenWrite`, `Remove`, `RemoveDir`, `ID`). A probe directory is used only as empty as it was made: one that holds something is another directory, moved over the name, and nothing in it is the check's. No path goes through a name a lane can replace, so nothing outside the directories the checks hold is written or removed.
2. **The control of a file probe is the runtime's own, and it is made twice.** A probe made beforehand is kept open from its making. For the control it is opened by its name, for writing, without waiting and not through a link, and written only when it is the file that was made: what the name led to and what the descriptor kept from the making holds are, at that moment, the same device and number. A probe the lane's command would create is created exclusively. A probe that is read is opened as a regular file without waiting and compared the same way. No shell runs outside the sandbox on a probe's path any more. After the lane's command was refused, the control is made again: the same file must still be writable, or readable, here; a name the command would have made must still be free; a probe directory must still be under its name.
3. **A refusal is a lane's command that ran to its own end and failed, between two controls that succeeded.** A probe that was replaced fails the check. So does a command that was ended by a signal — its own, or one that ended a program the shell was waiting for; a process in the write lane can end any of the operator's processes — one that could not be run at all, and one that did not finish (4). None of them shows anything either way, and the lane is unverified, as after any failed check: the read lane asks, or every shell command does, or the added directory is an error.
4. **Every probe command has ten seconds, and is ended with everything it started.** It runs in a process group of its own, and the group is ended: a shell ended alone left the program it was waiting for. Ten seconds is two hundred times the slowest probe measured on a quiet machine. The design review measured the probes while processes in the write lane kept renaming a directory, which slows every open of a file (ADR-0120): 0.25 to 0.33 s a probe with one such loop, 1.1 to 1.4 s with sixteen. A lane can therefore bring a check to its bound without touching a probe, and what that costs is what a failed check costs (3).
5. **Clean-up removes what the check made, from the directories it holds, and nothing by path.** A file is removed while its name still holds the file that was made, and a probe directory while its name still holds that directory; what a lane's command may have made in a probe directory is removed there by its name. What was put under a probe's name in the meantime is left, and is not named among the leftovers (ADR-0107 §4): it is not the check's, and the operator is not told to delete it.
6. **The controls that name no file stay shell commands outside the sandbox** — the socket connect, the signal, launching a denied program — with the bound of 4.
7. **The file-read check is held to the same.** Its two probes are made and removed through its directory, held open: that directory is in the work directory, which the write lane writes. Its control is the runtime's own read of the credential-named probe, before the caged read and after it. Only the child's own report that the kernel refused the read counts as the cage's refusal; a child that failed for another reason, was ended, or did not finish within the bound fails the check. A failed file-read check does what it did: the reads stay in this process, behind the name matcher.
8. **The read lane's scratch probe has a name nobody could have known.** It was one fixed name in the scratch directory, which is in the work directory: a link put under it beforehand had `x` written through it, with no moment to hit.
9. **The clipboard capture's file is made in the runtime's own state directory.** The design review looked for the same pattern outside the checks and found it there: the file was made in the temporary directory, which every write lane writes, and `osascript` — outside the sandbox — wrote the clipboard into the path it was given. Under the real write-lane profile, a link moved under that name had the clipboard written through it into a file no lane may write.

## Consequences

- A process in the write lane can still make a check fail: by replacing a probe, by ending a probe command, or by slowing the machine. The note then says which — a probe was replaced, a probe's command was ended by a signal or did not finish — and the session asks for what it would have run unasked. It reaches nothing outside the lane by doing so.
- It can no longer make a check pass by replacing a probe, by ending a probe command, by closing a probe to everybody, or by taking a name a command was about to make. Residual: a process that makes a lane's command fail for a reason of its own and undoes that before the second control — two moments a few milliseconds apart, at a probe it has to find first — makes the failure read as the lane's. How often such a loop wins was not measured. It matters only where the lane under test does not refuse what it should, and that can be so for the read lane, an added directory's credential names or the file-read cage while the write lane still holds the process.
- The control no longer shows that the shell's own redirection works outside the sandbox. Beyond "this path can be written here", what that showed was that the quoted path names the file: `shellQuote` has a test of its own for that now, and the probes that must succeed inside a lane still go through the shell.
- Residual: the directories the probes are made in — the project, an added directory, the work directory, the approved cache, `/private/tmp` — are opened by their paths. One replaced by a link to another directory before a check starts has the probes made and removed there: files the check made, under random names (two fixed names for the file-read check, made only where they are free), and nothing else.
- Residual: the commands a lane runs still name a probe by its path, and where that path leads after a replacement is decided by the lane's own profile. That is not nothing for a process in the write lane: the read lane may write `/private/var/tmp` and the write lane may not, so a read-lane probe led there through a link — in the moment between its control and the lane's command — writes `x` into a file there (measured by the design review). The check then fails on its second control.
- Not measured: a network volume (SMB, NFS). The comparison takes both numbers at the same moment, which is what has to hold there; `PROBE_TEST_VOLUME` runs the checks on a volume of the operator's choosing.
- Not decided here: the design review read three more places of the same kind in the code and did not run them — each acts by path on a directory a lane writes after that directory itself may have been replaced, the residual ADR-0120 records for a root named by path. They are the operator's to schedule.

## Tests the design requires

- A loop under the real write-lane profile replaces every probe it sees by a link — files, then directories — while every check runs fifteen times and until a check has met a replaced probe: nothing in the outside directory is changed, removed or created, and the directory itself is not touched.
- The same with a FIFO moved over each probe file: every round of checks comes back.
- Without the race: each probe file in turn is replaced at the moment it has been made and at the moment its control has written it, and the check fails on a replaced probe each time.
- Each kind of replacement — a link out, a link to another name in the directory, a hard link, a FIFO read and unread, another file, a directory, nothing — is refused by the control at once; what it led to is as it was, and the clean-up leaves it.
- A probe closed to everybody under the lane's command, and a name the command would have made taken by something else, fail the check.
- A probe directory that is not empty when it is opened is not used, and nothing in it is removed; one replaced by a link later is not gone through.
- A probe command that does not end is ended at the bound with what it started, and its check fails; one ended by a signal, one whose program was ended under the shell, and one that could not be run are not refusals.
- The added directory's check fails the same ways: a name taken, a credential probe closed under a read lane that reads credentials, its probe directory replaced under the command; and a read-only added directory whose probe directory was replaced is an error, not a note.
- Every check passes and leaves nothing with the project and the added directories on a FAT and on an exFAT volume (`PROBE_TEST_VOLUME`; on the temporary directory's volume without it).
- The file-read check passes on the child's report of a refusal only, and fails when its probe is gone, replaced or closed.
- The scratch probe does not write through a link waiting under its old name.
- The package of the checks holds no call that names a file by path, and one place that starts a probe command.
- Each property put back is seen by a test: thirty of them.

## Alternatives considered

- **Run the shell control inside a profile that allows a write to the probe's own path only** — not taken: a FIFO at that path still waits; whether a hard link to another file counts as that path is not documented; and the profile language is not documented either.
- **Make the probes where no lane writes** — not possible: a probe shows what a lane does where the lane writes.
- **Hold the directories as `os.Root`** — not taken: a root follows a link to another name inside it and opens a hard link, so a write could still land in another file of the project, and `Root.OpenRoot` waits on a FIFO (Context).
- **Keep a shell control that only looks (`[ -e path ]`)** — not taken: it would show that the quoted path names the file, which a test of `shellQuote` shows without a shell outside the sandbox at every start.
- **Bound the time and change nothing else** — not taken: the write through a link and the removal through a directory would stay.
- **Compare the directory a check opens with the root the file tools already hold** — not done here: it would close the first residual for the project and the added directories, and needs the checks to be handed those roots.

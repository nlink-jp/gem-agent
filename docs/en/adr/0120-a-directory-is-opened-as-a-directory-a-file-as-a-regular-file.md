# ADR-0120: a directory is opened as a directory, a file as a regular file

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-17) |
| Date | 2026-11-17 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The fix after v0.104.0 found four project files whose plain open waited on a FIFO, and left every other open unmeasured. Operator: measure the rest, and fix the ones whose name is not the operator's own choice |
| Amends | ADR-0119 (its residual: a directory that is listed no longer has one) |
| Relates to | ADR-0099 (added directories), ADR-0072 §4.8 (the file tools open without waiting) |

## Context

A plain open of a FIFO waits until something opens the other end. ADR-0119 met that for four files whose names a project chooses, and opened them only when they are regular files. The other opens were not measured then. They were measured here, each with a FIFO at the name and on the real code; an open that had not returned in two seconds counted as waiting (macOS 27.2, Go 1.27).

How an open behaves on a FIFO with nobody at the other end:

| Open | Result |
|---|---|
| `os.Open`, `os.ReadFile`, `Root.Open` | waits |
| `os.OpenRoot`, `Root.OpenRoot` | waits — they open plainly and look afterwards |
| an open for writing (`os.WriteFile`) | waits for a reader |
| with `O_DIRECTORY` | answers "not a directory" at once, through a link too |
| with `O_NONBLOCK` | opens at once |
| `Stat`, `os.ReadDir`, a path that leads through the FIFO | answers at once |

What a shell command can do to the roots themselves, run under the real profiles:

| | write lane | operator lane |
|---|---|---|
| make `.claude/skills` or `AGENTS.md` as a FIFO | refused by name | done |
| remove the session's work directory and leave a FIFO under its name | done | done |
| the same for a directory added with write access | done | done |
| move the project directory into another root and leave a FIFO under its name | done | done |

So the name of a root is something the model can choose, not only the names inside it.

What waited:

| The FIFO | What waited | Who can put it there |
|---|---|---|
| `<project>/.claude/skills` | skill discovery, the look before the trust question, the pins (at startup and after a tool writes) | a command the operator approved, or anything outside the runtime |
| the session's work directory | the tidy-up at exit and at `/clear`, the file tools' root, an `@` reference to it | the write lane |
| an added directory | the file tools' root (at startup, at a resume, in the child that runs a read), an `@` reference to it | the write lane |
| the project directory | the instruction files, `.mcp.json`, the pins, the file tools' root | the write lane |
| a memory, a memory directory, a transcript, the sessions directory, the `.project` marker, the two snapshots of the last session, the directory holding the work directories, `policy.toml`, `risk-rules.md`, the project's `rules.md`, the operator's own skills and instruction directories | their loads, most of them at every start; saving a memory over one never came back | the operator, or a process running as the operator outside the lanes |
| the configuration file (`--config`), `headers_file` | its load | the operator, by naming it |

What did not wait: every read the model can ask for (`read_file`, `search_files`, the listings, `view_image`, `show_image`, `edit_file`, the copy source of `write_file`), an `@` reference to a file and `/add-dir` — they open without waiting and look at what they hold (ADR-0072 §4.8) — and the entries of a skills directory and the files of a skill (ADR-0119).

## Decision

1. **A directory that is listed is opened as a directory** (`bounded.OpenDir`, `bounded.OpenDirIn`): with `O_DIRECTORY`, so the kernel refuses anything else at the open, without waiting. Nothing is looked at before the open, so there is nothing to swap in between. It is the flag `os.ReadDir` passes itself, and it changes nothing about how a directory opens.
2. **A root is opened by path only where a look finds a directory** (`bounded.OpenRoot`). `os.OpenRoot` takes no flags, so this is a look and then an open.
3. **A file this runtime reads under a name it fixed is opened only when it is a regular file** (`bounded.OpenRegularPath`, as ADR-0119 did for the project's four): the memories, the transcripts, the marker, the snapshots, `policy.toml`, `risk-rules.md` and the project's `rules.md`. The state directory is included although the lanes cannot write it, for three reasons. One name in it, the work directory, turned out to be the model's to replace. None of these files is ever meant to be a FIFO. And a rule with no exception inside the runtime's own tree can be held by a test, where judging name by name is what left these opens behind.
4. **A file written under a fixed name is written only over a regular file** (`bounded.WriteFile`): a memory, and the temporaries the policy file and the project's rules are saved through.
5. **What the operator names is not changed here.** The configuration file given with `--config`, and the `headers_file` the configuration names, still wait on a FIFO until something writes it: a FIFO or a process substitution there may be what the operator means. Whether to refuse one is the operator's decision, and is left open.
6. **A test holds it** (`TestOpensDoNotWait` in `internal/archtest`): outside `internal/bounded` there is no `os.Open`, `os.OpenFile`, `os.OpenRoot`, `os.Create`, `os.WriteFile` or `toml.DecodeFile`, and no call of an `Open`, `OpenFile`, `OpenRoot`, `Create`, `ReadFile` or `WriteFile` method, except those the test lists with the reason each cannot wait. Its first form left `os.OpenFile` and the `OpenFile` method out; the review of the change found what that hid.

## Consequences

- A FIFO at `<project>/.claude/skills` is a project with no skills. A FIFO where a root was is a root that does not open, and what needed it reports an error at once.
- A `policy.toml` that is a FIFO stops the start with an error that names it; it used to stop the start without a word. A `risk-rules.md` that is one is not loaded: the start goes on with the notice any unreadable rulebook gets, and the judge works without it. A link to a regular file is read as before.
- A file under a fixed name that is not a regular file is not written: saving a memory over a FIFO is an error.
- Residual: a name swapped for a FIFO between the look and the open still waits — for a file (3, and ADR-0119), for a root (2), and for the sub-directories of the skill walk, which `manifestWalk.dir` opens with `Root.OpenRoot` after their listed kind. For a directory that is listed nothing is left. The swap takes a process that keeps rewriting the name while the runtime opens it, in a place it can already write, and all it gains is a wait.
- Closing the rest takes an open that cannot wait (`O_NONBLOCK`) for files, and for roots an `os.OpenRoot` that takes flags, which Go does not have. The file tools already open with `O_NONBLOCK` (ADR-0072 §4.8), but how such an open and its reads behave on an online-only file of a cloud folder has still not been observed on purpose. Until it is, the loaders keep the plain open after the look.

## Alternatives considered

- **Leave the state directory as it is** — rejected, for the reasons in 3.
- **Open every file with `O_NONBLOCK` now** — not taken: unobserved on an online-only file, which is why ADR-0119 withdrew it.
- **Derive a root from an `O_DIRECTORY` descriptor through `/dev/fd`** — rejected: it would close the residual for roots, but it rests on how a device node reopens a directory, and it renames the root in every error.
- **Refuse a FIFO given as `--config`** — left to the operator (5).

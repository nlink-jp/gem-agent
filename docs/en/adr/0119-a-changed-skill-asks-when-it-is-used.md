# ADR-0119: A changed skill asks when it is used

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-17), implemented |
| Date | 2026-11-17 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The review before v0.103.1 noted that a project skill is read from disk when it is used, not when it is checked. Operator: when it has changed, ask whether to trust it or disable it |
| Relates to | ADR-0074 (trust is granted to content; asking at `/clear` and the reloads was declined there), ADR-0010 (skills; `/skill` injects the body directly), ADR-0080 §4 (a question of its own inside the gate), ADR-0086 §2 (a read asked about after it was refused), ADR-0110 (a pipe without `-p`) |

## Context

ADR-0074 pins the content of what a project gives the runtime, and states
the rule as "compare before consuming". For instruction files the two
moments coincide: the file is compared and read into the system prompt in
one step, at startup and at `/clear`. For a project skill they do not. The
skill's directory is compared when the list of skills is built; its body
and supporting files are read from disk later, each time `load_skill` or
`/skill` uses it.

Measured on the sibling runtime (lagent v0.27.4, a real session; the read
calls are the same here, cmd/skills.go): a project skill was trusted and
pinned, the session started, and `SKILL.md` was rewritten. The note after
a `!` command named the skill as changed. `/skill <name>` then sent the
rewritten body to the model, which followed it; after a second rewrite,
the model's own `load_skill` call returned the second text. Neither read
compared anything.

What changes the file mid-session is mostly the operator's own doing — a
`! git pull`, an operator-lane command, an editor in another window. The
write lane denies `.claude`, and an approved write re-pins the one name it
wrote. But not only the operator: a skill entry that is a link to a
directory elsewhere in the project is protected by name, so a write to the
link's target is an ordinary write (measured for the file tools by the
design review), and MCP servers run outside the sandbox. Either way the
content is one nobody confirmed, loaded as the operator's own instructions
and sent unwrapped (ADR-0010).

ADR-0074 declined to ask at `/clear` and the reloads because nobody is at
a prompt there. A tool call is a different place: the gate asks the
operator in the middle of a turn, and it already carries a question that
is not "may this call run" (the read-only lift, ADR-0080 §4).

## Decision

1. **Every read of a project skill compares first, and returns only what
   was compared.** `load_skill` (the body and the `file` form) and `/skill`
   digest that skill's directory and compare it with the record on disk
   (`recordedPolicy`, ADR-0074's amendment). One implementation takes the
   digest for the pin and for this comparison, through an open root — the
   object the loader reads — so the two cannot disagree about an unchanged
   skill. The digest comes with its manifest, each covered file's own
   digest, and the read is held to it: the bytes read for a file are
   returned only if they are the bytes that tree holds for that file. A
   compare before the read alone would leave the read open — content
   swapped in between, or swapped in and back again, would be returned
   under the digest of something else. A file the volume found under
   another spelling of its name — a different case, another Unicode form
   — is held to the entry whose name is the same once those are set
   aside, when no entry has the exact name; names are matched component
   by component, through linked directories too, and a property test
   holds the manifest's answers against what the volume actually returns. A listing of a directory
   inside the skill shows only the entries the digest covers: an empty
   directory is not in it, and its name is text too. Bytes that do not
   match are discarded, and the skill is handled as what it has become. Nothing a
   pin does not cover is served: a skill with more files than a pin
   covers is not read, and a `SKILL.md` past the megabyte a pin covers
   byte for byte is not a skill. Different, or a listed skill the
   recorded set does not hold: the question of item 2. A directory that
   is gone — or moved aside, with another put at its path — is refused,
   not asked about. Every refusal the model is given — this one, and a trust answer that could not be recorded, included — tells it not to read the skill's files by other means and to say that the skill was not loaded. With pins off, or with nothing recorded for the
   project, nothing is compared — the state the list was built in. A
   global skill is never compared.
2. **The question is the runtime's own, and it is not put through the
   approval gate.** It opens under its own heading, names the skill by its
   validated name and shows its directory, says either that the content
   has changed since it was trusted or that the trusted record does not
   hold it, says the content should be looked at, and says what each
   answer does — including that trusting records the content for later
   sessions. It offers trust, disable and "not now" — the third is the
   unanswered outcome made visible — and opens on "not now", so a key
   pressed too soon changes nothing. Nothing else is offered, and no
   standing answer reaches it: not the session allowlist, a `"never"`
   policy, auto-approve or the model tier. It does not consult the
   session's exclusion set either: a listed skill is one the session
   loaded. The wording does not say whose change it was.
3. **Three outcomes: trusted, disabled, unanswered.** The question is put
   through the dialog the round-limit question uses, which already
   answers "nothing chosen" for Esc, Ctrl+C, a question that arrives after
   the turn was interrupted and a turn that ends under it; all of these,
   and "not now", are unanswered. One answer records persistent trust, so
   it takes keys nobody types by accident: on this question the arrows
   select and Enter confirms; the digit keys do nothing, the options are
   not numbered, and the selection does not wrap — so neither "1" and
   Enter nor Tab and Enter, what sending a message looks like, can reach
   "trust" from the opening choice. The model's own questions keep their
   one-press digits. On the plain REPL the answer is a word,
   `trust` or `disable`; a blank line, a number, any other text and a
   closed input are unanswered — the reader is shared with the prompt,
   and a line typed ahead must not be an answer. An answer that arrives
   after the turn was interrupted is discarded.
4. **Trust records what was asked about.** The directory is digested
   again. If it differs from the digest that raised the question, nothing
   is recorded and the question is asked once more about the new state; a
   second difference refuses the read as unanswered and says the skill is
   still changing; like an unanswered question, it is not asked again in
   that turn. Otherwise that digest is recorded for that one name
   under the policy file's lock — by a write of its own, which takes the
   digest it expects — and the name leaves the session's exclusion set. A
   call that was cancelled records nothing. The read then runs, held to
   that digest's manifest as in item 1. The pin stays whatever the read
   finds; it is what was asked about.
5. **Disable holds for the session.** The answer is kept beside the grant
   under a lock, read by `load_skill` and `/skill` before anything else,
   and written only after the question has been answered, never from
   inside the tool. Later uses of that skill are refused without a
   question; each one shows the operator a one-line notice. The answer is
   forgotten where the list of skills is rebuilt (`/clear`,
   `/skills reload`) and only there — not at `/mcp reload`, and not when
   `/clear` clears in place because no new session could be opened.
   Undoing it inside the session takes `gem-agent trust --accept` from
   another terminal and one of those two.
6. **Unanswered refuses the read, and the skill is not asked about again
   in that turn.** Later uses in the same turn are refused without a
   dialog, with a notice: a model that keeps calling `load_skill` cannot
   keep a dialog coming. The next turn asks again. Nothing is recorded and
   nothing is disabled.
7. **Where nobody is at a terminal, nothing is asked.** The predicate is
   the one the pin check uses at startup, so a one-shot run and a piped
   run both refuse the read. The refusal has its own text: it names
   `gem-agent trust --accept` and does not send the model to the read
   tools for the same file.
8. **`load_skill` says it needs the operator, and the agent asks.** The
   tool's run returns a typed outcome (`tools.NeedsOperator`); the agent
   has the question put outside the floor that abandons a slow tool, and
   runs the tool once more under the context the answer returned. The
   dialog is never inside the tool's own run; every digest, the pin write
   and the read are. The agent learns nothing about skills. This is the
   shape of ADR-0086 §2, where a refused read is asked about and then run
   again.
9. **`/skill` asks before it injects, without a model round.** The TUI's
   event loop only parses the line; the compare, the question and the
   read happen on the turn's own goroutine, through the same guard and
   with the same bound on how often the question is put. A cancelled turn
   raises no dialog, and the outcome is recorded the same way. Trust
   injects the body as ADR-0010 §2 decided; disable or unanswered ends
   the turn without a model call, and the operator has been told why.
10. **What the model is told is fixed text.** `load_skill` results are
    sent unwrapped, so every refusal is built from constants and the
    skill's validated name — never the directory entry's name or the text
    of a filesystem error.
11. **The directory cannot write the dialog.** A skill is not listed,
    with a note that quotes the name, when its directory entry or the
    path it resolves to holds a control or format character or is not
    valid text. The question shows the directory quoted, and the dialog's
    text is made inert at the TUI's ingress like every message.
12. **Every outcome is recorded under a kind of its own:** the question,
    the outcome and each refused repeat, with the skill's name, as
    `skill_trust` in the transcript and `skill.trust` in the audit log.
    Not as a gate decision: the rule learner reads those as approval
    evidence.
13. **The comment that called `load_skill` ungated is corrected.** Its
    description, which the model sees, is left as it is: it says nothing
    untrue.

## Consequences

- "Compare before consuming" holds for skills at the moment of use. After
  a `git pull` that touches a skill, the first use asks once, in the
  session, and the operator answers with one key.
- A read of a project skill costs one digest of its directory and one of
  the file it returns; a trust answer adds a digest; the bound is 2,000
  files of 1 MiB, 4,000 entries looked at, and 32 levels. Measured on an Apple Silicon machine, APFS, warm cache
  (the digest is the code lagent shares): the installed skills (1 to 21
  files, up to 126 KiB) take 0.1 to 2.7 ms per digest; a whole skill
  repository checked out as a skill, `.git` included (248 files, 1 MiB),
  takes 135 ms.
- The agent gains a way for a tool to say it needs the operator; the
  question dialog gains a heading and an opening choice; `/skill` stops
  being a text expansion on the event loop and becomes a step of the
  turn; the pin's digest moves onto a root; the audit log gains one
  event. The design review estimated more — a new method on every
  approval gate — before the existing question dialog was found to have
  the three outcomes already.
- The question, its three labels and the notices go in the catalogue in
  both languages and through the label review. The refusals are
  model-facing and fixed.
- ADR-0010 §2 stands: `/skill` spends no model round.

## Tests the design requires

- One test enumerates the read sites (`load_skill` body, `load_skill`
  `file`, `/skill`) and fails if a site reads without comparing.
- One test holds the digest taken at use equal to the pin's digest for an
  unchanged skill, over the cases the pin distinguishes (links, a file
  past 1 MiB, more files than the cap).
- One test drives a changed skill, with pins on and recorded, to the
  question through the production wiring: with "pins on and recorded" as a
  precondition, a wire that was never connected looks the same as "equal".
- The dialog and the plain prompt each have a test of every way of not
  answering — Esc, Ctrl+C, an interrupt, a turn that ends, a blank line,
  a number, other text and a closed input — and of a digit, a digit and
  Enter, and Tab and Enter not reaching "trust".
- The three outcomes and the unattended refusal each have a test of what
  is recorded, what the model receives, what stays disabled and what the
  exclusion set holds afterwards.
- The window of item 4 is tested by changing the directory between the
  question and the answer, twice, and between the pin and the read. The
  ordinary read is tested with a change that lands between the compare
  and the read, and with one that is taken back before anything compares
  again.
- A directory entry with a control character is tested as not listed.

## Accepted, and written down

- **The operator's own skill of the same name stays shadowed after
  disable**, until the list is rebuilt. ADR-0074's rule that a changed
  project skill must not switch off a skill the operator wrote is applied
  at list time; applying it mid-turn means replacing the list under a
  running turn.
- **A disabled skill stays in the catalogue the model already has** — the
  system prompt's skill section — until the list is rebuilt. The refusal
  is what tells the model.
- **The digest has limits, and what it does not cover is not served:** a
  skill with more than 2,000 files, or with more than 4,000 entries in
  all — files, links and directories together — or with directories nested
  more than 32 deep, is not read at all, a `SKILL.md` over
  1 MiB is not a skill, and a supporting file over the 96 KiB read limit
  is refused. A link inside a skill is covered by its target string; a
  file read through it is held to the entry it leads to. The first
  version of this record accepted "files past the 2,000th are not
  covered"; the implementation review showed that `SKILL.md` itself could
  be one of them. The second bound is on the walk itself, and was added after the first release: directories are neither counted as files nor part of the digest, so a tree of empty directories added to a trusted skill changed no digest and was walked whole at every use. Measured on lagent v0.28.0, which takes the digest with the same code: 100,000 empty directories took 9.6 s per compare, and one directory holding 200,000 files took 0.7 s, because a directory was listed whole before the cap was looked at. A directory is now read only as far as the walk's budget goes, and a tree over the budget is refused like one over the file cap. Within the bounds every digest is what it was, with one exception: a tree of exactly 2,000 files with anything that is not a file it can digest sorting after the last of them, which the old walk refused and this one covers. The budget is twice the file cap, so a skill that keeps every file in a directory of its own still fits. The review of that change found a third cost. A directory was opened by its path from the root, which the runtime resolves one component at a time, so a chain of d directories cost d²/2 opens: 800 empty directories, inside the budget, took 32 s per compare, in the old walk and the new alike. A directory is now opened from its parent's handle, and since one handle is held for each level, nesting is bounded at 32. After both changes the two trees above are refused in 0.3 s and 0.008 s and a chain of 800 directories in 0.005 s, and a tree at every bound — 2,000 files 32 levels down, beside 1,968 directories — is walked in 0.3 s (1.8 s when each file is 1 MiB; that is content, which changes the digest and asks). A file that cannot be digested is not counted wherever it sorts; counted at the cap and not below it, it made the answer depend on its name. And a file whose name the project chooses — a skill's files, an instruction file, `.mcp.json`, the project configuration — is opened only if it is a regular file, looked at before the open: opened plainly, one that is a FIFO held the read, and so startup, until somebody wrote to it — measured for each of the four, and present since each was first read. A name swapped for a FIFO between the look and the open still waits, for a file and for a directory alike; closing that takes an open that cannot wait, and how one behaves on an online-only file of a cloud folder has not been observed. ADR-0120 measured the other opens, and for a directory that is listed nothing is left of this; it also holds the observation, made afterwards on iCloud Drive, after which a file is opened without waiting and nothing is left of this for a file either; and since its 8 the walk opens each level from the one above it without waiting, so nothing is left of it for a directory under a skill. A tree past a bound is refused either way; its digest may differ from a pin taken before, and then startup asks about it once.
- **Scripts.** A skill loaded earlier may have told the model to run a
  script from its directory. If the script changes afterwards, the shell
  runs what is there, under the lane and approval rules of every command.
- **Other readers.** The read tools and an `@` reference can read a
  skill's files as data, wrapped. A body loaded before the change stays in
  the conversation.
- **A lost record.** In the state ADR-0074's amendment describes, a trust
  answer records a set holding that one name.
- **`/clear` and the reloads** still leave a changed file out and name it;
  instruction files have no later read to guard.
- **A project skill that carries a dependency tree** — a `node_modules`,
  a `.venv` — is past 2,000 files and cannot be loaded. It stays in the
  catalogue the model has; the notice says why. The file cap is checked when a file is about to be counted, so a skill of exactly 2,000 files loads whatever directories follow them; the first release checked it at the next entry of any kind and refused that skill.
- **A trust write can still land late** in one narrow case: a call
  cancelled after its last check and then held more than a second on the
  policy file's lock. It records the digest the operator was asked about.
- **A skill that keeps changing** is refused after one more question. The
  operator asks for it again once the pull or the build has finished.

## Alternatives considered

- **Refuse without asking** (the first proposal). Fail-closed and
  smaller, but it sends the operator out of the session to answer a
  question they can answer in it.
- **The ordinary approval dialog with a reason line** (the first draft of
  this ADR). It offers "allow for this session" and "never ask again",
  which here would each act as trust plus a standing entry nothing shows;
  and an interrupted turn answers it no, which would disable a skill
  nobody decided to disable. The design review found both.
- **`/skill` tells the model to call `load_skill`** (the first draft). It
  spends a model round ADR-0010 §2 decided not to spend, and if the model
  does not make the call nothing is loaded and nothing is said.
- **Take a piped line as the answer.** ADR-0110 makes a pipe the
  operator's input, and the stdin gate would read the next line. The pin
  check at startup already treats the same run as one with nobody to ask;
  one run should not be both.
- **Keep the body read at list time.** Supporting files and scripts are
  still read later, so the skill would be half old and half new.

# ADR-0062: delegation-first exploration — firing the dormant agentic_file_search

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-02 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator report: "the model never uses agentic file search — it has become a completely dead feature" |

## Context

Measured across all 75 real sessions (788 tool calls):
`agentic_file_search` fired **once**, and that once was the ADR-0037
E2E itself — the operator's prompt named the tool. Spontaneous
invocations: **zero**. Meanwhile the main model made 177 direct
navigation calls (`read_file` 126, `list_files` 22, `search_files`
21, `list_tree` 8), ten turns contained four or more navigation
calls, and one turn contained thirty — precisely the
delegation-shaped workload the tool was built to absorb, served
manually, with every read replayed on every later round.

This is the third instance of the capability-without-trigger
pattern (ADR-0029's unwired Msgs, the never-firing memory proposals
fixed in v0.39.0), with an aggravating twist: the trigger that
exists is in the wrong layer, and the system prompt supplies a
*competing* trigger for the manual path. The tool's own description
says "far cheaper than several rounds of list/search/read" — but
the Working style section of the system prompt prescribes exactly
that loop, by name and in order ("Orient with list_tree, locate
with search_files, then read_file the specific lines"), and never
mentions `agentic_file_search`. A one-line trigger inside one tool
schema among twenty cannot outcompete an explicit workflow
instruction; the model follows the workflow it was given.

A secondary observation from the one time the tool did run
(ADR-0037 E2E): the main model re-read files afterwards to
double-check the report, halving the saving. The description said
what the tool does but not how much to trust its output.

## Decision

**1. The system prompt routes exploration to delegation first.**
The single "Inspect before changing" bullet splits into two, in
this order:

- *Delegate exploration*: for any question answered by exploring —
  "where/how is X done", "which files touch Y", anything expected
  to take more than a couple of list/search/read calls — call
  `agentic_file_search` first, without waiting to be asked. Trust
  the report; re-read only the lines you will edit or quote.
- *Navigate yourself* only when you already know where to look —
  the existing orient/locate/read guidance, unchanged, now framed
  as the known-target path rather than the default.

The recommendation carries the same specificity as the old manual
guidance did (named tool, named trigger cases, a concrete
threshold), per the v0.39.0 lesson: a vague recommendation next to
specific instructions reads as "rarely".

**2. The trust contract spans all three surfaces.** The
description gains "Trust the report — re-read only the lines you
will edit or quote" — and the report **header** is rewritten to
agree. The first live run after the prompt fix delegated correctly
and then re-explored with 29 navigation calls anyway: the old
header said "quotes may be lossy; verify exact lines with
read_file before editing" at the exact moment the model read the
report, and an in-band invitation outcompetes an out-of-band
contract just as surely as the prompt outcompeted the description
(the v0.44.1 lesson: every surface of one message must carry the
same conditional). The header now reads "Trust it for answering —
re-read only the exact lines you are about to edit", keeping the
before-editing protection and dropping the blanket verification
invite. The child's own quoting rule ("copy quotes exactly, never
reconstruct") is what makes the trust honest. Measured after the
header fix: a second live run (a neutral mechanism question)
delegated first and followed with 6 targeted re-reads instead
of 29 — the questions differed, so this is direction, not a
controlled comparison.

**3. Wiring is pinned by tests.** A prompt test asserts the system
prompt names `agentic_file_search` and that the delegation guidance
precedes the manual-navigation guidance; a tool test asserts the
description carries the trust sentence. The defect class is
"guidance silently absent", which no behavioural test catches
cheaply — the string-level pin is the honest fixture.

**4. Fixed is claimed only after live firing.** Per the standing
lesson, a prompt edit alone proves nothing: the change ships with a
live E2E in an isolated state dir where an unprompted "where/how"
question fires the delegation, verified in the transcript. And
because a dormant feature waking up re-opens its surroundings:
the child runs read-only tools under a deny-all gate (unchanged),
but its token spend now actually occurs — `/usage`'s delegation
category is the place to watch.

## Consequences

- The system prompt changes, so the implicit-cache prefix re-warms
  once (ADR-0018) — the standing, accepted cost of any prompt edit.
- Navigation-heavy turns should shrink; the report replaces the
  exploration in context. If the model starts delegating questions
  it could answer with one `search_files` call, the "literal string
  you already know" counter-trigger in both layers is the lever to
  sharpen — revisit with transcripts, not intuition.
- The child loop's budget (10 rounds) now gets real traffic; a
  question that legitimately needs more shows up as a truncated
  report. That limit stays as designed by ADR-0037 ("a narrower
  question, not a bigger budget") until real transcripts argue
  otherwise.

# ADR-0021: Whole-code review fixes

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: check the whole code — unknown defects may exist despite green tests |

## Context

A six-lens parallel review (concurrency, confinement, LLM layer,
session/resume, TUI, wiring/policy) plus static analysis and live API
measurement found nine confirmed defects and a set of timing-dependent
TUI hazards. Most fixes are plain corrections; this ADR records the ones
that are *decisions* — where more than one repair was defensible.
It follows the bundled-ADR precedent: one wide record for co-shipped
fixes discovered by one review.

Notably, two findings reported as certain by independent reviewers —
"a transcript ending in a dangling function call 400s on resume" and
"an interrupted tool round poisons the history" — were **refuted by
live measurement**: the current API accepts all three suspect history
shapes. No repair code was added for them; a measurement beats a
plausible mechanism.

## Decisions

1. **`/clear` becomes a transcript record, and the schema version moves
   to 2.** `Reset` was the only history mutation without a matching
   transcript record, so a cleared conversation resurrected on resume —
   and a post-clear compaction's `Replaced` index was applied to the
   wrong list, able to land on a tool result (the shape that 400s).
   Load replays a `clear` record by emptying the history, exactly as
   live did. The schema bump makes an *older* build refuse a file with
   clear records instead of silently resurrecting what the operator
   discarded; new builds read schema-1 files unchanged.
2. **The transcript reader becomes line-based and tolerant; Reopen
   repairs the tail.** A crash's torn last line, once Reopen appended
   after it, glued into one invalid line — and the old decoder treated
   any error as EOF, silently dropping every later record (measured:
   1 of 6 turns survived). Load now reads JSONL line by line (unbounded
   reader, no 64KB Scanner limit), skips unparseable lines, and reports
   the skip count so the operator sees "N lines unreadable" instead of
   a shorter conversation that looks normal. Reopen appends a newline
   when the file does not end with one, isolating the tear to one line.
3. **A failed conversation-bearing log write stops the transcript,
   loudly.** Fire-and-forget writes could desynchronise the live
   history from the file, making later compaction indices corrupt the
   replay. After the first failed message/compaction write the logger
   is marked dead, nothing more is written (the file stays a consistent
   prefix), and the operator is told the session can no longer be fully
   resumed. Diagnostics-only degradation was rejected: the file's
   second job (ADR-0005: resume source of truth) makes silent partial
   logging worse than none.
4. **The session file takes an advisory flock.** Two processes resuming
   the same session interleaved appends into a conversation neither
   had. Open and Reopen take a non-blocking exclusive lock for the
   logger's lifetime; a second process gets "session in use" instead.
5. **The session allowlist ('a') sits *below* the Block floor and below
   an explicit "always" policy.** Measured hole: one 'a' on a benign
   `shell_exec` waved every later Block-tier command (sudo, rm -rf,
   force-push, credential paths) through with no prompt, in every mode
   — the gate consulted the allowlist before anyone computed risk. The
   `Approver` interface now carries `mustPrompt`: the agent sets it for
   Block-tier calls and for tools whose policy is "always", and both
   gates skip their allowlist when it is set. Answering 'a' on such a
   prompt still registers the allowlist — for future *non*-Block calls;
   the floor keeps asking. This also fixes the stale-allowlist hole (a
   mid-session policy change to "always" now bypasses the allowlist
   instead of being silently overridden) with no revocation API.
6. **Policy resolution is scope-first, then specificity.** One merged
   pattern map resolved exact-before-wildcard across scopes, so a
   global exact rule beat a project wildcard *tighten* — breaking
   ADR-0008's "tighten anywhere" promise. Rules now resolve project
   scope first (nearest wins), each scope internally exact-first.
7. **`!` and `/` inputs cannot be queued mid-run.** Queued messages
   merge into one input (ADR-0007's one-turn-per-instruction), but the
   merged text was then prefix-routed whole: a queued `!make test` plus
   queued prose executed the prose as shell; a queued `/clear` silently
   discarded what followed. Commands are now refused at queue time with
   a hint (the text stays in the box); merged pending is always prose.
   Queue-as-list was rejected — multiple buffered instructions against
   a moving world is what ADR-0007 deliberately avoided.
8. **A truncated-but-nonempty model response is reported, not
   silently accepted.** The empty-response guard only caught fully
   empty turns; a stream cut off by SAFETY/MAX_TOKENS with partial text
   was stored and returned as a normal answer. The turn still proceeds
   (what happened, happened) but the operator is told the answer was
   cut off and why.
9. **Rejected, on the record:** capturing thought signatures from empty
   non-thought parts (could change replay shape for a pattern never
   observed), memory `.project` marker absence (unreachable within the
   threat model, documented in ADR-0020), case-normalising project
   paths (macOS case-insensitivity splitting memory scope needs a
   deliberately weird cd), and clearing the screen on width *growth*
   (growth reflow does drift the pin in some terminals, but clearing on
   every grow erases visible content repeatedly during a drag resize —
   the existing shrink-only reset with graceful drift is the better
   trade, and its test says so deliberately).

The remaining fixes ride along without decision weight: approval-dialog
type-ahead grace, draft preservation across queued sends, shell
interrupt handback, tab expansion in the pinned renderer, view clamping
on short terminals, banner-borne startup warnings, rune-safe clipping,
live policy in /tools, resolved-path
trusted_projects matching, file_info parent resolution, MCP stdin
generation guard and name-collision disambiguation, retry chunk
counting, web metadata nil guards, restored-history compaction
estimate, read_file/edit_file line-count off-by-ones, near-miss
diagnosis alignment, and the reachable dependency vulnerabilities.

## Consequences

- Sessions using `/clear` resume correctly; crashed sessions lose at
  most the torn line and say so; concurrent resume is refused.
- 'a' means "stop asking about the routine cases", never "stop asking
  about the dangerous ones" — and an explicit policy always wins.
- A project can genuinely tighten past any global wildcard/exact mix.
- Transcript schema is now 2; older builds refuse newer files by name.

## References

- ADR-0004/0008 (the floor and policy semantics this restores)
- ADR-0005/0006 (the resume/compaction invariants findings 1–4 protect)
- ADR-0007 (the queue semantics decision 7 narrows)
- ADR-0018 (measure, then believe — applied here to refute two findings)

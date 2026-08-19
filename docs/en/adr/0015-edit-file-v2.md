# ADR-0015: edit_file v2 — batched, diagnosed, self-verifying

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: editing needs extension — whole-file writes are wasteful for partial changes, but line-number addressing "would be bad; think of a good approach" |

## Context

`edit_file` is already an exact-string replacement with a uniqueness
contract — the right anchor, and the operator's instinct about the
alternative is correct: line-number addressing is fragile in exactly the
ways that matter here (numbers drift with every edit, models miscount,
and a stale number writes to the wrong place *silently*). String
anchoring fails loudly or works; that property is worth keeping.

What makes editing expensive today is not the anchor — it is the rounds
around it:

1. **One edit per call.** A change touching five places is five rounds,
   each replaying the entire history (ADR-0014's cost, again).
2. **No bulk replacement.** A rename forces many calls — or a
   `write_file` full rewrite, which is the waste the operator named.
3. **Failure is undiagnosed.** "old_string not found" sends the model
   back to re-read the file, often wholesale, to discover its
   old_string differed by a tab.
4. **Success is unevidenced.** "edited <path>" gives a careful model no
   choice but to re-read and verify.

## Decision

Keep the anchor, fix the economics. `edit_file` gains, backward
compatibly:

1. **Batched edits: `edits: [{old_string, new_string, replace_all?}]`**,
   applied **sequentially** (each edit sees its predecessors' output —
   stated in the description, because it is the semantic people trip on)
   and **atomically**: everything is applied to an in-memory copy and
   written once, or nothing is written and the error names which edit
   failed and why. A half-applied batch would leave a file no one has an
   accurate picture of. The single `old_string`/`new_string` form stays
   as the one-edit case.
2. **`replace_all`** per edit — the rename case. The uniqueness contract
   stays the default; replace_all is the explicit opt-out, and its
   report says how many replacements it made.
3. **Misses are diagnosed, not just refused.** On "not found", the file
   is searched again with whitespace normalized (indentation and
   run-of-space differences — the way models actually miss). A near
   match is reported with its line number and its *actual* text, so the
   correction is a copy-paste instead of a re-read. On "appears N
   times", the occurrences' line numbers are listed, so making the
   anchor unique needs a window read at most, not a full one.
4. **Success returns evidence: the changed region** — the new text with
   two lines of context and its line span. The span and headers live in
   the note, never as per-line prefixes on content (the ADR-0014 rule:
   numbered content poisons the exact-match contract the moment it is
   copied). With the evidence in the result, the read-back verification
   round disappears; the intended loop is windowed read → batched edit
   → verify from the result.

## Consequences

- The partial-change waste the operator named is gone end to end: a
  window read in, one batched call out, verification included. Nothing
  reads or writes the whole file unless the whole file is the point.
- Sequential batch semantics can surprise: an edit whose old_string was
  itself changed by an earlier edit in the same batch will miss. The
  atomic failure plus the near-miss diagnosis make this loud and
  recoverable; the description warns about it.
- The near-miss search is heuristic. It only ever affects error
  *messages* — application still requires the exact unique match, so a
  wrong guess costs nothing but a less useful hint.
- Result payloads grow by the snippet (capped). Cheaper than the
  re-read they replace, every time.

## Alternatives considered

- **Line-number or line-range editing** — rejected, with the operator:
  stale numbers write to the wrong place silently; the failure mode of
  string anchoring (loud miss) is strictly better than the failure mode
  of addressing (quiet corruption).
- **Unified-diff / patch input** — rejected: models emit malformed
  hunks and drifted context lines routinely; a patch parser then needs
  its own diagnosis machinery for a format nobody demanded. The edits
  array carries the same information with exact-match semantics.
- **Fuzzy application** (apply the near-miss automatically) — rejected:
  the whole value of the exact contract is that the tool never writes
  something the model did not literally specify. The near-miss is a
  hint, never an action.
- **A separate `multi_edit` tool** — rejected: same operation, second
  name (the ADR-0014 §read_lines argument again).

## References

- ADR-0014 (context economy; this closes the write half, and the
  no-line-number-prefix rule carries over)
- Claude Code's Edit tool (the exact-unique contract this keeps)

# ADR-0051: whole-file rewrites that shrink are a red flag — four floors against summarizing overwrites

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-27 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator field report: "While the project's SOP documents are small the agent performs well, but once they grow, asking it to revise the SOP itself tends to destroy the existing content by aggressively summarizing it. Model characteristics aside, aren't there other factors producing this symptom?" — followed by "Let's put in all of them" on the four-part diagnosis. |

## Context

The symptom: ask the agent to revise a large existing document and the
result is a shorter document — sections gone, prose compressed, the
revision technically applied to a corpse. The model's own tendency to
compress long verbatim reproduction is real, but the investigation
found four implementation-side factors that *manufacture* the
conditions for it, and one of them explains why the symptom scales
with document size:

1. **Context-economy pressure points away from full reads.** The
   system prompt and `read_file`'s description both push toward
   windowed reads and `summarize_file` ("everything you read is
   replayed on every later round"). Right for code navigation — but
   for document revision it steers the model into holding a partial
   or summarized copy, from which it then regenerates the whole file.

2. **`write_file` legitimizes whole-file regeneration and cannot see
   destruction.** Its description said "for small changes prefer
   edit_file" — a large revision is not a small change, so the model
   reasonably picks `write_file`, which forces it to reproduce the
   entire document as output tokens inside one function-call argument:
   exactly the shape where long reproduction drifts and compresses.
   Replacing 42KB with 8KB reported `wrote 8192 bytes` — success.

3. **Auto-compaction destroys the verbatim copy mid-task — the size
   correlation.** Compaction replaces everything but the trailing
   messages with a prose summary (ADR-0006, auto at 80% of the
   window). A large document (a) costs enough prompt tokens that
   reading it approaches the threshold and (b) takes enough rounds to
   revise that compaction fires *between the read and the write*. The
   model then writes the file from a summary of it. Small documents
   survive to the end of the task verbatim; large ones do not — the
   observed "gets worse as the SOP grows" is this mechanism.

4. **Silent truncation seeds bad copies.** `read_file` caps at 200KB;
   instruction files inject into the system prompt capped at 32KB per
   file. Both truncations are disclosed in-band, but a model that
   "already knows" the document from a truncated copy has a truncated
   document to write back.

No single fix addresses all four, and the fixes reinforce each other
(a guard that fires teaches the rule the prompt states; the prompt
rule explains the guard the model hits), so they ship together as one
ADR (org lesson: co-reinforcing changes ship together).

## Decision

Four floors, from deterministic to advisory:

### 1. The shrink guard (deterministic, tools layer)

`write_file` refuses to overwrite an existing file of
**2048 bytes or more** with content **smaller than 70% of its
current size**, unless the call passes `allow_shrink: true`:

    refusing to replace docs/sop.md (42KB) with much smaller content
    (8KB): a whole-file rewrite destroys everything not reproduced
    verbatim. Use edit_file for targeted changes, or re-read the file
    and pass allow_shrink=true if this shrink is intentional
    (file unchanged)

The error is instructive in the `edit_file` near-miss tradition: it
names both sizes, both remedies, and the fact that nothing was
written. `allow_shrink` is **declared intent** (org lesson: make the
model declare, don't infer) — the declaration is an argument, so it
appears verbatim on the approval dialog and in the transcript's
`gate_decision` record. A destroyed document becomes impossible to
produce *silently*: it now requires either targeted edits or an
explicit, recorded claim that the shrink is deliberate.

The guard runs inside the tool (after the approval gate): the tools
layer owns filesystem knowledge, and an approve-then-refuse costs one
visible round whose retry carries the declaration through a fresh
gate. In auto-approve mode — where project-internal writes raise no
dialog at all — this floor is the load-bearing one.

The thresholds are constants, not config. A config knob for a safety
floor is an invitation to turn it off without reading why it exists;
revisit on false-positive evidence, in an ADR.

### 2. The working-style rule (prompt)

The system prompt's `edit_file` preference line becomes a rule about
regeneration:

> Prefer edit_file for changes to existing files, even large
> revisions; write_file is for new files. Overwriting an existing
> file regenerates ALL of it from your context — never do that
> unless you have read the whole file in this conversation after any
> compaction; everything you do not reproduce verbatim is destroyed.

This names the failure mode in the same breath as the economy
guidance that feeds it (factor 1): windowed reads for navigation,
full reads before whole-file writes.

### 3. The compaction staleness notice (deterministic, trusted framing)

`SummaryMessage` — the message that stands in for compacted history —
gains one sentence in its **Content** (the trusted framing gem-agent
writes), not in the attached summary (untrusted model output):

> File contents shown before this point are no longer verbatim in
> context: re-read a file before editing it or quoting it exactly,
> and never rewrite an existing file from this summary alone.

Deterministic by construction — it does not depend on the summarizer
following instructions, and it lands at exactly the moment factor 3
strikes. Recorded compactions replay it on resume because the message
is transcript-carried like any other.

### 4. The size delta on the approval dialog (display)

`tools.Tool` gains an optional display-only `Annotate` hook: extra
detail lines derived from live filesystem state, appended to the
call detail the operator sees. `write_file` implements it: when the
target exists, the dialog (and the `gate_decision` transcript record)
carries

    replaces existing file: 42KB → 8KB

The operator's floor for the manual-approval path — a shrinking
overwrite is now visible at the moment of consent instead of after
it. The annotation is a log-shaped factual note and stays English
(ADR-0029's out-of-scope face). Annotate must not mutate anything;
it is consulted where `Describe` composes the detail.

## Consequences

- A legitimate large shrink (intentional replacement, generated
  file) costs one extra round: refusal → retry with `allow_shrink`.
  That round *is* the feature — the intent lands in the transcript.
- Tests and scripts that legitimately shrink files ≥2KB must pass
  `allow_shrink: true`.
- The prompt change rotates the session prefix (one cold cache per
  upgrade, as with any prompt change).
- Factor 4's truncation caps are unchanged — the guard catches the
  destructive *outcome* (a truncated copy written back is a shrink)
  rather than policing every path that could seed one.

## Rejected alternatives

- **Require a prior full read, tracked by the registry.** The
  dangerous case is precisely "read happened, then compaction
  discarded it" — the tools layer cannot see compaction, so the
  tracking would certify exactly the copies that are stale. A check
  that mostly passes is worse than a rule the model must satisfy.
- **Full diff preview on the approval dialog.** A whole-document
  diff in a terminal dialog is not readable review (the ADR-0050
  lesson: mandatory review must be *readable* to be review). The
  size delta is the honest signal a human can actually evaluate at
  a glance; real diff review belongs to git.
- **Blocking `write_file` on existing files entirely.** Wholesale
  regeneration is sometimes correct (generated artifacts, deliberate
  replacement), and `edit_file`'s exact-match contract is the wrong
  tool for it. The guard prices the operation; it does not ban it.
- **A config threshold for the guard.** See §1 — safety floors do
  not get knobs.

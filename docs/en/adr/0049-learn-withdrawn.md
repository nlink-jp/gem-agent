# ADR-0049: `/learn` is withdrawn — confirmation was not a durable boundary for loosening

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-26 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, after field-testing v0.47.0: "far more ends up permitted than I expected — I judge this dangerous. We went to the trouble of implementing it, but `/learn` should be reconsidered from the root." |

## Context

`/learn` was field-tested twice and failed twice, in opposite
directions. On v0.46.0 a session of 25 approvals produced zero
proposals (ADR-0048 diagnosed why). On v0.47.0, with both fixes in,
the operator's verdict was that too much ends up permitted. Between
the two runs the thresholds were tuned once in each direction — and a
control surface that fails on both sides of one adjustment is the
wrong control surface, not a miscalibrated one.

### Why it over-permitted

The design's load-bearing safety argument was ADR-0048 §3:
*"the operator confirms each rule with the full covered-tool list in
front of them."* In the field that confirmation did not carry the
weight placed on it, for four reasons that compound:

1. **Consent was one keystroke, offered at the worst moment.**
   Proposals arrive right after a session of approving things — the
   operator is primed to keep answering yes, and `1` accepts a
   standing permission as cheaply as `y` approved a single call.
   The gate's own lesson (one `a` must not stand for many decisions)
   applies to the confirmation step itself, and the design did not
   apply it there.
2. **The bundle mixed risk levels.** A server wildcard covers
   read-only lookups and mutating tools in one yes/no. The disclosure
   listed them — honestly — but a bundled question invites a bundled
   answer, and nobody re-reads eight tool descriptions after a long
   session.
3. **Momentary evidence bought a permanent, context-free grant.**
   Five approvals during one afternoon's investigation are decisions
   *about that investigation*. The rule they became is global and
   forever. Nothing in the mechanism made the grant as narrow — in
   time or in scope — as the evidence.
4. **Accepted rules had no management surface.** `/settings` fed
   learned command rules into the live policy but never displayed
   them; the reference docs claimed otherwise. A standing permission
   that is invisible after the moment of consent cannot be
   reconsidered, which turns one primed keystroke into policy nobody
   reviews. This defect is recorded here rather than fixed, because
   the feature it belongs to is being withdrawn.

The deeper error: ADR-0008's asymmetry says loosening must be an
explicit operator act — and its cost was part of the design. Writing
a policy line by hand is deliberate *because it is manual*. `/learn`
set out to remove exactly that friction, and succeeded: it made
loosening cheap. The operator has now judged the resulting
equilibrium dangerous. The feature worked as designed; the design
moved a boundary that the cost structure was holding in place.

## Decision

### 1. The command and the proposal engine are removed

`/learn`, `internal/learn`, and all proposal UI are gone — not
disabled, removed; git history keeps the code for the redesign. A
half-alive feature ("present but neutered") would keep its docs, its
strings, and its ambiguity.

### 2. The records stay

`gate_decision` and `auto_decision` records — with the aggregation
key and the `source` distinction (ADR-0048 §1) — continue to be
written, and `Approver.Approve` keeps its two-value return. The data
outlives the feature: any future design needs exactly this record,
it is diagnostic-only, and it costs nothing. The per-command policy
vocabulary (`policy.CommandKey`, `ForCall`, the `commands` tables in
the policy file) also remains in code: existing policy files must
keep parsing, and the gate machinery is sound — what failed was the
granting pipeline, not the vocabulary.

### 3. Learned command rules stop being applied

`[projects."…".commands]` tables were written only by `/learn` and
are invisible in every management surface, so leaving them silently
active while the feature is reconsidered is the worst state. They
are no longer fed into the policy; a startup note reports how many
entries were found and where they live, so the operator deletes them
or leaves them for a future version. Ignoring recorded policy is
acceptable here because the direction is tightening — every ignored
rule means more asking, never less — and because the operator who
confirmed them is the one who judged the result dangerous.

Global `[tools]` entries `/learn` wrote (`mcp__<server>__*`) are
byte-identical to hand-written ADR-0008 policy and cannot be told
apart, so they remain in force. The operator is advised to review
them; the release notes carry the exact lines to look for.

## Open questions for the redesign

Recorded as questions, not decisions:

- **Observability instead of granting?** The friction report —
  what you approved repeatedly, where the model tier wobbles — has
  value with zero risk. The successor could show the evidence and
  stop, leaving the writing of any rule manual: the cost of writing
  it *is* the deliberateness.
- **Session-scoped relief instead of standing policy?** The reported
  friction was an investigation chain crossing many tools of a few
  servers. A per-server session allowlist (an `a` that covers the
  server until exit) would relieve exactly that, and persist nothing.
- **If granting ever returns: enumerate, never wildcard.** A rule
  listing exactly the evidenced tools covers nothing unused; each
  new tool earns its own entry.
- **Whether any of this is still needed.** ADR-0046 already reduced
  the model tier's wobble at the source. Measure the residual
  friction before building anything.

## Consequences

- The friction `/learn` addressed returns in full until a successor
  is designed. That is the accepted cost of withdrawing a loosening
  mechanism the operator judged unsafe.
- ADR-0045 and ADR-0048 stand as the record of the design and its
  field failures; their INDEX entries note the withdrawal.
- Lesson, recorded for anything that grants standing permissions:
  per-item confirmation with full disclosure is necessary and NOT
  sufficient. A grant should be as narrow as its evidence, visible
  after the moment of consent, and no cheaper to accept than the
  risk it retires — and a threshold tuned twice in opposite
  directions is telling you the knob is on the wrong machine.

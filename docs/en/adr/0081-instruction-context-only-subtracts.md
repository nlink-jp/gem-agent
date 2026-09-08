# ADR-0081: instruction context may only ever subtract, and by composition rather than by asking

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-09) — **implemented and unreleased**. §1's `safe` bullet was corrected against the code during implementation |
| Date | 2026-09-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, while designing [ADR-0080](0080-read-only-is-a-mode.md): are injection defence, continuity of intent under `--auto`, and following a mid-session change of mind jointly satisfiable at all — and is a judge whose verdict moves with its context calibratable? |
| Relates to | ADR-0004 (the ladder), ADR-0020 §4 (the proposer cannot be its own judge), ADR-0038 (the instruction as evidence inside the isolation wrap), ADR-0047 §3 (the proposer's self-justification is stripped), ADR-0050 (the rulebook is guidance), ADR-0054 (the instruction reaches every round), ADR-0080 (which states the session mode to the evaluator and is bounded by this) |
| Split from | [ADR-0080](0080-read-only-is-a-mode.md), where this was first drafted. ADR-0080 no longer depends on it: the mode is state, so nothing has to be carried and no window widens. This stands on its own |

## Context

The evaluator's instruction context is **bidirectional today**.
ADR-0038 states that alignment with the operator's request supports
approval, and it was measured doing exactly that on 2026-09-09: a
`make build` at a later round, with the turn's input being 「続けて」,
was approved at confidence 0.95 — "Standard build command within
project directory".

The consequence is that anything reaching the evaluator through that
channel can move a verdict *toward* approval. The channel is
operator-typed text, which an injection attacker cannot write into
directly — but an operator who pastes a log, an error message or a file
excerpt into their own prompt has carried attacker-controlled bytes
into the one input this system trusts. At one turn's width that is
already true; it is not a hazard introduced by any proposal.

ADR-0080 §5 adds one more thing to that payload — the session mode —
and measured that naming the sandbox did not invite the evaluator to
treat the cage as a licence to approve. One measurement is evidence, not
a property. What follows is the property.

## Decision

### 1. The verdict is a composition of two decisions

```
approve = (the decision reached WITHOUT instruction context)
          AND
          (the evaluation given the instruction context)
```

- **Safe tier.** Nothing to compose, and no round at all. *Corrected
  against the code during implementation:* this bullet said the aligned
  round runs for `safe` calls, which came from ADR-0080's second draft,
  where in-project writes had to reach the evaluator because prose was
  the only channel a restriction had. ADR-0080 as accepted answers that
  with the lane ceiling instead, so routing `safe` calls through an
  aligned round would now buy nothing and cost a model round each. The
  guarantee is unaffected: instruction context never reaches those
  calls, so it cannot create an approval there.
- **Review tier.** The baseline is a model round with no instruction
  context — the evaluation this system made before ADR-0038. If it
  escalates, the call escalates and the aligned round is not run:
  context may not rescue anything, so there is nothing to ask. One
  round in that case, two when the baseline approves.

The baseline is context-free, so within a session it is cacheable by
tool name and arguments.

### 2. What the composition guarantees

Anything reaching the evaluator through the context can only **remove**
approvals. Attacker bytes that survive into the operator's prompt can
cause extra escalations — a denial of convenience — and cannot cause an
approval the context-free decision would not have made. The floor is
exactly the pre-ADR-0038 evaluator.

This also bounds the cage-as-licence reading ADR-0080 §5 looked for:
under the composition, no statement in the context can produce an
approval on its own, whatever the model concludes from it.

### 3. Why not instruct the prompt instead

"Treat alignment as grounds to escalate, never to approve" is a string
rule, and the property has to hold against a model that does not follow
it. A composition holds because of how two answers are combined, not
because of what either was asked — the same reason ADR-0020 §4 removed
memory writes from the model tier rather than telling it to be careful.

### 4. A change of mind is unaffected

「じゃあ直して」 approves because the baseline approves and the aligned
view no longer objects. Lifting a restriction never requires the
context to *create* an approval, so the subtractive rule costs nothing
where following the operator matters most.

## Alternatives considered

- **One round returning two self-reported fields** (a baseline verdict
  and an alignment verdict). Rejected: the same context influenced
  both, so the decomposition is the model's claim about itself rather
  than a property of the composition.
- **Running the baseline for `safe` calls too.** Unnecessary: the rule
  tier already is the context-free decision there, so a second opinion
  re-derives it at the cost of a round.
- **Removing instruction context altogether.** Rejected: it is what
  makes a contradiction visible at all — measured escalating a
  forbidden build at rounds 1 and 5 — and ADR-0080 §5 depends on the
  channel existing.

## Consequences

- A Review-tier call the baseline approves costs two evaluator rounds
  instead of one. `safe` calls are unchanged in count.
- A verdict that used to be carried to approval by an aligned
  instruction now also has to pass the context-free decision, so some
  calls that ran will escalate. CHANGELOG under *Changed*.
- **Not measured.** No probe has yet constructed the case this exists
  to stop — attacker text inside the operator's own prompt moving a
  verdict from escalate to approve. The composition makes it
  impossible by construction rather than unlikely by measurement,
  which is the point, but the cost half of the trade deserves numbers
  before this ships.

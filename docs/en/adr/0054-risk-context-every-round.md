# ADR-0054: the risk evaluator sees the operator's instruction in every round

| Field | Value |
|-------|-------|
| Status | **Accepted** — amends [ADR-0038](0038-risk-eval-instruction-context.md) §3 (the round cutoff is removed) |
| Date | 2026-08-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a headless pipeline's terminal send was evaluated without the instruction in view; the deny itself was right (the egress rubric), but "does `< 3` match reality?" deserved re-examination |

## Context

ADR-0038 gave the auto-approve model tier the operator's typed
request as alignment evidence — **for the first 3 rounds of a turn
only**. The cutoff was operator direction at the time, set by
intuition, with no measurement behind the number: deep-turn calls
were expected to serve sub-goals the instruction never names, and
prompting a judge to tolerate "indirect relation" looked like soft
engineering around a hard problem.

Measured against every real transcript in the state directory
(55 files, 171 model-consulted decisions across 35 turns,
2026-08-18 → 08-29):

- **70% of model-tier evaluations happen outside the window**
  (119/171); rounds 8–48 alone hold 49%. The window covers a
  minority of the decisions it was designed to inform.
- **63% of turns place their *final* gated call outside the window**
  (22/35) — terminal actions, the calls whose alignment matters
  most, are the least likely to be evaluated with the instruction
  in view.
- **Every one of the 26 beyond-window escalations was a
  network-category call** (web_fetch ×19, read-only lookup MCPs ×5,
  web_search, one Slack send). Where gate records exist
  (post-v0.46), the operator then approved 3/3 by hand — and
  ADR-0048 §1 records the same shape at scale: 25 escalations,
  all approved. Beyond-window escalations are, in practice, false
  alarms on read-only research calls.
- **The instruction does not reliably rescue network calls even
  in-window**: whois-lookup approved 2 / escalated 7, doh-lookup
  1 / 4 with the instruction in full view. The rubric's "reach the
  network" line dominates and the verdict wobbles. So this
  amendment fixes *reach*, not the lookup friction — that belongs
  to a standing ADR-0008 `"never"` policy or an ADR-0050 rulebook
  line, and the ADR-0053 egress stance is untouched.

The structural fact ADR-0038 §1 rests on has no round dependence:
**the typed input is the one context channel an injection attacker
cannot write** — at round 0 and at round 40 alike. A call steered by
poisoned tool output at round 20 is *more* detectable with the
instruction present (the contradiction is visible), not less.

## Decision

### 1. The instruction rides on every model-tier evaluation

`riskContextRounds` is removed. Whenever the turn has a typed
instruction (`Run` rejects empty input, so it always does), the
risk-evaluation payload carries it, clipped as before. Every other
part of ADR-0038 stands unchanged: the strict §1 scope (typed input
only — never history, tool results, intent narration, or attachment
bytes), the §2 evidence framing inside the nonce wrap, the 2000-rune
clip, and the addendum text — whose final sentence ("an indirect
relation is normal in a multi-step task and is not by itself a
reason to escalate") now carries the load the cutoff used to carry.

### 2. Uniformity replaces the fallback

ADR-0038 §3 valued a byte-identical fallback: no regression where
the context does not apply. With the cutoff gone the uniform case is
the *with-context* evaluation — one prompt shape per turn instead of
two, and the round number no longer changes how the same call is
judged. The no-instruction branch survives in code as a guard, not
as a mode.

### 3. What this deliberately does not change

- The **egress rubric line**: sends still escalate with the
  instruction in view (measured in-window: 0/2 approved). Opening
  egress remains the operator's explicit act — ADR-0008 policy or
  `--allow` (ADR-0053).
- Safe and Block tiers, the confidence bar, fail-closed on every
  uncertain path.
- The round-limit review (ADR-0040) keeps its own use of the round
  counter.

## Alternatives considered

- **Raise the constant** (5? 8?) — any constant mismatches the
  measured tail: rounds 8–48 hold half the volume. A number that
  "matches reality" is no cutoff at all.
- **One-shot only** (no cutoff in `-p`, keep 3 interactively) — the
  measured friction (the web_fetch chains, the lookup escalations)
  was interactive. A mode split fixes the unmeasured case and keeps
  the measured one.
- **Semantic drift detection** (include while the call "still
  relates") — the judge judging its own context eligibility is the
  soft engineering ADR-0038 §3 refused, squared.

## Consequences

- Terminal actions and deep-turn research calls are evaluated with
  the alignment evidence present; the contradiction-detection
  benefit (ADR-0038's live-measured case) now applies at every
  round.
- A few hundred extra payload tokens on the risk calls that
  previously ran bare — side-call accounting only (ADR-0019).
- The lookup-friction numbers above are the baseline for judging
  the follow-up (policy or rulebook) separately.

## References

- ADR-0038 (instruction context; §3 amended here)
- ADR-0048 §1 (25 escalations, all approved — the friction shape)
- ADR-0050 (rulebook: the calibration layer for the wobble)
- ADR-0053 (one-shot approval controls; egress stance)
- Measurement script: session-transcript round reconstruction,
  2026-08-29 (numbers quoted above)

# ADR-0025: Configurable thinking level

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a thinking-level setting would be good to have |

## Context

Gemini 3 exposes a per-request thinking level (MINIMAL / LOW / MEDIUM /
HIGH); gem-agent always ran the model's default. The operator pays for
thinking tokens on every round (the footer and /usage already show
them), and different work wants different depth — a quick lookup does
not need HIGH, a refactor might.

## Decision

1. **`[model] thinking = "minimal" | "low" | "medium" | "high"`**,
   empty/unset meaning the model's own default. An unknown value is a
   startup error, not a silent fallback — the strict-config principle.
2. **The level applies to every main-model `ChatStream` call** — the
   conversation loop, and the risk/compaction side-calls that ride the
   same backend. The summary model (ADR-0014) keeps its own default:
   `WithModel` deliberately does not inherit the level, and the
   grounded-search / URL-context side-calls are untouched.
3. **/settings displays the value read-only with its source**, like the
   model name: changing it means a config edit and restart. Live
   editing was considered and deferred — it would add the first mutable
   field to the shared Vertex client for a setting that changes rarely.
4. Verification is by measurement — done: one arithmetic prompt on
   gemini-3.7-flash spent 93 (low) / 170 (medium) / 222 (high) thought
   tokens, and "minimal" was rejected by that model with a clear 400
   ("Thinking level is unsupported") on the first turn. Which levels a
   model accepts is model-dependent; the config accepts all four SDK
   values and lets the API be the authority, because the failure is
   loud, immediate, and names the level.

## Consequences

- One config key; thought-token spend becomes steerable.
- ThinkingBudget (the 2.5-era token knob) is deliberately not exposed:
  gem-agent binds to Gemini 3.x, and two knobs for one dial invite
  conflicting configuration.

## References

- ADR-0009 (read-only settings rows carry their reason)
- ADR-0014 (the summary model this deliberately does not affect)
- ADR-0019 (the usage accounting that makes the effect visible)

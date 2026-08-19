# ADR-0018: Context caching — a cache-friendly loop, measured

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "shouldn't we support context caching, to economise on context?" |

## Context

First, the honest scoping: caching economises **cost and latency**, not
window occupancy — the same prefix is still in the window; it is just
billed at the cached rate and not re-processed. Window pressure remains
compaction's job (ADR-0006). What caching attacks is the bill for the
agent loop's defining behaviour: every round re-sends the entire
history, and ADR-0014 measured how everything read is "replayed on every
later round". Caching is the discount on exactly that replay.

Vertex's mechanism for this request shape is **implicit caching**:
requests sharing a byte-identical prefix get cached-token pricing
automatically, no cache objects, no TTLs, no storage fees. Our loop is
almost the ideal customer — system instruction plus append-only
history — except for one design that defeats it completely:

**The isolation tag is regenerated on every LLM call.** The nonce lands
in the system instruction (`{{DATA_TAG}}`) and in every wrapped tool
result, so between round N and round N+1 the request differs from the
first byte of the system instruction onward. Implicit caching can never
match a prefix. Our injection defense is maximally cache-hostile.

## Decision

1. **The main loop's isolation tag becomes session-scoped.** Created
   with the agent, regenerated on `/clear` and on resume. With the tag
   stable, the system instruction and all previously wrapped history are
   byte-identical across rounds *and turns*, and the growing request is
   a pure append — the shape implicit caching rewards.
2. **Why this does not weaken the isolation.** The per-call nonce
   guarded against an attacker *reusing a known tag* to fabricate
   wrapper boundaries. That guard has a stronger, mechanism-level form
   which nlk's `Wrap` already implements: content containing the tag
   name — opening, closing, or bare — is **refused and withheld**
   (`ErrTagCollision`), so knowing the tag is useless for escaping it.
   What per-session reuse actually changes: a tag echoed by the model
   into some attacker-readable place stays valid for the session instead
   of one call, so an attacker can plant it in content to get that
   content *withheld* — an availability nuisance (and a loud one: the
   withholding is visible), not an integrity break. The 128-bit nonce
   stays unguessable either way.
3. **Side-calls keep per-call tags.** Risk evaluation (ADR-0004),
   compaction (ADR-0006), summarize_file (ADR-0014) are one-shot calls
   with no prefix to reuse — no cache benefit exists, so no reason to
   move off the stricter default.
4. **Measure, then believe.** The usage pipeline now carries
   `cachedContentTokenCount` end to end: captured from the API, written
   to the session log's usage records, shown in the footer as the cached
   share of the last round's prompt. Whether implicit caching actually
   fires for this model, at this prefix size, on this endpoint is a
   claim the counter proves or disproves — the runbook lesson about
   checks that look like passes applies to optimisations too.
5. **Explicit caching (CachedContent API) is deliberately not built.**
   It bills storage per hour, wants TTL management, and fits a fixed
   large prefix reused across sessions — while our prefix grows every
   round, which is churn, not reuse. It becomes worth revisiting only if
   the counter shows implicit caching failing us.

## Consequences

- The replay tax on long tool loops drops to cached pricing for the
  shared prefix — precisely where gem-agent's cost lives. Latency
  improves with it.
- Compaction and `/clear` reset the prefix and thus the cache: expected,
  visible in the counter, and still net-positive (compaction shrinks
  what needs caching at all).
- The isolation contract's documentation changes from "fresh tag per
  call, always" to "fresh per call for side-calls; per session for the
  main loop, backed by Wrap's collision refusal" — this ADR is that
  documentation, and the org memory carries the nuance.

## Alternatives considered

- **Keep per-call tags, accept zero caching** — the status quo; rejected
  because the security delta is an availability nuisance (see §2) while
  the cost delta is the majority of every long session's bill.
- **Explicit CachedContent objects** — rejected for now (§5).
- **Tag rotation every N rounds** — rejected: it buys nothing the
  collision refusal does not already provide, and every rotation throws
  the cache away; it is the worst point on both curves.

## References

- ADR-0001 (isolation; amended in mechanism emphasis, not in stance)
- ADR-0006/0014 (what actually saves window space; the replay cost this
  discounts)
- nlk/guard `ErrTagCollision` (the mechanism that makes session scope
  sound)

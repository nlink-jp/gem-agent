# ADR-0057: Every model call leaves an accounting record

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-30 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "can a session's cost be computed from the transcript at catalog prices — or does the API report cost per request?" |
| Amends | ADR-0019 (side-call accounting), ADR-0005 (transcript records), ADR-0035 §1 (audit events) |

## Context

The API never reports money. Vertex returns `usageMetadata` token
counts and nothing else; Cloud Billing reports cost per SKU per day,
which cannot be attributed to a session, a turn, or a call. So the only
route to "what did this session cost" is **token counts × catalog
price**, and that requires the counts to be on disk at the moment of
the call — a process that exits takes its in-memory tallies with it.

Measured across the 63 transcripts on this machine (747 main-loop
rounds): prompt 78.5M of which 64.4M cached, output 258k, thoughts 92k.
Three holes in that record:

1. **Risk evaluations and compaction wrote nothing.** They update
   `Stats` for `/usage` and vanish at exit. The transcripts hold 309
   `auto_decision` lines with `tier: "review"` — each one a model call
   whose tokens no longer exist anywhere.
2. **Side-call records carry `prompt` and `output` only**
   (`summary_usage`, `web_search`, `web_fetch`,
   `agentic_search_usage`). Thinking tokens bill as output and cached
   prompt tokens bill at a discount, so a record without them cannot
   be priced — it can only be guessed at.
3. **Nothing says which model spent them.** The header names the main
   model; a summariser or fetch model billing at a different rate is
   named only in prose, per record kind.

Two measurements fix the arithmetic itself. `prompt + candidates +
thoughts = total` (25 + 174 + 534 = 733, live): thinking tokens are a
**separate bucket**, not part of `candidates`, and they bill as output.
And `cached ⊆ prompt` (no counterexample in 183 rounds): the cached
count is a discounted *share* of the prompt, not an addition to it. An
aggregator that gets either wrong is wrong by multiples — cache hits
are 82% of all prompt tokens here.

## Decision

### 1. One record kind, one shape, one per model call

Every model call in the process — main loop, risk evaluation, progress
review, compaction, `summarize_file`, `web_search`, `web_fetch`, the
file-search child — writes exactly one `usage` record:

    {"kind":"usage","data":{"source":"risk","model":"…","prompt":4183,
     "output":42,"thoughts":81,"cached":0,"total":4306}}

`source` is the only new dimension the aggregation needs, and `model`
makes the record self-priceable without joining the header.

### 2. `total` is the API's own number, kept as a checksum

Not derived — read from `usageMetadata.totalTokenCount`. An aggregator
that forgets that thoughts are billed separately fails
`prompt + output + thoughts == total` loudly, instead of undercounting
quietly.

### 3. The descriptive records lose their token fields

`web_search` keeps its query and source count, `summary_usage` its
path, `web_fetch` its URL and status, `agentic_search_usage` its
question and round count — the tokens live in the `usage` record
beside them. Two places to count is a double-counting bug waiting for
its first aggregator.

### 4. The header records the region

Pricing is resolved per SKU per region, so `location` joins schema,
version, model and project in the session header.

### 5. The audit stream matches

`model.usage` (ADR-0035) gains `thought_tokens` and `total_tokens`, so
a fleet-wide figure from Cloud Logging can use the same arithmetic as
a local transcript. Still metadata only — no prompt, no content.

### Not in scope

The price table and the cost report. This ADR buys the *possibility*
of computing cost later; what it deliberately does not do is bake a
price list into a tool whose prices churn.

## Consequences

- A transcript is now a complete accounting document for its own
  session: sum by `source`, price by `model`, check against `total`.
- Old transcripts stay readable and stay incomplete. A `usage` record
  without `source` is a pre-0057 main-loop round: an aggregator may
  count it, but must report the file as partial — its risk and
  compaction spend was never written.
- Not a schema bump. Usage records are diagnostic; `Load` ignores them
  and resume is unaffected.
- Transcript growth is a few hundred bytes per model call, against
  transcripts that already carry every file the agent read.

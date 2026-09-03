# ADR-0066: The fourth bucket — tool-use prompt tokens in the usage record

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-03 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Issue #1 (operator, via gem-usage-lens `verify`): two `web_search` / `web_fetch` records on a 2026-09-01 transcript fail the ADR-0057 checksum |
| Amends | ADR-0057 §1 (record shape), §2 (the checksum), §5 (audit stream) |

## Context

ADR-0057 §2 stated the arithmetic every aggregator is to check:
`prompt + output + thoughts == total`, measured live on the main loop
(25 + 174 + 534 = 733). The measurement was right and the equation was
wrong, because the probe was not representative: the main loop sends
function declarations and never a provider tool, so it never exercises
the bucket the equation omits.

The SDK's own definition of `totalTokenCount` has **four** addends
(`genai.GenerateContentResponseUsageMetadata`, v1.54.0):

> The total number of tokens for the entire request. This is the sum of
> `prompt_token_count`, `candidates_token_count`,
> `tool_use_prompt_token_count`, and `thoughts_token_count`.

`toolUsePromptTokenCount` is "the number of tokens in the results from
tool executions, which are provided back to the model as input" — the
output of the provider's **built-in** tools (Google Search grounding,
URL context) that the model reads before answering. It bills at the
input rate and is not part of `promptTokenCount`, so `cached ⊆ prompt`
still holds and the bucket is never cached.

Exactly two call sites in gem-agent enable a built-in tool: `web_search`
(grounding) and `web_fetch` (URL context), the ADR-0017 side calls. Both
read their spend through `sideUsage`, which copies four buckets and
`total`. Whenever the tool returned content, the record's `total` is
therefore larger than the sum of its parts — and the checksum ADR-0057
installed to catch *undercounting* aggregators reports the record
itself as broken.

Two facts about how it surfaced:

- The five web records on the author's machine all balance (the bucket
  was zero: search calls whose grounding added nothing to the count).
  The two on the reporter's did not. A probe of n=5 with an empty
  bucket said "no such bucket" — the same failure mode as ADR-0063's
  whitespace-free probe: a measurement that passes is not a
  measurement that covers the case.
- gem-usage-lens v0.1.1 derives the bucket as
  `total − (prompt + output + thoughts)` and bills it as input. The
  derivation is exact only while this is the **only** unrecorded
  bucket; the day a fifth one appears, the remainder is the sum of two
  unknowns and nothing in the transcript can tell them apart. The
  transcript is the accounting document (ADR-0057) — it should say
  what the API said, not leave a residual for the reader to name.

## Decision

### 1. The `usage` record carries `tool_prompt`

`llm.Usage` and `session.UsageRecord` gain `ToolPrompt`
(`"tool_prompt"`), populated from `ToolUsePromptTokenCount` on both
paths — the streaming accumulator (`Response.ToolPromptTokens`) and
the non-streaming `sideUsage`. Every call site that builds a record
goes through the two `logUsage` helpers, so no source is left out;
the bucket is structurally zero everywhere but the two web sources.

The key is written always, zero included, in the position before
`total` (addends before the checksum):

    {"kind":"usage","data":{"source":"web_fetch","model":"…","prompt":1200,
     "output":900,"thoughts":40,"cached":0,"tool_prompt":7000,"total":9140}}

Always-written, like `cached`: a **missing** key is a pre-0066 record
and a **zero** is a measured zero. `omitempty` would fold the two
cases into one and cost an aggregator the only signal it has for
"derive or trust".

### 2. The checksum is restated

    prompt + output + thoughts + tool_prompt == total

Pricing: `(prompt − cached) × input + cached × input × discount +
tool_prompt × input + (output + thoughts) × output`. For a record
without the key, an aggregator may derive `tool_prompt` as the
non-negative remainder; a negative remainder is still a broken record.

### 3. The audit stream matches

`model.usage` (ADR-0035, ADR-0057 §5) gains `tool_prompt_tokens`, so a
figure computed from Cloud Logging keeps using the same arithmetic as
one computed from a transcript. Still counts only.

### 4. What does not change

- **`/usage` and the exit receipt.** The in-memory statement shows
  prompt and output per category and already omits thoughts and
  cached for side calls; it is a glance, and the transcript is the
  accounting document. `UsageStats` is untouched — the main loop has no
  built-in tool, so the bucket is zero there by construction.
- **`Usage.Empty()`** keeps its three terms: a call that returned
  tool-result tokens and no prompt tokens does not exist.
- **Not a schema bump.** Usage records are diagnostic; `Load` ignores
  them and resume is unaffected (ADR-0057 consequences).
- **No price table.** ADR-0057's "not in scope" stands.

### 5. The evidence stays in the tree

A `-tags live` test issues one URL-context fetch and asserts the
four-term checksum with a non-zero fourth term, next to the existing
main-loop measurement. If a future SDK moves the bucket, that test is
what notices — not an aggregator three weeks later.

## Consequences

- `web_search` and `web_fetch` records balance again, and their spend
  is priceable from the record alone rather than from a residual.
- Pre-0066 transcripts keep their records; the lens's derivation
  becomes the legacy path, selected by the key's absence.
- One more integer per model call in the transcript and one more
  attribute per `model.usage` event.
- The unit test that pinned the three-term checksum now pins the
  four-term one with a non-zero fourth term, so the omission cannot
  return silently.

# ADR-0017: Web access — grounded search and digested fetch

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: web search and fetch equivalents are needed; search should use Gemini's Grounding with Google Search (plain search APIs barely exist); fetched content should go through the summariser rather than raw into the main model |

## Context

The fallback has no eyes on the web. For search, the operator's
constraint is also the org's history: plain web-search APIs are scarce
and their terms are hostile to agents — the org's own agentic-web-search
project was frozen over exactly that. **Grounding with Google Search**
is Vertex's first-party path: same client, same credentials, ToS-clean.

For fetch, the operator asked the right question ("何かよい手はあるか？")
and supplied half the answer: raw pages should not be poured into the
main model — extract and organise first. The other half is Vertex's
**URL Context tool**: the model fetches the URL *server-side* and reads
it in its own context window.

## Decision

Two tools, both side-calls in the established pattern (compaction,
summarize_file): an LLM call that offers no function tools and returns
one result string.

1. **`web_search(query)`** — a call on the **main model** with the
   GoogleSearch tool enabled and nothing else. The grounded answer
   returns with its **sources** (title, domain, URI) extracted from the
   grounding metadata, so a claim can be checked rather than believed.
   Search grounding and function calling do not mix in one request —
   which is fine, because the side-call architecture never mixes them.
2. **`web_fetch(url, focus?)`** — a call on the **digest model**
   (`[model].summary`, the lightweight slot) with the URL Context tool.
   The prompt asks for exactly what the operator specified: an organised
   extraction — what the page is, dense key points keeping exact names,
   numbers and dates, focus-weighted — not a transcript. Three layers of
   context economy stack here: the page bytes never enter the local
   process, never enter the digest model's *output*, and never enter the
   main conversation; only the digest does, and the saving repeats every
   round (ADR-0014's argument).
3. **Both tools are approval-gated by default (`Mutating: true`)**
   despite being read-only in effect, because the *request itself* is an
   egress channel: a query or a URL is a place where injected
   instructions could exfiltrate whatever the model can read. The
   operator relaxes this per tool with the ADR-0008 policy
   (`"web_search" = "never"`), which also makes the tools usable in
   one-shot mode — a deliberate, per-operator decision, exactly what
   ADR-0008 was built for.
4. **Web content is untrusted, and the isolation is honest about its
   layers.** The digest/answer returns as an ordinary tool result and
   gets the ordinary nonce wrap (no ADR-0010 exemption —
   derived-from-untrusted stays untrusted). Inside the side-call, the
   page cannot be nonce-wrapped (it is fetched server-side), so the
   fetch prompt carries the defensive framing — the ADR-0012 position,
   stated as the weaker layer it is.
5. **Server-side fetching is a security property, not a limitation.**
   The URL is retrieved by Google's infrastructure, not from this
   machine: localhost, RFC1918 and the operator's LAN are structurally
   unreachable — the SSRF class dies in the architecture. The cost is
   the mirror of the benefit: intranet and authenticated pages cannot be
   fetched, and the tool says so when retrieval fails (the URL Context
   metadata carries a per-URL status: error, paywall, unsafe).
6. Retrieval failures, blocked responses and empty answers are reported
   errors naming the reason — never a silent empty result.

## Consequences

- The fallback can research: search with checkable sources, fetch with
  organised extraction, both inside one agent loop.
- Egress-gated defaults mean two more approval prompts until the
  operator writes two policy lines. That is the right default for this
  operator's threat model, and the relaxation is one paste.
- Fetch quality is bounded by the digest model and the URL Context
  fetcher (no JS execution, size limits). For pages that defeat it, the
  failure is named; a local-fetch fallback would reopen SSRF and is
  deliberately not built.
- web_search runs on the main model (grounding quality is the point);
  web_fetch on the summary model (volume is the point). No new knobs.

## Alternatives considered

- **Third-party search APIs** — rejected: scarcity and ToS, per the
  operator and per the frozen agentic-web-search project.
- **Local HTTP fetch + readability extraction** — rejected: it reopens
  SSRF against the operator's own network, needs HTML parsing and JS
  answers it cannot give, and its output would still need the digest
  step. Server-side fetch plus digest does the job with none of it.
- **Returning raw fetched text with a size cap** — rejected by the
  operator in the request itself: extraction and organisation beat
  truncation (a cap keeps the first N bytes; a digest keeps the facts).
- **Enabling GoogleSearch/URLContext on the main agent loop directly** —
  rejected: grounding does not mix with function tools in a request, and
  even if it did, uncontrolled egress from every turn is the opposite of
  the gated default chosen here.

## References

- ADR-0006/0014 (the side-call pattern and the context-economy argument)
- ADR-0008 (the per-tool policy that makes the gated default liveable)
- ADR-0010/0012 (what gets wrapped, what gets framed, and why)
- agentic-web-search (lab-series; frozen over search API ToS — the
  history behind the grounding choice)

# ADR-0019: Usage accounting and /usage

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a /usage command — model utilization and cache consumption |

## Context

ADR-0018 promised "measure, then believe" and delivered one number in
the footer. The session's full spend is scattered: main-loop usage in
the log's `usage` records, summariser usage in `summary_usage`, web
calls tracked without token counts at all, and risk-evaluation tokens —
worst of all — **fed into the same footer callback as the main loop**,
so a risk check momentarily stomps the "context" gauge with its own
prompt size. The footer is a glance; the session's accounting deserves a
statement.

## Decision

1. **Per-category accounting, in memory.** The agent keeps a
   mutex-guarded `UsageStats`: main-loop rounds/prompt/output/thoughts/
   cached plus last-round context and the known window; separate buckets
   for risk evaluations and compaction. Side-calls **stop feeding the
   footer callback** — the ctx gauge now means the main conversation,
   always — and cmd keeps a tally for the tools that spend tokens on
   their own backends (summarize_file, web_search, web_fetch), whose LLM
   layer now returns usage alongside results.
2. **`/usage` renders the statement**: per category — calls/rounds,
   prompt (with cached share for the main loop), output, thoughts; the
   current context against the window; per-tool lines naming the model
   that spent the tokens. Plain text, works in the TUI and the plain
   REPL alike.
3. **The honest line is printed in the report**: cached tokens are
   billed at the reduced cache rate — caching saves cost and latency,
   not window space. /usage exists so the ADR-0018 measurement stays
   inspectable, not anecdotal.
4. In-memory, not log-parsing: the transcript stays the durable record,
   but a display command must not reread a session file that may hold
   megabytes of base64 images just to add integers.

## Consequences

- "What has this session cost, and is the cache working" is one command.
- The footer's ctx gauge stops lying during auto-approve bursts — a
  small correctness fix that fell out of the categorisation.
- Web tools' usage becomes visible for the first time (their LLM calls
  previously reported nothing).

## Alternatives considered

- **Parse the session log on demand** — rejected (§4).
- **Grow the footer** — rejected: one more permanent widget for data
  consulted occasionally; the footer keeps the glance, /usage keeps the
  statement.

## References

- ADR-0018 (the measurement this makes inspectable)
- ADR-0014/0017 (the side-call tools now accounted)

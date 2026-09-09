# ADR-0082: The model tier gets its own model slot

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-10) — **implemented and unreleased** |
| Date | 2026-09-10 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: could the risk evaluation run on macOS's on-device model, falling back to the cloud when its filters refuse? Measured — no — but the measurement showed where the latency actually comes from, and that a lighter *cloud* configuration wins it back without giving up the verdicts |
| Relates to | ADR-0004 (the ladder, and the "cheaper second model" it waved at and did not build), ADR-0014 §4 (the summary slot: model choice per call on a shared client), ADR-0040 (the progress review shares the tier's accounting), ADR-0057 (usage records name their model), ADR-0080 §2 (the read-only watcher's per-turn question is a model-tier call), ADR-0081 (an approved Review-tier call now costs two rounds — the tier's latency counts twice) |
| Amends | ADR-0025 §2: "the risk / compaction side-calls ride the same backend" now holds for the risk half only while the slot is unset. The non-inheritance rule ADR-0025 states for the summary model is extended to this slot; compaction is not moved |

## Context

The model tier — the risk evaluation of ADR-0004, two rounds per
approved Review-tier call since ADR-0081, and the progress review of
ADR-0040 — runs on the **main backend**: the main model at the main
thinking level. That was ADR-0025 §2's explicit choice ("the risk /
compaction side-calls ride the same backend"), and it was fine while the
main model answered a one-kilobyte JSON verdict in two seconds. The
operator's configuration today is `gemini-3.8-flash` at `thinking =
"high"`, so every Review-tier decision pays two rounds of that.

The session transcripts on the author's machine show what it costs
(model-tier decisions, wall time from the proposing round to the
`auto_decision` record):

| period | tier model | decisions | median | p90 |
|---|---|---|---|---|
| 2026-08-30 – 09-02 | gemini-3.7-flash | 102 | 2.4 s | 4.0–5.1 s |
| 2026-09-04 – 09-08 | gemini-3.8-flash | 219 | 4.7–10.6 s | 12.5–19.5 s |
| 2026-09-09 | gemini-3.8-flash | 3 | 23.5 s | 27.9 s |

A live bench (`internal/agent/riskmodel_bench_live_test.go`, `-tags
live`) then ran the production `evaluateRisk` — composed as `decideAuto`
composes it, with the operator's rulebook and the MCP self-descriptions
in the payload — over fourteen calls that actually reach the tier
(shell `write`-lane commands, MCP calls, `web_fetch`, an injected
"pre-authorised" line, an aligned and a contradicted build). Six of the
bench's configurations (the two other lite levels behaved as the lite
default did), **interleaved per case** so that API conditions varying
over the run hit every configuration alike; each model call time-boxed:

| configuration | decision median | wrong of 14 | notes |
|---|---|---|---|
| 3.8-flash / high (today) | 10.9 s | 1 false escalation | 3 calls stalled past 180 s; retries |
| 3.8-flash / medium | 11.9 s | 1 false escalation | per-call 7.6 s at 193 thought tokens |
| 3.8-flash / low | 13.1 s | 1 false escalation | per-call 8.9 s at 59 thought tokens |
| 3.7-flash / high | 7.3 s | 0 | no stalls |
| **3.7-flash / low** | **3.65 s** | **0** | 68 thought tokens |
| 3.5-flash-lite (no thinking) | 2.0 s | 1 false **approval**, 1 false escalation | approves an Obsidian-vault write as "local", 7 of 7 runs |

Two things the numbers settle:

1. **Lowering the thinking level buys nothing on 3.8-flash.** Per-call
   latency is 7.6–8.9 s whether the model spent 59 or 524 thought
   tokens. The time is the endpoint's, not the reasoning's — which is
   also why the transcripts got slower on 09-04 without any change on
   this side.
2. **The verdicts that matter survive on 3.7-flash at `low`, and do
   not survive on the models below it.** The rule tier cannot read
   another server's effects, so for an MCP call the model tier is the
   only eye there is; a model that approves a vault write outside the
   project as "safe local work" has no floor under it. The no-thinking
   lite model did exactly that on every run. The on-device model
   (Apple's Foundation Models framework, macOS 26.6, measured the same
   night with the same prompt) was worse: zero guardrail refusals —
   the premise of the proposed fallback never happened — and nine
   confident false approvals in twenty-four, including `curl | sh`,
   `git push`, a Slack send, and the injected line, at confidence 1.0.

So the fix is not a smaller model and not a lower dial on the main
model. It is **a slot**: the tier gets a model and a thinking level of
its own, and the measured setting is a lighter cloud model at low
thinking.

## Decision

1. **`[model].risk`** names the model the model tier runs on: the risk
   evaluation (both ADR-0081 rounds), the progress review, and the
   read-only watcher's per-turn question (ADR-0080 §2) — the three
   calls that share the tier's prompt discipline and its accounting
   bucket. Unset means the main model.
2. **`[model].risk_thinking`** sets that slot's thinking level, with the
   vocabulary and the strict validation of `[model].thinking` (ADR-0025
   §1). The two keys compose as a table:

   | `risk` | `risk_thinking` | the tier runs on |
   |---|---|---|
   | unset | unset | **the main backend** — main model at `[model].thinking`. Today's behaviour, unchanged |
   | set | unset | that model at its own default level |
   | set | set | that model at that level |
   | unset | set | the main model's *name* at that level, on a backend of its own |

   The rule behind the table: the moment the operator says anything
   about the slot, it is a slot, and **a slot never inherits the main
   dial** — the rule ADR-0025 §2 already states for the summary model,
   now applied here, which is the amendment. `--model` and
   `GEMAGENT_MODEL` move the main name, so an unset `risk` follows them.
   In the code the slot is resolved at each call, not at construction:
   a nil slot backend means "whatever the main backend is now", so
   swapping the main backend (as the tests do) moves an unnamed tier
   with it.
   **Compaction stays on the main backend** at the main level: it
   rewrites the conversation the main model will continue, so its
   quality is the main model's business, and it is not a judgment call
   whose cost this ADR measured.
3. **Same client, per-call model choice** (ADR-0014 §4): the slot is
   `WithModel` on the main backend plus a `WithThinking` derivation —
   a name and a level, not a second connection, credential, or safety
   policy.
4. **Accounting names the slot's model.** The `risk` and
   `progress_review` usage records (ADR-0057) and the
   `auto_decision.evaluator_model` field carry the model that actually
   answered; `/usage`'s reviews line, `/info`, and two read-only
   `/settings` rows (restart to change — ADR-0025 §3) say the same.
5. **Failure stays fail-closed and loud.** A slot model that the
   endpoint rejects makes every model-tier evaluation fail, and a failed
   evaluation escalates with the error in its reason — in `-p --auto`
   that is a refusal naming the model. There is no automatic fallback to
   the main model (alternatives, below).
6. **A recommendation, not a default.** `config.example.toml` names the
   measured setting (`gemini-3.7-flash` at `low`); the code compiles no
   model name in (org rule) and the unset behaviour is unchanged.
   3.7-flash's retirement date is unannounced, and a default that dies
   on a schedule is worse than one that is merely slow.

## Consequences

- With the measured setting, a Review-tier decision answers in about
  3.7 s instead of about 11 s, at equal verdict quality on the bench.
  The bench was fourteen cases at one pass when this was decided; the
  release checklist asked for a second pass and a real session. Both
  done 2026-09-10 before v0.75.0: the second pass gave 3.7-flash / low
  3.68 s per decision, 0 false approvals, and the one false escalation
  (`python gen > docs`) that 3.8-flash / high also produced — identical
  verdicts to the current model, at a quarter of its 14.8 s; the lite
  model approved the vault write again. In a real `-p --auto` session
  on the slot, a Review-tier `go test` was approved and an MCP vault
  write escalated ("modifies external Obsidian vault outside the
  project directory", 0.95), both billed to `gemini-3.7-flash`. The
  operator's own transcripts (`evaluator_model` says which model
  judged) are the ongoing measurement.
- **The slot is a free dial, and a cheaper model is not a safer one.**
  Validation constrains the level, not the model name: the operator can
  point the tier at the lite model this ADR rejects. The example config
  and the reference say so next to the key; the bench is the check to
  run before pointing the slot at any model it has not seen.
- The tier's thinking level stops riding the main dial as soon as the
  slot is named: an operator who runs the main loop at `high` no longer
  pays `high` for a JSON verdict.
- Two more keys, two more `/settings` rows, one more item on `/info`'s
  model line while a slot is named.
- **Not measured: the progress review and the read-only watcher on the
  slot model.** Both ride along because they are the same tier — the
  same prompt discipline, the same `/usage` bucket, and in the watcher's
  case a verdict that can only tighten (ADR-0080 §2), so a weaker judge
  there costs a lift dialog, never a write. A third key can separate
  either later if a measurement says it should.
- The live bench stays in the tree (`-tags live`, project from the
  environment): the next model generation will move these numbers
  again, and the question "is the tier's model still the right one" now
  has a command that answers it.

## Alternatives considered

- **On-device Foundation Models with a cloud fallback** — the trigger;
  rejected on measurement. The fallback's premise (guardrail refusals)
  never fired: 0 of 24. The failure mode was confident wrong approvals,
  which no fallback catches. A fact-extraction variant (booleans for
  network / credentials / deletion, composed in Go) failed *closed*
  instead — `go test` flagged as network and code execution — so the
  model discriminates in neither direction. A composition where the
  on-device verdict may only subtract (ADR-0081's shape) is sound but
  keeps the cloud round on every approval path, so it buys no latency.
  Details and the raw runs are in the workspace memory; revisit when
  the on-device model grows.
- **The no-thinking lite model** (`gemini-3.5-flash-lite`) — 2.0 s,
  and it approved the vault write on every run. Rejected for the reason
  in Context §2.
- **Lowering `[model].thinking` instead** — does not reach the tier's
  cost on 3.8-flash (measured), and would trade main-loop reasoning for
  a side call's latency.
- **Falling back to the main model when the slot model fails** —
  rejected: it turns a misconfiguration into silent slowness, the
  opposite of the strict-config principle. The failure is loud and
  names the model; the operator fixes the key.
- **A `[model.risk]` table with `name` / `thinking`** — rejected:
  `summary` established flat keys under `[model]`, and two flat keys
  keep `/settings` rows and provenance uniform with it.
- **A `--risk-model` flag** — not now: the summary slot has none, and
  the setting changes with model generations, not with runs.

## References

- ADR-0004, ADR-0014, ADR-0025, ADR-0040, ADR-0057, ADR-0081
- `internal/agent/riskmodel_bench_live_test.go` — the bench; run it
  against the next model generation before trusting these numbers

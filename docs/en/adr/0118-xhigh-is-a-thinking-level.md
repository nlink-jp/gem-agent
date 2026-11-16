# ADR-0118: `xhigh` is a thinking level — the dial takes a fifth value

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-16) |
| Date | 2026-11-16 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: asked which thinking options each Gemini 4 model takes. The answer showed that the top level of Gemini 4 Hydrogen could not be chosen in gem-agent |
| Amends | ADR-0025 §1 and §4 (the four values), ADR-0098 §1–2 (the words `/thinking` and the /settings row take) |
| Relates to | ADR-0082, ADR-0083 (the slots, which share the vocabulary) |

## Context

ADR-0025 took the four levels the SDK names — `minimal`, `low`, `medium`,
`high` — and left it to the API to say which of them a model supports
(§4). Gemini 4 Hydrogen adds a fifth, `XHIGH`, above `HIGH`. The
provider's table of levels (read 2026-11-16):

| Model | Levels | Default |
|---|---|---|
| Gemini 4 Carbon | LOW, MEDIUM, HIGH | MEDIUM |
| Gemini 4 Argon | LOW, MEDIUM, HIGH | MEDIUM |
| Gemini 4 Hydrogen | LOW, MEDIUM, HIGH, XHIGH | HIGH |

gem-agent refuses the word `xhigh` in all three places a level is given —
the config file at startup, `--thinking`, and `/thinking` — so that level
is out of reach.

The SDK (`google.golang.org/genai` v1.77.0, the newest release) has no
constant for it. Its `ThinkingLevel` type is the string that goes on the
wire, so the value can be sent without one.

**Measured** (`internal/llm/levels_live_test.go`, 2026-11-16, one small
arithmetic question, one call per level):

| Model | Level sent | Result |
|---|---|---|
| Gemini 4 Hydrogen | (none) | accepted, 78 thought tokens |
| Gemini 4 Hydrogen | `LOW` | accepted, 145 |
| Gemini 4 Hydrogen | `MEDIUM` | accepted, 232 |
| Gemini 4 Hydrogen | `HIGH` | accepted, 201 |
| Gemini 4 Hydrogen | `XHIGH` | accepted, 289 |
| Gemini 4 Hydrogen | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 4 Carbon | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 4 Carbon | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 4 Argon | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 4 Argon | `MINIMAL` | 400 `Thinking level is unsupported: THINKING_LEVEL_MINIMAL` |
| Gemini 3.8 Flash | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |
| Gemini 3.7 Flash | `XHIGH` | 400 `Thinking level is unsupported: THINKING_LEVEL_XHIGH` |

This is one call per row. It shows that the value reaches the model and
which models take it; it does not show what a level costs — on a
question this small the counts are not even in order. The rejections
arrived on the first request, and each already carried gem-agent's hint
naming the level and the way back (ADR-0098 §7). The two Gemini 3 rows
were measured after an independent review asked whether an older model
might ignore a level it does not know; it refuses it the same way.

**The live switch holds across `xhigh`** (`levelswitch_live_test.go`,
Gemini 4 Hydrogen, same day): a tool call made at `xhigh`, replayed with
its signature at `low` — accepted; that conversation replayed at `xhigh`
and at the model's default — accepted. This is the premise ADR-0098
measured for `high` and `low`, now for the new level.

## Decision

1. **`xhigh` joins the one vocabulary.** It is accepted wherever a level
   is named: `[model].thinking`, `[model].risk_thinking`,
   `[model].librarian_thinking`, `--thinking`, `/thinking`, and the
   `model.thinking` row in /settings, which now cycles `default, minimal,
   low, medium, high, xhigh`. `config.ValidThinking` stays the single
   predicate all of them use.
2. **It is sent as the wire string `XHIGH`**, through a constant
   gem-agent defines because the SDK has none. A test holds the value.
3. **Which model takes it stays the API's call** (ADR-0025 §4). `xhigh`
   on a model without it fails the next request with the 400 measured
   above and the existing hint. Nothing new is built for that case.

## Not taken

- **A table of models and their levels in the code**, to refuse a level
  before the request. Model names are configuration, never written into
  the code; such a table would be wrong again with the next model; and
  the API's refusal is immediate and names the level.
- **Passing any word through to the API.** A typo would then fail at the
  first request instead of at startup, and the /settings row would have
  nothing to cycle through.
- **Waiting for the SDK to name the value.** The type is the wire string;
  there is nothing to wait for. When a constant appears, it replaces
  ours and the test still holds the value.
- **The token-count knob (`thinking_budget`).** Still not offered
  (ADR-0025, Consequences). The provider's model pages say the three
  Gemini 4 models reject it with a 400; gem-agent never sends it, so
  that is read, not measured.

## Consequences

- The top level of Gemini 4 Hydrogen can be chosen, at startup or during
  a session.
- Thought tokens are billed as output and count toward the output limit
  of one response. A level this high can spend that limit before any
  answer; the existing message for that case already names `/thinking`.
- The /settings row has one more stop on its cycle.

## References

- ADR-0025 (the level, and support being per model)
- ADR-0098 (the live dial and its 400 hint)

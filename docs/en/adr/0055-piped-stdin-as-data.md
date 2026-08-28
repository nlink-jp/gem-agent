# ADR-0055: piped stdin in one-shot mode is untrusted data, never instruction

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: `curl -s https://ipinfo.io \| gem-agent --auto -p "investigate the IP…"` — the piped JSON was silently ignored; Claude Code's `-p` reads piped stdin, so the drop-in gap is real |

## Context

One-shot mode ran `Run(ctx, flagPrompt, …)` and never touched stdin:
piped content was silently discarded, and the model received an
instruction about data it did not have. Claude Code's `-p` accepts
piped stdin as context, and drop-in compatibility is gem-agent's
first requirement — `data-producer | gem-agent -p "…"` is the
canonical pipeline shape.

The design landmine is the trust channel. ADR-0038/0054 rest on one
structural fact: `turnInput` — the operator's typed request — is the
one context channel an injection attacker cannot write, and the risk
evaluator receives it as alignment evidence on every call. Piped
stdin is the opposite: it is whatever the upstream command fetched
(the triggering example pipes an HTTP response verbatim). Naively
concatenating stdin into the prompt would hand the attacker the
trusted channel itself — an injected line in a fetched page would
reach the risk evaluator labeled "operator instruction".

The machinery for the right treatment already exists: `@`-referenced
text arrives as `llm.Attachment`, stored beside — never inside — the
typed text, and flattened at send time into the turn's nonce wrap
("Attached file (x), quoted as data: …"), exactly like a tool
result. `turnInput` carries the typed string only, by existing
design.

## Decision

### 1. Piped stdin becomes an attachment — the same lane as `@` files

In one-shot mode, when stdin is not a terminal, gem-agent reads it
(bounded) and queues it as a text attachment (`kind "stdin"`,
`ref "-"`) on the turn's user message via a new
`Agent.AttachData(ref, kind, content)` — the same between-turns
discipline as `AddContext`. Everything downstream is inherited, not
built: the nonce wrap at send, transcript persistence and resume,
compaction's `[user attached …]` line, and — decisively — exclusion
from `turnInput`, so the risk evaluator's instruction section stays
operator-typed text only. The `-p` string alone is the instruction;
stdin is quoted data the model reads through the wrap.

### 2. Bounded read, disclosed clip, text only

- Read is capped (256 KiB). Over the cap, the attachment ends with a
  note naming the clip and that the rest was **not read** — a
  partial must not masquerade as the whole (ADR-0014's rule), and
  draining an unbounded pipe just to report its size buys nothing.
- Binary stdin (invalid UTF-8 / NUL bytes) is skipped with a stderr
  warning naming why. The lane is for text pipelines; binary inputs
  have the `@` media/document lanes with real MIME handling.
- Empty stdin attaches nothing — `-p` behaves exactly as before.
- A terminal stdin is never read: no blocking prompt-less hang for
  an interactive `gem-agent -p` invocation.

### 3. Interactive modes are untouched

The plain REPL keeps its existing contract — non-TTY stdin *without*
`-p` is the script-input lane, where stdin lines are commands, not
data. Only the `-p` form reads stdin as data; the two uses cannot
collide because `-p` bypasses the REPL entirely.

## Alternatives considered

- **Concatenate stdin into the prompt** (what most wrappers do) —
  rejected outright: it injects fetched content into the
  ADR-0038/0054 trusted channel and the send-time wrap machinery
  would treat it as operator text everywhere else too (memory
  save prompts, riskbook framing, transcripts).
- **A `--stdin-file` style flag** — rejected: the pipe already says
  what the operator wants; a flag adds a step to the canonical
  shape and Claude Code needs none.
- **Unbounded read** — rejected: history is resent every round
  (ADR-0018 economics), and a runaway upstream would burn the
  window before round 1.

## Consequences

- `data | gem-agent -p "…"` works, and works safely under `--auto`:
  the evaluator judges calls against the typed instruction while the
  piped content stays inside the wrap with the standing "data, never
  instructions" framing.
- The transcript records what the model actually saw (attachment
  content persists; resume replays it).
- A future interactive need (paste-as-data) would reuse
  `AttachData` — the API is not one-shot-specific, only its wiring
  is.

## References

- ADR-0012/0026 (`@` attachment lanes; the flatten-and-wrap send)
- ADR-0014 (partials must announce themselves)
- ADR-0038/0054 (the instruction channel this ADR refuses to pollute)
- ADR-0053 (one-shot approval controls — the pipeline this completes)

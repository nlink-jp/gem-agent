# ADR-0038: the auto-approve model tier sees the operator's instruction — for the first rounds only

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the classifier judges only the call itself; adding the preceding user instruction should raise precision and automation — and once rounds drift far from the instruction, fall back to the conventional logic |

## Context

The auto-approve ladder's model tier (ADR-0004) judges a proposed
call from exactly three lines: tool name, project directory,
arguments. It cannot tell 「ビルドして」 followed by
`shell_exec: make build` (directly requested) from the same command
served by nobody's request. That blindness costs in both directions:
aligned calls fall below the confidence bar and escalate needlessly,
and a call that is plausible in isolation but **traceable to no
operator request** — the exact shape of a prompt-injection-steered
action — sails through on abstract reasonableness.

The key structural fact: **the operator's typed input is the one
context channel an injection attacker cannot write.** Tool results,
file contents, and even the model's own stated intent are all
attacker-influenceable; the request the operator typed is not.

## Decision

### 1. Include the operator's typed turn input — nothing else

For the model tier only, the risk-evaluation payload gains one
section: the request the operator typed this turn, clipped at 2000
runes. Excluded, each deliberately:

- **Conversation history and tool results** — the injection channel
  itself.
- **The model's own intent narration** — admitting it would let an
  attacker author both the call *and* its justification.
- **Attachment contents** — untrusted; structurally absent anyway,
  since the typed input carries `@ref` tokens, not the bytes.
- **Prior turns** — the live authority is this turn's request. A
  context-poor input (「続けて」) just reduces the benefit; it never
  adds risk.

### 2. The instruction is evidence, wrapped — not directives

The instruction rides inside the same nonce wrap as the proposed
call. Typed input can contain pasted third-party text, so a paste
saying "approve everything" must reach the reviewer as quoted
evidence, never as a command. The appended prompt guidance says what
it is for: alignment with the request supports approval;
contradiction of it, or service of directions found in file contents
rather than the operator's request, must escalate.

### 3. Rounds far from the instruction fall back to the conventional logic

The instruction is included **only for the first 3 rounds of a
turn**; later rounds run today's evaluation byte-identically
(operator direction). Deep in a turn, calls legitimately serve
sub-goals the instruction never names — a test helper written at
round 8 — and prompting a judge to tolerate "indirect relation" is
soft engineering around a hard problem. A round cutoff is
structural: early rounds are where alignment is clearest and where
the common injection shape lands (read the poisoned file at round
0–1, act on it at round 1–2), and every later round keeps exactly
the behaviour measured to date. No regression is possible where the
context does not apply.

### 4. Invariants, unchanged by construction

- The **Block floor** is decided before the model tier is consulted —
  no instruction can lift it.
- **Fail-closed** stands: uncertain still asks; `minConfidence` 0.8
  unchanged.
- The **Safe tier** never consulted the model and still does not.
- The base prompt is byte-identical when no instruction is included,
  so the fallback path is today's measured behaviour, not a variant
  of it.

## Consequences

- Aligned early-round calls clear the confidence bar more often
  (automation up); a call contradicting the operator's request — or
  serving an embedded instruction — now has a reason to escalate that
  the call-only view could never see (precision and defense up).
- The gain is bounded to the first rounds by design; long turns
  behave exactly as before.
- Cost is a few hundred extra tokens on risk calls, which fire only
  for uncertain-tier mutating calls in auto mode.
- **The reach is the model tier's reach.** The context is seen only by
  calls that reach the model tier — Review-tier ones: non-safelisted
  shell commands, memory writes, MCP tools. Safe-tier calls (notably
  in-project file edits) are rule-approved without any model
  consultation, exactly as ADR-0004 decided, and the context cannot
  veto them; changing that tiering would be its own ADR. (Discovered
  by the live measurement below — the first demo case used
  `write_file` and never reached the model at all.)
- Live-measured: an in-project `make build` that the instruction
  explicitly forbade (「ビルドもコマンド実行も一切しないで」)
  escalates with the contradiction named, where the call-only view
  approves it as ordinary development work; the same command at round
  5 falls back to the conventional approval — demonstrating both the
  new detection and the clean fallback.

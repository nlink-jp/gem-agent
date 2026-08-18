# ADR-0004: Auto-approve with two-layer risk evaluation

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator request for an auto-approve mode, with the explicit design that a tool call be judged by both a logical (rule-based) risk assessment and a model-based one |

## Context

ADR-0001 made per-call human approval (MITL) the primary defense, with
sandbox-exec as defense-in-depth. Approving every mutating call by hand
is the right default but too slow for longer unattended stretches. An
auto-approve mode must not simply drop the gate: the agent-skeleton
external review established that heuristic pattern matching alone cannot
catch dynamic construction (`$(echo /etc/passwd)`), and the org's
destructive-delete lesson requires HITL to stay in the loop for
irreversible actions.

## Decision

Add an **opt-in** auto-approve mode. It does not remove approval; it
raises the bar for what still needs to ask. When on, each mutating tool
call passes through an escalation ladder:

1. **Logical tier** — a pure, LLM-free classifier (`internal/risk`)
   returns one of:
   - **Safe** → auto-approved. (In-project write/edit, read-only shell
     commands.)
   - **Block** → **always escalates to the human gate; the model layer
     cannot override this.** The hard floor: destructive or irreversible
     or scope-escaping operations (`rm -rf`, `sudo`, `git push`,
     `git reset --hard`, `curl … | sh`, `dd`/`mkfs`, redirection to
     `/dev/…`, `chmod 777`, credential paths like `~/.ssh` `~/.aws`
     `.env`, fork bombs, writes outside the project).
   - **Review** → uncertain; goes to the model layer. (MCP tools, whose
     effects the classifier cannot know, are Review.)
2. **Model tier** — evaluates a Review call and returns a structured
   verdict (auto-approve + reason, or escalate). Low-risk **and**
   confident → auto-approve; anything else, any parse/timeout error, or
   any sign of prompt injection in the call → escalate.

Escalation means the existing human approval dialog, carrying the reason.
**Auto mode is off by default** (ADR-0001's MITL stays the default), a
config default (`[agent].auto_approve`) plus a runtime toggle. The
sandbox (ADR-0001) applies unchanged in every mode.

**Fail closed everywhere.** Unknown tool, classifier uncertainty that the
model cannot resolve, model error, or malformed verdict all escalate to
the human — never auto-approve on doubt.

**Injection isolation.** The tool-call arguments handed to the model
evaluator are untrusted (a prior tool result may have influenced them),
so they are nonce-wrapped (nlk/guard) and the evaluator is instructed
that text inside asking to be approved is itself a reason to escalate.

## Consequences

- Auto mode handles the safe majority (list/read are already ungated;
  in-project edits and benign shell commands auto-run) while every
  genuinely risky action still reaches a human — MITL preserved as the
  backstop, not deleted.
- The Block tier is a deterministic floor independent of model judgment:
  even a compromised or mistaken model cannot auto-approve `rm -rf`.
- Cost/latency: Review adds one LLM round-trip per uncertain call; Safe
  and Block are instant (no model call). Acceptable for an auto mode.
- The model evaluator uses the same configured model. A separate cheaper
  risk model (`[agent].risk_model`) is a possible future refinement, not
  taken now.
- This amends ADR-0001 (MITL is now "primary defense, default mode"
  rather than "always"); ADR-0001's sandbox and isolation clauses are
  unchanged.

## Alternatives considered

- **Model-only evaluation** (no logical tier) — rejected: a single model
  call is both the injection surface and the sole arbiter, with no
  deterministic floor for irreversible actions. The logical Block tier
  is what makes a bad model verdict non-catastrophic.
- **Logical-only** (no model tier) — rejected: the agent-skeleton review
  already showed heuristics miss dynamic construction; the model catches
  what patterns cannot.
- **Auto-deny the risky tier** (Claude Code "blocks the rest") — softened
  to escalate-to-human: in interactive use the human is present and a
  hard block is worse UX than one prompt; the MITL backstop is the point.
- **On by default** — rejected: weakening the primary defense must be a
  deliberate opt-in.

## References

- ADR-0001 (MITL primary defense; sandbox defense-in-depth — amended here)
- agent-skeleton external review (heuristics miss dynamic construction)
- Org lessons: destructive-delete HITL; injection-triage two conditions;
  nonce-tag isolation

# ADR-0053: one-shot approval controls — `--auto` and `--allow`

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a headless Slack read-summarize-post pipeline died on the one-shot blanket deny; the risk ladder never ran, and `[agent].auto_approve` was silently ignored in `-p` |

## Context

One-shot mode (`-p`) wires `denyGate` as the approver: every gated
call is refused, because nothing can answer a prompt there. That much
is documented. What was **not** documented — or recorded anywhere —
is that one-shot also force-disabled auto-approve: `AutoApprove:
cfg.Agent.AutoApprove && !oneShot`, an uncommented conjunction in
`cmd/root.go`. The ADR-0004 ladder (rule classifier → model
evaluation → escalate) never ran headless; a call the ladder would
wave through in the TUI was blanket-denied in a pipeline, and a
config that says `auto_approve = true` silently meant `false`.

The ladder itself needs no redesign to run headless. Its terminal
state, "escalate to the human", degrades to "deny" when no human
exists — which is fail-closed, and is exactly the "auto-deny the
risky tier" shape ADR-0004 softened *for interactive use* because a
present human deserves a prompt. Headless, there is no one to soften
for. Every floor (Block tier, `"always"` policy, pre-tool hooks)
already lands on `mustPrompt`, which the deny gate answers with no.

Separately, ADR-0008's `"never"` policy already lets read-only MCP
lookups run in pipelines — but it is a **standing** grant: it also
switches the gate off in every interactive session, and for an
egress-capable tool (posting to Slack, say) a permanent no-questions
grant is exactly what the ADR-0017 reasoning warns about. What was
missing is a grant with the lifetime of one invocation.

ADR-0049 (the `/learn` withdrawal) set the bar for any loosening
mechanism: enumerated, not wildcard-bought; visible where it is
granted; and the writing cost is part of the design — deliberation
lives in the explicit act.

## Decision

### 1. `--auto` arms the ADR-0004 ladder in one-shot mode

`gem-agent -p "…" --auto` runs mutating calls through the normal
two-tier evaluation. Approvals happen exactly as in the TUI (rule
tier for Safe, model tier for Review, confidence ≥ 0.8); everything
else — escalations, Block-tier calls, `"always"`-policy tools —
falls through to the deny gate and is refused **with the ladder's
reason on stderr**.

`[agent].auto_approve` remains ignored in one-shot mode, now
deliberately: an unattended run's grant must be visible **on the
invocation itself** (the command line a script or cron entry shows),
not in a standing file far from where the run was launched. This
extends ADR-0004's "weakening the primary defense must be a
deliberate opt-in" to: in headless use, per-invocation opt-in.

In interactive mode `--auto` simply starts the session in auto mode —
the same thing the config key or shift+tab does, recorded with flag
provenance in `/settings`.

### 2. `--allow` grants per-run `"never"` entries

`--allow "name"` (repeatable, or comma-separated) takes exactly the
`[approval.tools]` vocabulary — an exact tool name or a trailing-
wildcard prefix like `mcp__server__*` — and injects each entry as a
`"never"` policy for this run only.

The entries join the **global scope at flag precedence** (flags >
machine-owned policy file > hand-written config — the order the
config system already declares), and then everything about ADR-0008
resolution holds unchanged and automatically: a project's `"always"`
tighten still wins by scope, the Block floor is not lifted, pre-tool
hooks still deny, a bare `"*"` is still a hard error, and `/tools`
shows the effective result. No parallel bypass mechanism exists for
the floors to forget.

The flag works in every mode (one semantic per flag), but it exists
for `-p`: an interactive session already has richer tools (`a`,
`p`, `/settings`).

### 3. The deny gate explains itself

`denyGate` now prints the reason it was handed — a ladder
escalation's cause, a Block verdict — instead of only the generic
"mutating tools are disabled in one-shot mode" line, and the generic
line now names `--allow` and `--auto` as the remedies. A pipeline
that ends in denials is exactly the case where the operator
reconstructs the run from stderr (ADR-0047 §5).

### 4. Telemetry reports the effective auto state

`SessionStart` reported the raw `[agent].auto_approve` config value
while one-shot forced the effective value off — an audit event
claiming auto was armed in runs where it never could be. It now
reports what the session actually runs with. (Found while reading
the wiring for this ADR; the honest-reporting rule is ADR-0009's.)

## Alternatives considered

- **Honor `[agent].auto_approve` in `-p`** — rejected: a standing
  config line arming unattended auto-approval is invisible at the
  point of launch. The grant belongs on the invocation.
- **A separate allowlist mechanism outside the policy layer** —
  rejected: every floor (Block, project tighten, hooks, the `"*"`
  ban) would need re-implementing, and each miss is a bypass. Flag
  entries compile into the existing policy build instead.
- **Restrict both flags to one-shot mode** — rejected: one flag word
  with two mode-dependent behaviours (error vs effect) costs more
  than uniform semantics; interactively they are merely redundant,
  not harmful, and provenance surfaces (`/settings`, `/tools`) show
  them.
- **A headless "Safe tier only" auto variant** (skip the model tier)
  — rejected: the ladder is already fail-closed headless, and a
  second evaluation shape doubles the behaviour matrix ADR-0004's
  tests pin.

## Consequences

- The Slack-pipeline shape becomes expressible three ways, by risk
  appetite: standing `"never"` for read-only lookups (ADR-0008),
  `--allow` for enumerated per-run grants, `--auto` for the
  probabilistic general case — with the model evaluator as the only
  arbiter of Review-tier calls in that last form. The approval docs
  say which to reach for.
- `-p --auto` reading untrusted content with an egress tool granted
  is the operator's explicit, visible choice — the same injection
  calculus as interactive auto mode, minus the watching human.
- One-shot stderr gains `[auto-approved …]` lines (the existing
  plain-REPL callback now fires there), so a pipeline's audit trail
  shows what the ladder waved through, not only what it denied.

## References

- ADR-0004 (two-tier auto-approve; deliberate opt-in)
- ADR-0008 (per-tool policy; `"never"` in pipelines)
- ADR-0009 (provenance display; honest reporting)
- ADR-0017 (egress tools gate by default)
- ADR-0047 §5 (a denied run must be reconstructable)
- ADR-0049 (loosening: enumerated, visible, deliberate)

# ADR-0061: independent agent runtime — repositioning and promotion to cli-series

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-01 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator decision: "gem-agent is no longer a Claude Code backup — treat it as an independent agent runtime, and promote it to cli-series without the drill bar; real-world deployment is the evidence" |

## Context

gem-agent's original charter (RFP §1) was a continuity tool: keep
development work moving on the day Claude Code is unavailable. That
premise was load-bearing in four places:

1. **Positioning** — the README's opening sentence and Why section,
   the RFP's problem statement, the org profile row, the website
   card, the GitHub About text, and the Homebrew tap table all
   describe the tool by reference to Claude Code's absence.
2. **The scope yardstick** — "a backup tool needs the core 20% of
   Claude Code's daily features" (RFP §3). Scope was defined as a
   fraction of another product.
3. **The monthly drill** — "a backup that is never exercised is not a
   backup" ([drill](../reference/drill.md)). The drill exists because
   a backup's defining risk is rotting unused.
4. **The promotion bar** — six consecutive drill passes, a survived
   model-retirement cycle, and the rest of
   [promotion](../reference/promotion.md). Every criterion answers
   one question: will the fallback work, without surprises, on the
   day it is needed?

Reality moved out from under the premise. The recent feature
trajectory — headless `--auto` (ADR-0053), piped-stdin data
attachment (ADR-0055), the risk rulebook (ADR-0050), audit telemetry
(ADR-0035), operator pre-tool hooks (ADR-0044), session work
directories (ADR-0058) — serves day-to-day operation, not a
minimal standby. And the tool is in day-to-day operation: real
working sessions (the ADR-0058 field E2E ran on two of them) and
headless pipelines are deployment, not drills. The operator's
judgment: the tool's capability has outgrown the backup role.

## Decision

**1. gem-agent is an independent agent runtime.** Its identity is no
longer defined by reference to Claude Code's availability. It is a
CLI agent runtime on Vertex AI Gemini, macOS-only, defended by two
layers (sandbox-exec plus approval gates), used interactively and
headlessly.

**2. Drop-in compatibility remains the top requirement — with a new
rationale.** Reading a project's `AGENTS.md` / `CLAUDE.md` /
`.mcp.json` / skills as-is was justified as "switching over during
an outage requires no reconfiguration". The justification is now
ecosystem compatibility: one project setup serves every runtime that
works on it. The requirement, the mechanisms, and their security
boundaries (ADR-0010, ADR-0011, ADR-0023) are unchanged.

**3. Scope minimalism stands on its own charter.** The shell-agent
v1 lesson (feature accumulation → complexity → rewrite) still
governs, but the yardstick is no longer a fraction of Claude Code.
The charter is: a minimal, auditable agent loop — read / edit /
shell / MCP / approval — with no analysis or GUI subsystems.
Features still enter by ADR only.

**4. Promotion to cli-series, effective now, by operator decision.**
The promotion bar is superseded, not passed: its criteria measured
"will the backup work on the day it is needed", a question that no
longer matches the role. What the new role needs to know — does it
work in real use — real-world deployment already answers.
[promotion](../reference/promotion.md) is closed out as a historical
record of the bar and of this decision. The cli-series contract
applies from now on: interface stability is a promise, and breaking
changes go through the org's breaking-change process (user
confirmation → compatibility plan → implementation) rather than a
CHANGELOG line.

**5. The monthly drill is retired as an obligation.** Regular use
now provides the rot detection the drill existed to provide.
[drill](../reference/drill.md) is reframed as an on-demand health
check: recommended after a long idle stretch, before relying on a
clean-machine install, after an OS or model-generation change, or
when the architecture doc is suspected of drifting from the code
(step 7 keeps that audit). Nothing in the runbook's steps changes —
only the cadence and the framing.

## Consequences

- Positioning surfaces are rewritten from the new premise rather
  than word-patched: README (both languages), RFP §1 and §6 (with
  pointers to this ADR; the Discussion Log stays as history),
  AGENTS.md, CLAUDE.md, INDEX entries, org profile, website card,
  GitHub About, tap table.
- The repository moves from the lab-series umbrella to the
  cli-series umbrella. The repository URL does not change.
- The stability promise is now in force. gem-agent's user count
  (currently one) does not soften it — the contract is what makes
  the promise checkable by anyone.
- Accepted risk: without a mandatory drill, environment rot
  detection rests on actual use. If usage ever lapses for a long
  period, run the health check before relying on the tool again —
  the runbook says exactly this.
- Historical documents (ADRs, the RFP Discussion Log, the frozen
  first-drill record) keep their era's language. A record speaks
  for its own time.

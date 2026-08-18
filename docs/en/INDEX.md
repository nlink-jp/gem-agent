# Documentation Index

Entry point for gem-agent's maintainer-facing documentation. For
user-facing material see [`README.md`](../../README.md).

Japanese mirror: [`INDEX.ja.md`](../ja/INDEX.ja.md) (full parity,
enforced by `scripts/docs-mirror-check.sh`).

## Specification

- [`gem-agent-rfp.md`](gem-agent-rfp.md) — the canonical spec: problem
  statement, functional surface, scope boundaries, phase plan. Features
  outside it need an ADR, which is how session resume and context
  compaction got in.

## Reference

Current behaviour. Evergreen — updated in place as the code changes.

- [`reference/architecture.md`](reference/architecture.md) — package
  layout, the turn loop, the two confinement boundaries, persistence,
  and failure behaviour in one table
- [`reference/drill.md`](reference/drill.md) — the monthly drill: what
  rots on its own, the procedure that catches it, and the record of the
  first run
- [`reference/promotion.md`](reference/promotion.md) — the checkable bar
  for moving out of lab-series into cli-series, and the current status

## ADRs

Point-in-time design decisions. Immutable once accepted; a changed
decision gets a new ADR that supersedes the old one (typo and link fixes
excepted).

- [`ADR-0001`](adr/0001-sandbox-mechanism.md) — sandbox-exec plus MITL:
  two boundaries, one for decisions and one for containment
- [`ADR-0002`](adr/0002-tui.md) — Bubble Tea in inline mode; alt-screen
  rejected to keep native scrollback and copy/paste
- [`ADR-0003`](adr/0003-bottom-pinned-layout.md) — pinning the input to
  the window bottom without alt-screen
- [`ADR-0004`](adr/0004-auto-approve.md) — auto-approve as a two-tier
  ladder: a rule floor the model cannot lift, then a model judgement
- [`ADR-0005`](adr/0005-session-resume.md) — the session log becomes the
  resume source of truth; refusals over warnings on project and model
- [`ADR-0006`](adr/0006-context-compaction.md) — summarise the older
  half instead of failing at the window; fail safe, never fail small

## History

Frozen audit trail of superseded documents. Empty so far — nothing has
been superseded yet. When something is, it moves here with a note saying
what replaced it, rather than being deleted: the discussion in a
superseded document is often the only record of why the current design is
shaped as it is.

# ADR-0045: transcript-driven approval-rule learning — `/learn` proposes, the operator decides

| Field | Value |
|-------|-------|
| Status | **Proposed** |
| Date | 2026-08-26 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: auto mode runs on a fixed evaluation — can it learn from the operator's decision history? Direction set in review: rule sets are built per project (the opposite would be dangerous), and learning runs as an explicit `/` command over the recorded transcripts, proposing rules. Follow-up: the model tier's verdicts on MCP calls visibly wobble — an equal friction source |

## Context

The auto-approve ladder (ADR-0004) is fixed: a pure-function rule tier
(Safe/Review/Block), a model tier consulted only for Review (approve ∧
confidence ≥ 0.8), and the human gate. ADR-0038 added one adaptive
element — the operator's typed instruction rides along as evidence —
but nothing in the ladder ever changes shape from experience. The
operator answers the same question about `go test` for the hundredth
time with the same `y`.

MCP calls are the other half of the friction, and the operator reports
it directly: the rule tier classifies every `mcp__` call Review
(effects unknown to it), so each call rides on the model tier — whose
verdicts wobble. The same lookup tool is auto-approved on one call and
escalated on the next, and an escalation the operator cannot predict
is the same friction as the hundredth `go test` prompt with worse
ergonomics: it interrupts an investigation chain mid-flight.

Feedback channels exist, but each carries exactly one decision: `a`
(session allowlist), `p` (persisted per-tool policy, ADR-0009), and the
hand-written ADR-0008 tables. The accumulated record of what the
operator actually decided — dozens of sessions of it — is written down
and then never read.

Two structural facts shape any learning design:

**The signal is one-sided.** A human decision exists only for calls
that were escalated. A wrong auto-approval produces no correction
signal at all. Learning from this history can therefore only reduce
over-escalation (friction); it cannot improve the safety side, and it
must not be allowed to degrade it.

**The labels are trustworthy; the calls are not.** The decision itself
(y/n) is operator input — the one channel a prompt-injection attacker
cannot write (the ADR-0038 argument). But the call being decided was
authored by the model, which tool output can influence. Any matching
that is looser than deterministic equality hands an attacker a way to
dress a hostile call as a previously-approved pattern.

What the transcripts already hold, measured against the current code:

- **Denials**: a tool-role message whose `Content` equals the exact
  `deniedResult` constant. Hook denials carry the distinct
  `denied by a pre-tool hook: ` prefix and are excluded — they are not
  operator decisions.
- **Auto-mode verdicts**: `auto_decision` diagnostic records
  (name, approved, tier, reason, model). Calls these approved are the
  ladder's decisions, not the operator's, and are excluded from
  endorsement counts.
- **Operator approvals**, in two shapes. An `auto_decision` record
  with `approved=false` followed by a real result: the ladder
  escalated and the operator said yes — the exact shape of the MCP
  wobble, and the strongest evidence class. And a mutating call with a
  real result and no `auto_decision` record (non-auto mode) — where
  `y`, the session allowlist, and a `"never"` policy in force at the
  time are retroactively indistinguishable. §7 removes this ambiguity
  going forward; for backfill it is mitigated by deduplicating
  proposals against current policy.

## Decision

### 1. `/learn` — operator-invoked, never ambient

Learning runs when the operator types `/learn`, and at no other time.
The command scans **this project's transcripts only** (the ADR-0022
layout plus legacy flat files whose header names this project),
aggregates the operator's decisions, and presents rule proposals for
per-item confirmation. Nothing changes until a proposal is accepted.

A background learner that shifted approval behavior silently would be
an unauditable loosening channel — the exact thing the ADR-0008
asymmetry (tightening is free, loosening is an explicit operator act)
exists to prevent. Keeping the operator the author of every rule is
the design, not a limitation.

### 2. Deterministic extraction — no model reads the transcripts

The learning pass parses structured records only: tool-call names and
arguments, tool-role results compared against exact constants, and
`auto_decision` records. It never feeds transcript text to a model.

Transcripts are full of attacker-influenceable prose — tool outputs,
file contents, web pages. A model that read them and then proposed
policy would be a prompt-injection-to-policy pipeline: the persistence
vector ADR-0020 §4 closes for memory, rebuilt one layer up.
Deterministic aggregation is immune by construction — the only inputs
that carry weight are the operator's own recorded decisions.

### 3. Aggregation keys are syntactic facts

- **Non-shell tools** (MCP included): the tool name — the ADR-0008
  vocabulary, directly. Arguments are deliberately not part of the
  key: for a lookup-style tool the risk lives in what the tool does,
  not in which indicator it is asked about.
- **`shell_exec`**: the command key is the first token, extended by the
  second token iff it has subcommand shape (`^[a-z][a-z0-9-]*$` — not a
  flag, not a path). `go test`, `make build`, `git status` keep two
  tokens; `ls -la` and `touch newfile.txt` reduce to their head.
- **No key is derived** — at learn time *and* at match time — for
  commands that are not plain: multi-segment commands (`|`, `;`, `&&`,
  `||`, `&`), dynamic construction (`$(`, backticks, `${`, `eval`), or
  any redirection. A key that names two tokens must be the whole truth
  of what runs (the agent-skeleton finding). Learner and matcher share
  one derivation function so they cannot drift apart.

Semantic or fuzzy similarity is rejected outright: it is precisely the
loose matching §Context warns about.

### 4. New vocabulary: per-command policy, project scope only

`[projects."<path>".commands]` in the machine-owned `policy.toml` maps
a command key to `"never"` / `"always"` — the same two words as
ADR-0008, no third vocabulary. Semantics are identical to ADR-0008:

- `"never"` skips the gate and the model tier in all modes, but does
  **not** lift the rule tier's Block floor, and pre-tool hooks
  (ADR-0044) still run first.
- `"always"` is the tightening floor: always ask, in every mode.
- A command that fails §3's plainness test matches no rule and takes
  the normal ladder.

A **global commands table deliberately does not exist** (operator
direction). `make build` being safe in one repository says nothing
about another; a `"never"` learned in a trusted project, applied
globally, would auto-run inside the next hostile clone. Per-tool
global policy (ADR-0008) is unchanged.

The vocabulary lives in machine-owned `policy.toml` only. Extending
the hand-written `[approval]` tables can follow if demand appears —
a capability without a demonstrated trigger is dead weight (ADR-0044).

### 5. Proposal thresholds — v1 constants

- Propose `"never"` for a key with **≥ 5 operator approvals across
  ≥ 2 sessions and 0 denials**, whose representative calls do not
  classify Block today.
- Propose `"always"` for a key with **≥ 2 denials** — tightening gets
  the lower bar, in the Block-pattern spirit (a generous match costs
  one prompt).
- Never proposed: `save_memory` / `delete_memory`. Frequency evidence
  is invalid where the risk lives in per-call content — twelve
  harmless saves say nothing about the thirteenth (ADR-0020 §4).
  Command keys aggregate calls with stable semantics; memory writes
  are the opposite.
- MCP proposals get no semantic filter: the learner cannot tell a
  lookup (risk per tool) from a messenger (risk per call). That
  judgment is what the confirmation step asks of the operator — who,
  unlike any classifier, knows what the server does. This is
  ADR-0008's original rationale applied: the policy table is the place
  to write down what only the operator knows.
- Skipped: keys already covered by current policy in either scope, and
  keys whose calls classify Safe (there is no friction to remove).

Constants, not config: there is no evidence yet that tuning is needed.

### 6. Confirmation: evidence, then one decision per rule

Each proposal is shown with its evidence — approved/denied counts,
session count, two or three example calls the operator has already
seen and approved, and how the ladder handled the key historically
(rule tier, model approved / escalated counts). That last line makes
the wobble visible: "model tier approved 12, escalated 9" is the case
for replacing a per-call judgment with a deterministic rule. MCP
proposals additionally show the tool's current self-description,
clipped (ADR-0046 §4) — the operator judging "lookup or messenger?"
should not have to recall it. Each proposal is answered y/n in the
approval-dialog grammar.
Accepted proposals are written via `MutatePolicyFile` and take effect
immediately; `/settings` shows them with `policy.toml (project)`
provenance, editable and deletable like any other entry.

### 7. `gate_decision` records make future learning exact

Alongside the existing telemetry emit, the agent writes a
`gate_decision` diagnostic record (tool name, decision, source
`operator`/`allowlist`, mustPrompt) to the transcript. `Load` already
skips unknown kinds, so no schema bump; the record is diagnostic, like
`auto_decision`, and invisible to resume. It records nothing that
`ToolCalls` args do not already store. Backfill over old transcripts
uses the §Context reconstruction; precision improves as these records
accumulate.

## Rejected alternatives

- **Ambient learning / automatic threshold adjustment** — silently
  loosening approval behavior violates the ADR-0008 asymmetry and
  re-enacts the v0.39.0 lesson that self-approval is not a defense.
- **History as evidence in the model tier** (the ADR-0038 shape, fed
  with past decisions): adapts silently and audits poorly; the
  operator chose explicit, inspectable rules. May return as a future
  ADR if command-rule coverage proves insufficient.
- **Semantic similarity matching** — a poisoning surface (§3).

## Consequences

- Friction falls only where the operator has already voted repeatedly;
  a genuinely novel command still asks. That bound is the point.
- Learned rules are ordinary policy: visible with provenance,
  deletable, and subordinate to the Block floor and pre-tool hooks.
- The scan decodes records partially (kind first, then only the fields
  needed), skipping attachment payloads — a transcript full of base64
  images costs little.
- Backfill imprecision (y vs allowlist vs policy-at-the-time) is named
  in §Context, mitigated by policy dedup, and decays as
  `gate_decision` records accumulate.
- Tests: the key-derivation table (including every §3 exclusion),
  extraction against synthetic transcripts (exact denial constant,
  hook prefix, auto_decision exclusion), the shared learner/matcher
  function, threshold logic, and the policy write path with dedup.

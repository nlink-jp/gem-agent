# Promotion criteria: lab-series → cli-series

> **Closed (2026-09-01).** gem-agent was promoted to cli-series by
> operator decision under
> [ADR-0061](../adr/0061-independent-runtime-promotion.md). The bar below
> was superseded, not passed: it measured whether a backup would work on
> the day it was needed, and the backup role was retired — real-world
> deployment already answered what the new role needs answered. This
> document remains as the record of the bar and of that decision; nothing
> below is expected to change again.

gem-agent sat in lab-series, whose contract is "experimental; interfaces
may change without notice". A fallback tool wants the opposite contract —
it must work on the day it is needed, without surprises. The RFP (§6) put
it in lab-series anyway and named the tension explicitly, to be resolved
by the monthly [drill](drill.md) and by criteria written down before
anyone wants to argue about them. This is that document.

The criteria exist to be checkable by someone in a hurry, so each one is
a fact you can look up rather than a judgement you have to form.

## The bar

All of the following, together:

| # | Criterion | How it is checked |
|---|---|---|
| 1 | **Six consecutive monthly drills passed**, none skipped | drill records; a skipped step is not a pass |
| 2 | **No step-4b failure, ever** | containment is the one non-negotiable: a sandbox that did not hold, even once, resets the count |
| 3 | **Two real tasks completed with gem-agent alone**, in different months, that the operator would have shipped | the drill's closing two questions (step 8), answered yes |
| 4 | **No open issue that blocks a drill step** | issue tracker |
| 5 | **No breaking config or CLI change in the last three months** | CHANGELOG; a fallback whose flags moved is a fallback you cannot use from memory |
| 6 | **The model policy survives one retirement cycle** | a Gemini generation retired while gem-agent kept working via config alone, with no code change |
| 7 | **Install works from a clean machine** | `brew install nlink-jp/tap/gem-agent` on a machine that never had it, then drill steps 1–2 |

Six months is not arbitrary: it is long enough to cross at least one
macOS point release, one Gemini model deprecation notice, and one
credential expiry, which are the three things that break this tool
without anyone touching it.

## What promotion changes

- **Series and umbrella**: repository moves to `cli-series`, submodule
  pointer moves with it, the catalog and org profile rows follow.
- **Contract**: interface stability becomes a promise. Breaking changes
  need the org's breaking-change process (confirm affected users, offer a
  compatible path, then implement) rather than a CHANGELOG line.
- **What does not change**: the drill. Promotion is not graduation from
  being exercised — the drill is the reason the criteria could be met,
  and dropping it afterwards is how the tool would rot while carrying a
  stability promise.

## What promotion is not

Not a reward for features. gem-agent's scope is deliberately the core 20%
of Claude Code (RFP §3), and adding features to look more promotable would
work against the reason it exists. Nothing in the list above counts
features.

Not urgent. Staying in lab-series costs nothing operationally — the tool
is installed, released, and drilled either way. The only thing promotion
buys is a stability promise to other people, and gem-agent currently has
one operator.

## Outcome

**Promoted 2026-09-01 — bar superseded** (see the banner at the top and
ADR-0061). Status when the bar was retired, as of 2026-08-19: one drill
run (2026-08-19, pass), so criterion 1 stood at 1 of 6. Criterion 6 was
open until the next Gemini retirement. Criteria 2, 3, 4, 5 had no
failures recorded. The bar was never failed — it stopped being the right
question.

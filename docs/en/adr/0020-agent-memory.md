# ADR-0020: Agent memory across sessions

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: global memory and project memory may be needed |

## Context

Everything gem-agent knows beyond the current conversation comes from
two places: the operator's instruction files (AGENTS.md and friends,
injected at startup) and whatever a resumed transcript happens to
contain. Facts learned *during* work — a build quirk, an operator
preference, the name of the staging host — die with the session unless
the operator writes them into an instruction file by hand. Claude Code
solves this with agent-maintained memory; the fallback tool loses
exactly this continuity when it is needed most.

## Decision

1. **Agent-written memory at two scopes.** Global (facts about the
   operator and this machine, loaded everywhere) and project (facts
   about one project, loaded there only). One memory = one small
   markdown file named by a slug; saving the same name updates it.
2. **Machine-owned, outside the repository.** Memory is agent-produced
   data, so it lives with the other agent-produced data:
   `~/.local/state/gem-agent/memory/global/` and
   `memory/projects/<escaped-path>/`. Nothing is written into the
   project tree — ADR-0009's reasoning (a file appearing in the repo as
   the side effect of a keystroke is a surprise) applies with more
   force when the writer is the model, and a repo-carried memory would
   arrive with a clone, author unknown. The path-escaping is lossy
   (`/`→`-`), so each project directory carries a `.project` marker
   with the real path; a mismatch means a collision, and that project's
   memories are skipped with a note rather than misattributed.
   `~/.claude` is never read (the ADR-0011 principle); Claude Code's
   memory format is also structurally different (index + frontmatter),
   so there is no symlink-sharing story here.
3. **Full injection under a budget.** All memories ride the system
   prompt — global first, then project, alphabetical within scope (a
   deterministic order also keeps the prompt prefix stable for
   ADR-0018's implicit caching). The same budget mechanism as the
   instruction files bounds them: per-memory cap (truncated with a
   marker — only reachable by hand-editing, since save enforces the cap)
   and a total cap (overflow skipped with a note). The skills-style
   index-plus-on-demand-load was rejected: memories are short facts
   where the description would *be* the content, so the indirection
   doubles round-trips and saves nothing. Revisit if real memory
   outgrows the budget.
4. **Writes are the trust boundary.** `save_memory` and `delete_memory`
   are Mutating and approval-gated by default; the rule tier classifies
   them Review, never Safe — memory is a *persistence vector* for
   prompt injection (a poisoned tool result that talks the model into
   remembering an instruction becomes trusted-looking context in every
   later session). MITL at write time is the defence, and the ADR-0008
   policy is the operator's deliberate relaxation. The injected section
   is framed as what it is: recorded by the agent in past sessions,
   background knowledge rather than instructions, possibly stale. It is
   not nonce-wrapped — wrapped-as-untrusted "memory" the model must not
   act on would be self-defeating (the load_skill argument) — but it is
   also not granted the operator-authored standing of AGENTS.md; the
   framing line states the difference.
5. **Startup snapshot.** Memory is read once when the session starts;
   a save is acknowledged as taking effect from the next session (the
   model already knows the fact in the conversation that saved it).
   This also means the system prompt never changes mid-session, which
   ADR-0018's cache prefix depends on. `/memory` lists what is on disk
   right now, with the loaded-at-startup caveat stated.

## Consequences

- Continuity across sessions without hand-maintaining instruction
  files; the operator reviews each write instead.
- Two new gated tools, one slash command, one banner line. No config
  knobs — the limits are fixed defaults, and gating is tuned through
  the existing ADR-0008 policy.
- One-shot `-p` mode recalls memory but cannot save it (mutating tools
  are denied there), unless the operator's policy says `"never"`.
- Memory files are plain markdown in known directories: auditable,
  editable, and deletable by hand.

## Alternatives considered

- **Project memory inside the repository** (`.gem-agent/memory/`) —
  rejected: repo pollution, gitignore management, and clone-borne
  memories of unknown authorship (§2).
- **Index + on-demand load** (the skills pattern) — rejected for
  round-trip cost on short facts (§3).
- **Nonce-wrapping the injected section** — rejected as self-defeating;
  the write-time MITL gate is the boundary (§4).
- **Reading Claude Code's memory directory** — rejected on the ADR-0011
  principle and on format mismatch (§2).

## References

- ADR-0008 (the policy that relaxes the write gate)
- ADR-0009 (nothing machine-written goes into the repository)
- ADR-0010/0011 (trust standing of operator-authored vs other content)
- ADR-0018 (why deterministic ordering and the startup snapshot matter)

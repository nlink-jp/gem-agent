# ADR-0113: three limits an audit found are accepted, with their reasons

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-09) |
| Date | 2026-11-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A repository audit (code-audit, 2026-11-09) rated these medium; the maintainers reviewed them against who must act for each to happen and what was already decided |
| Amends | ADR-0023 §2, ADR-0072 §3, ADR-0108 (residuals) |
| Relates to | ADR-0044 (pre-tool hooks), ADR-0074 and ADR-0099 §10 (persistent files, moved-in directories), ADR-0110 (the same test applied to a pipe) |

## Context

The runtime guards what runs without being asked, and records as a residual what needs an approved or operator's act. The audit's three findings below fall on the second side or were already decided, but the reasons were incomplete or stated where the operator does not read them.

## Decision

1. **The reads inside `agentic_file_search` have no hook and no per-tool policy.** ADR-0072 §3 accepted this for hooks because the child is read-only and refuses mutation; it said nothing of an `always` policy, and the operator's reference said both apply in every mode. The same reasoning covers both. The reference now states the exception, and that a guard which must see every read has to cover `agentic_file_search` itself. Credential reads stay refused in the child.
2. **Ancestor instruction files stay outside the trust gate.** Reading them is the drop-in behaviour the runtime exists to keep. ADR-0023's reason, that a clone cannot plant files above itself, does not hold for a session started in a subdirectory of the clone: its root `AGENTS.md` is then an ancestor, and the question is not asked when the subdirectory offers nothing itself. Starting there is the operator's act. Only instruction text crosses; the ancestor's MCP servers and skills do not load, and the approval gates and the sandbox stand. The reference now says to start at the root of a repository you do not trust. The route through a moved-in directory is ADR-0074 §3's and ADR-0099 §10's residual, and it needs an approved write-lane command and a later session opened below it.
3. **A file tool's verdict and its write can see different links.** ADR-0108 recorded this residual on the ground that `os.Root` keeps the open inside the root. That answers containment, not the floor: `.git` and `.claude` are inside the root, so a link retargeted after the verdict can land a write there that a direct call would be refused. It needs a write-lane command to have been approved earlier, a background child left running, and a change timed between the verdict and the write. It is accepted for those preconditions, and because closing it means carrying a held directory handle from the verdict to the write for one narrow race.

## Consequences

- No behaviour changes.
- A guard keyed on read tools does not cover the file-search child; the reference says so.

## Alternatives considered

- **Give the child the hook and policy** — not taken: the child's value is that a search needs no answers, and a hook on reads that must hold can name `agentic_file_search`, which it does see.
- **Stop the ancestor walk at a repository root** — not taken: it changes what every session in a monorepo loads, against the drop-in requirement.

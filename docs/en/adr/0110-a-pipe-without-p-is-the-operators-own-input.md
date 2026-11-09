# ADR-0110: a pipe without `-p` is the operator's own input

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-11-09) |
| Date | 2026-11-09 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | A repository audit (code-audit, 2026-11-09) rated as high that piped stdin without `-p` is dispatched as operator input, including unapproved `!` shell commands |
| Amends | ADR-0064 §4 (the sentence "piped stdin is never prompt text") |
| Relates to | ADR-0002 (the plain REPL fallback), ADR-0055 §3 (the script-input lane), ADR-0038/0054 (the operator's typed input) |

## Context

With a non-terminal stdin and no `-p`, gem-agent runs the plain REPL and reads the pipe through the path that handles typed input: `!` runs in the operator lane without a prompt, `/` reaches the slash commands, and other text is the turn's instruction. The audit found that an operator who pipes third-party content and omits `-p` gives its author a shell.

The behaviour is the specification: ADR-0002 made the fallback for scripting, and ADR-0055 §3 records that "non-TTY stdin *without* `-p` is the script-input lane, where stdin lines are commands, not data". What the audit called a contradiction is a loose sentence: ADR-0064 §4 and the interface reference said piped stdin is never prompt text, while ADR-0064 §4 goes on to say the stdin lines follow as they always have.

No boundary is crossed. Nothing runs unless the operator composes the pipe themselves, which is the shape of `curl … | bash`. The runtime's rule is to guard what runs without being asked and to record as a residual what needs the operator's own act.

## Decision

1. **A pipe without `-p` stays the operator's own input.** It is not a privilege boundary and is not gated. The runtime cannot tell who wrote the text, and does not try.
2. **The documents say so where the operator reads them.** The README and the interface reference state that `!` and slash commands in such a pipe run and that third-party text belongs to `-p`. The "never prompt text" rule is the `-p` form's.

## Consequences

- Residual, accepted: `data | gem-agent "…"` looks like the safe `data | gem-agent -p "…"` and is one flag from it. An operator who omits `-p` while piping third-party content runs that content as their own.
- The plain REPL's paste-safe reader joins lines already buffered together into one input, so a pipe's lines are not reliably separate inputs. This is unchanged and documented.

## Alternatives considered

- **Refuse a pipe without `-p`, with a `--script-stdin` flag for the old behaviour** — implemented and withdrawn before release. It breaks the documented script lane to guard an act the operator performs themselves, and the flag then needs its own rules (one input per line, approvals refused).
- **Disable only `!` and `/` on a pipe** — rejected: the same category of guard, against the same operator act.
- **A warning on stderr** — rejected: it arrives after the command has run, so it changes nothing.

## References

- ADR-0055 §3, ADR-0064 §4

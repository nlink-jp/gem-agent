# ADR-0011: Skills live in gem-agent's own directory, shared by choice

| Field | Value |
|-------|-------|
| Status | **Accepted** — supersedes the personal-scope location of ADR-0010 |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator, reviewing v0.6.0: reading `.claude` directly invites environment mixing — and MCP is separated while skills are not |

## Context

ADR-0010 pointed the personal skill scope straight at
`~/.claude/skills/` — Claude Code's own live directory. The operator
caught two problems with that within hours of the release.

**Environment mixing.** `~/.claude/` is another tool's running
environment. Skills installed there were installed *for Claude Code*,
and some of them assume it: its tool names, its plugins, its execution
context. gem-agent silently inheriting all of them means the fallback's
behaviour changes whenever the primary's environment does — the coupling
a fallback exists to not have. There is no way to give gem-agent a
different (smaller, or just different) skill set than Claude Code short
of uninstalling things from Claude Code itself.

**The precedent already existed and said otherwise.** For MCP, this
exact question was settled the other way: the global scope is
`~/.config/gem-agent/mcp.json` — Claude Code's *format*, gem-agent's
*location* — and only the project scope (`<project>/.mcp.json`) is
shared, because a repository's files are the project's environment, not
either tool's. ADR-0010 broke that symmetry without arguing for it.

The distinction that matters: **format compatibility is drop-in;
location sharing is coupling.** The value of ADR-0010 was the first, and
it accidentally bought the second.

## Decision

1. **The global skill scope moves to `~/.config/gem-agent/skills/`** —
   gem-agent's own directory, Claude Code's format, exactly the MCP
   arrangement. A skills-series zip unpacks there unchanged. gem-agent
   no longer reads `~/.claude/` at all, for anything.
2. **The project scope stays `<project>/.claude/skills/`**, unchanged.
   That is the repository's environment, shared by both agents on
   purpose — the same standing as `<project>/.mcp.json` and `CLAUDE.md`.
3. **Sharing with Claude Code is a symlink, made by the operator:**

   ```sh
   # share one skill
   ln -s ~/.claude/skills/meeting-notes ~/.config/gem-agent/skills/meeting-notes
   # share everything, deliberately
   ln -s ~/.claude/skills ~/.config/gem-agent/skills
   ```

   Discovery follows symlinks (a linked skill directory is discovered
   like a real one), and the read confinement of ADR-0010 §4 applies to
   the *resolved* directory, so a linked skill's supporting files work
   and its boundary still holds. Sharing everything is one line — the
   difference from v0.6.0 is that it is a line the operator wrote.
4. Scope labels follow MCP's vocabulary: `[global]` and `[project]`.

## Consequences

- The fallback's skill set is the operator's decision, per skill or
  wholesale, instead of an implicit copy of the primary's. The two
  environments can now diverge on purpose — a gem-agent-only skill, a
  Claude-Code-only skill, or full sharing are all one filesystem
  operation each.
- Anyone who relied on v0.6.0's behaviour (released earlier today, one
  known operator) restores it with the one-line full-share symlink.
- One more directory to know about. The `/skills` empty-state output
  prints both paths and the symlink recipe, so the knowledge is
  deliverable at the moment it is missing.
- ADR-0010's security reasoning — instruction-grade content, the unwrap
  exemption bounded by confined reads — is untouched. This ADR moves
  where skills are found, not what they are allowed to do.

## Alternatives considered

- **Keep reading `~/.claude/skills` as an additional scope** — rejected:
  that *is* the mixing, just with an extra scope on top. Opt-out
  defaults are how a fallback quietly inherits a primary's problems.
- **A config key pointing at Claude Code's directory** — rejected: a
  symlink does the same job with no schema, survives config rewrites,
  and is visible to `ls`, which is where an operator debugging skill
  discovery will actually look.
- **Copy-on-install tooling** (a `gem-agent skills sync` command) —
  rejected for now: divergence-by-copy invites stale copies, the exact
  failure mode the transcript/resume design refused (ADR-0005). A
  symlink shares the single source of truth; revisit only if a real
  need for diverging *contents* of one skill appears.

## References

- ADR-0010 (skills; its personal-scope location clause is superseded
  here, everything else stands)
- The MCP two-scope precedent (`~/.config/gem-agent/mcp.json` +
  `<project>/.mcp.json`) this brings skills into line with

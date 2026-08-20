# ADR-0023: Startup safety — broad roots and first-run project trust

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-20 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: confirm before starting in / or ~; ask before trusting a new project's AGENTS.md/CLAUDE.md/GEMINI.md/AGENT.md |

## Context

Two startup gaps. First, the working directory becomes the confinement
root: launched in `~` or `/`, "confined to the project" quietly means
the file tools read and write, and the sandbox permits shell writes
across, the operator's entire home — or everything. Second, a freshly
checked-out repository's agent-facing files are honoured unconditionally
on first launch: its instruction files are injected as *instructions*
(the trusted tier), its `.mcp.json` **starts child processes**, and its
`.claude/skills` load as operator-tier content via `load_skill`.
ADR-0008 already refuses approval-loosening from untrusted projects;
"clone and inspect" was still one `cd` away from instruction injection
and process execution.

## Decision

1. **A broad root requires interactive confirmation.** Broad means the
   filesystem root, the home directory, or an ancestor of home. The
   prompt names the consequence (file tools and shell writes span the
   whole tree) and defaults to no; non-interactively (`-p`, pipes) a
   broad root is refused with an error naming the interactive path.
   The answer is deliberately not persisted — launching there is rare
   and one keystroke is cheap.
2. **One first-run trust question covers everything a project
   provides.** Gating only the instruction files would be false
   comfort while `.mcp.json` still executed on sight, so the gate spans
   the project's own instruction files, its `.mcp.json`, and its
   `.claude/skills`, listed in the prompt with what each implies
   (server entries start child processes). Ancestor instruction files
   and all global configuration are outside the gate: a clone cannot
   plant files in directories the operator owns above it.
3. **The answer persists once per project** in the machine-owned
   policy.toml (`[projects."<dir>"] trust = "granted" | "declined"`) —
   the ADR-0009 home for machine-written per-project state. Declining
   still starts the session: the project's instructions, MCP servers,
   and skills are simply not loaded, and the banner says so with the
   file to edit for a re-ask. Content-hash re-asking (known_hosts
   style) was offered and declined: the operator edits their own ~40
   repositories' instruction files constantly, and a prompt per edit
   is friction without a matching threat.
4. **Hand-declared trust is stronger:** a project listed in
   `[approval].trusted_projects` (the ADR-0008 loosening allowlist) is
   trusted without asking. A project that provides none of the gated
   files asks nothing and records nothing.
5. **Non-interactive runs in an undecided project start bare** — no
   injection, no project MCP, no project skills, note on stderr, no
   decision recorded. Refusing instead would break read-only `-p`
   pipelines over freshly cloned repositories, which is a legitimate
   inspection workflow; bare is the safe version of it.

## Consequences

- `cd /tmp/clone && gem-agent` asks one question before the clone's
  files reach the prompt or spawn anything; `gem-agent -p` over the
  same clone runs with nothing of the clone's loaded.
- First launch in each existing project asks once (accepted friction).
- The prompt reads a single line unbuffered from stdin before the TUI
  starts, so no typed-ahead input is stranded in a discarded buffer.

## Alternatives considered

- **Instruction files only** (as first proposed) — widened to cover
  `.mcp.json` and skills (§2): the narrow gate guarded the weaker
  channel and left process execution open.
- **Content-hash re-ask** — offered, declined (§3).
- **Refusing non-interactive untrusted runs** — rejected (§5).
- **Persisting the broad-root answer** — rejected (§1).

## References

- ADR-0008 (trusted_projects: the loosening allowlist this composes with)
- ADR-0009 (the machine-owned file the decision persists in)
- ADR-0010/0011 (why skill content is instruction-tier, hence gated)

# ADR-0001: Sandbox mechanism — sandbox-exec + MITL two-layer defense

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-18 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | The `shell_exec` isolation mechanism must be chosen before the agent loop is implemented (development Phase 1) |

*Amended by ADR-0073: the one profile became three lanes — read, write,
operator — chosen per call and enforced by the same mechanism; a
read-lane `shell_exec`, verified at startup, is non-mutating and runs
without the MITL prompt; unconfined execution (`--no-sandbox`) is an
operator-only mode rather than a degraded default.*

## Context

gem-agent executes LLM-proposed shell commands on the operator's machine. An
external review of agent-skeleton (the org's agent PoC) established that
heuristic path validation (PathGuard-style) cannot stop dynamic path
construction — `$(echo /etc/passwd)` and equivalents pass any string-level
check. A structural, OS-level boundary is required in addition to human
approval.

The tool is a backup for Claude Code: it must start instantly during an
outage, on macOS, with no infrastructure warm-up.

## Decision

Two layers, with distinct roles:

1. **Primary defense — MITL approval gates.** `write_file` / `edit_file` /
   `shell_exec` / MCP tools require per-call human approval, with a
   session-scoped (non-persisted) allowlist. This is the decision boundary.
2. **Defense-in-depth — sandbox-exec (Seatbelt).** `shell_exec` children run
   under an SBPL profile that restricts file writes to the project directory
   plus a scratch area. This is the containment boundary for what approval
   cannot foresee.

`--no-sandbox` exists for debugging only and prints a startup warning.

## Consequences

- gem-agent is macOS-only. This is accepted and documented as a design
  constraint, not a limitation to fix.
- sandbox-exec is deprecated by Apple while remaining the de facto standard
  (Claude Code and comparable agents use it). If a future macOS removes it,
  the fallback path is container-based isolation or Apple's Containerization
  framework — recorded as a platform risk in the RFP §7.
- The SBPL profile is generated per session (project path is dynamic) and the
  generator must be unit-tested.

## Alternatives considered

- **Container isolation (podman)** — shell-agent-v2's opt-in sandbox.
  Stronger boundary, but machine setup (Podman Machine) and container startup
  are too heavy for a backup tool that must work instantly during an outage.
  Rejected as the default; not precluded as a future opt-in.
- **Heuristic path validation only (PathGuard)** — already assessed as
  insufficient by the agent-skeleton external review (dynamic path
  construction bypasses it). Rejected as a sole mechanism; argument-level
  validation still happens before approval prompts for readability.
- **App Sandbox (entitlement-based)** — designed for bundled GUI apps
  distributed via signed entitlements; unsuited to a developer CLI that must
  read arbitrary project directories. Rejected.
- **macOS Containerization framework (macOS 26+)** — promising successor,
  but ties the tool to the newest OS and adds VM startup latency. Recorded as
  a future candidate, not adopted now.

## References

- RFP §3 (security design), §7 (platform constraints) —
  `docs/en/gem-agent-rfp.md`
- agent-skeleton external review backlog item 3 (sandboxed execution)
- shell-agent-v2 container sandbox (design source for the rejected alternative)

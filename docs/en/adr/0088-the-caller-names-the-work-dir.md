# ADR-0088: The caller names the work directory — attach `_meta` to every tools/call

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-13) |
| Date | 2026-09-13 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Organization ADR-021 (the work-directory contract for file-mediated MCP servers) settling, and ten fleet servers coming to require `work_dir`. The contract places an obligation on the calling side too |
| Relates to | ADR-0058 (the intake that spills large MCP results into the work directory), ADR-0071 (the three exports children see), ADR-0087 (the runtime hides only its own) |

## Context

Organization ADR-021 settled that a server which produces files takes its
destination as a **per-call argument**, `work_dir`. Measurement of the four
calling runtimes showed why: neither MCP `roots` nor the environment reaches
half of them, and the per-call argument is the only common channel.

The contract has a second channel — the request's
`_meta["jp.nlink/work_dir"]` — and it exists **for the runtimes we write
ourselves**: it is schema-blind, so it can be attached to every `tools/call`
without knowing any tool's schema, and a model that forgets the argument still
leaves the server with a usable destination.

This runtime already exports `GEMAGENT_WORK_DIR` (ADR-0071), but that is an
environment variable for child processes, **not a protocol value**. For a server
to read it, the registration entry has to carry `${GEMAGENT_WORK_DIR}` — and in
the sibling runtime an undefined variable expands silently to the empty string.
`_meta` removes that arrangement.

## Decision

1. `mcp.Client` carries the session work directory and **attaches
   `jp.nlink/work_dir` to `params._meta` on every `tools/call`**.
2. **A session with no work directory attaches nothing.** An empty hint is worse
   than none: a server would take it for an answer.
3. The value is a `NewStdio` parameter — not a global, not a setter. It does not
   change while the client lives, and it should be visible at the call site.
4. **The model's own argument always wins.** A server reads `_meta` only when the
   argument was absent (ADR-021 §2's resolution order). The runtime supplies a
   default; it does not overwrite the caller's intent.

## Consequences

- The ten in-house servers write into this session's work directory even when the
  model omits `work_dir`. Claude Code and Codex still have to pass the argument,
  which is unchanged.
- chrome-pilot's registration no longer needs
  `--workspace-root ${GEMAGENT_WORK_DIR}` — the only line that made gem-agent's
  and lagent's `mcp.json` two separate files.
- A server that does not know the key ignores it (github, obsidian, …). That is
  what MCP's `_meta` is for.

## References

- Organization ADR-021 §2 and §9; ADR-0058 (intake); ADR-0071 (the three exports)

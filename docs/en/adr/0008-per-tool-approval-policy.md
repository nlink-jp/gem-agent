# ADR-0008: Per-tool approval policy, global and per project

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator request: set MITL per function — built-ins and MCP tools alike — in both a global and a project scope |

## Context

Today the gate is decided by one bit: `Mutating`. Built-in writes and
`shell_exec` ask; `list_files` and `read_file` do not; **every MCP tool
asks, always**, because the client cannot know what a server's tool does.

That last rule is where the friction is. A read-only lookup — "is this IP
a Tor exit node?" — is indistinguishable, to the client, from a tool that
posts to production. So it asks, every call, and in a session doing
indicator triage it asks constantly. Auto-approve (ADR-0004) helps only
if the operator turned it on, and it spends an LLM round deciding what
the operator already knows about their own servers.

The information gap is real and the client cannot close it. But the
operator can: they know their servers. What is missing is a place to say
so.

## Decision

A per-tool approval policy, in two scopes.

```toml
# ~/.config/gem-agent/config.toml
[approval.tools]
"mcp__tor-exit-lookup__*" = "never"   # a read-only lookup server
"shell_exec"              = "always"  # even in auto mode
```

```toml
# <project>/.gem-agent.toml
[approval.tools]
"write_file" = "always"
```

1. **Two values, and unset.**
   - `"always"` — always ask, in every mode. A per-tool floor the
     operator sets, the counterpart of ADR-0004's rule-tier Block.
   - `"never"` — never ask, in every mode. Including manual mode: if it
     only applied under auto-approve, it would not answer the request,
     which is about ordinary sessions too.
   - unset — today's behaviour: mutating tools ask, auto mode runs them
     through the ladder.
2. **`never` does not lift the Block floor.** A tool whose effect varies
   per call — `shell_exec` above all — is still checked by the pure rule
   classifier, and a Block verdict (`rm -rf`, `sudo`, `curl … | sh`,
   credential paths, out-of-project writes) still asks. Without this,
   `shell_exec = "never"` would be a blanket "run anything unattended",
   which is not a setting worth offering. The model tier is skipped
   entirely under `never`: the point is to stop paying for a decision the
   operator already made.
3. **Matching**: exact tool name, or a trailing `*` prefix — the whole
   point of which is per-server policy for MCP (`mcp__server__*`). Exact
   beats wildcard; longer wildcard beats shorter. **A bare `"*"` is a
   config error**, not a shortcut: disarming everything is what
   `--no-sandbox`-grade options look like, and it should not be reachable
   by a one-character typo in a policy list.
4. **Scopes, and the direction each may move.** The project file may
   **tighten freely** — anyone may ask for more approvals. It may
   **loosen only in a project the operator has trusted**, by path, in the
   global config:

   ```toml
   [approval]
   trusted_projects = ["/Users/…/work/my-repo"]
   ```

   In an untrusted project, `"never"` entries are **ignored and
   reported** at startup, naming the tools and the exact line to add if
   the operator does want them.

   This is the asymmetry the design turns on. A project directory's
   contents are not necessarily authored by the operator — cloning a
   repository to look at it is a normal thing to do, and for the person
   this tool was built for it is a normal thing to do with *hostile*
   code. A `.gem-agent.toml` that silently switched off approval for
   `shell_exec` would make "clone and inspect" a code-execution
   primitive. The sandbox still confines writes, but a shell running
   unattended can read and send anything the operator can read.

   Tightening needs no trust: a hostile repository asking for *more*
   confirmation is not an attack worth defending against.
5. Both files are strict-decode. An unknown key in a policy file is an
   error, not a silently ignored line — a policy that does not do what it
   says is worse than no policy.

## Consequences

- The common friction — an always-gated read-only MCP server — is fixed
  by one line, without turning on auto-approve and without an LLM round
  per call.
- `always` gives the operator a floor the model cannot lift, symmetric
  with ADR-0004's Block tier. `shell_exec = "always"` is a reasonable
  thing to keep in a global config forever.
- The trust list is one more thing to maintain, and the first time a
  project's `"never"` entries are ignored will be mildly annoying. The
  startup note prints the line to paste, so the cost is one paste, once,
  per project — paid in exchange for cloned repositories not being able
  to disarm the gate.
- ADR-0001 is amended a second time: MITL is "primary defense, default
  mode" (ADR-0004) and now also "per tool, by operator policy". The
  sandbox is unchanged and applies in every mode, as does the
  project-directory confinement.

## Alternatives considered

- **Project file may loosen freely** — rejected: it makes `git clone`
  plus `gem-agent` a way to run unattended shell commands. The literal
  request was for a project scope, and it has one; what it does not have
  is authority to weaken a defense in a directory whose contents the
  operator may not have written.
- **Prompt once per project ("this project reduces approvals — trust
  it?")**, VS Code style — rejected for now: it needs a persisted trust
  store and an interactive path that one-shot and piped mode do not have
  (both would have to fail closed anyway). The global list does the same
  job with no new state file and is auditable in one place. Worth
  revisiting if editing the list becomes the friction.
- **Per-tool policy only in the global config** — rejected: it answers
  half the request, and per-project policy is genuinely useful for the
  tightening direction (a repository that wants every write confirmed).
- **A `risk`/`ladder` third value** — rejected as redundant: that is what
  unset already means under auto mode.
- **Marking MCP servers read-only in `.mcp.json`** — rejected: that file
  is Claude Code's format, read as-is for drop-in compatibility.
  Inventing fields in someone else's schema breaks the one property that
  makes it worth reading.

## References

- ADR-0001 (MITL primary defense; amended here, as ADR-0004 amended it)
- ADR-0004 (auto-approve; its Block floor is preserved under `never`, and
  its model tier is skipped)

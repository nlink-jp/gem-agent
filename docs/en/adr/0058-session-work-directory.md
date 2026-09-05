# ADR-0058: The session work directory, and who decides a result is too big

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-31 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "shouldn't a session have a work directory of its own, separate from the project, with the sandbox spanning both?" |
| Amends | ADR-0001 (sandbox write roots), ADR-0012 §4 (where file-mediated MCP output lands), ADR-0020/0022 (state layout) |

*Amended by ADR-0075: the intake no longer prefixes a server-reported
error with `error:` itself; the MCP adapter returns a typed
`RemoteError` and the executor renders the failure with its provenance.
Budget and spill are unchanged.*

## Context

Two facts, found while designing the work directory, turned out to
matter more than the directory itself.

**MCP results were never bounded.** Built-in tool results have always
been cut at `tools.OutputCap` (20,000 bytes) — the comment on that
constant calls unbounded tool output "the primary context-explosion
failure mode for agent loops". MCP results bypassed it entirely:
`cmd/mcp.go` returned `client.CallTool`'s string straight into the
conversation. Every file-mediated server in the nlink-jp fleet — the
lookup group, splunk-mcp — had grown a `workspace_root` argument to
keep from flooding a context it has no way to measure. The guard was
being implemented seven times over, in the seven processes least able
to do it: **a server cannot know the model's context window.** This
side can.

**Non-text content was being thrown away.** `CallTool` flattened every
block that was not text to `[non-text content: image]`.
`chrome-pilot-mcp` already returns MCP image content under a 4 MiB
budget (`internal/tools/debug.go`), so screenshots taken through
gem-agent were invisible to the model — not degraded, gone.

Against that, the original problem stands: a session needs somewhere to
put what is not part of the project. The two available places are both
wrong. The **project directory** is the operator's source tree, and
writing intermediates there makes a git working copy dirty with things
nobody asked for. **`/tmp`** is one flat namespace shared with every
process on the machine (327 entries on this one, `drwxrwxrwt`), with no
session isolation and no way for a resumed session to find what its
earlier self produced.

## Decision

### 1. A work directory per session

`<state root>/<escaped project>/work/<session id>/`, on the same state
root as transcripts and memory (ADR-0020/0022). `GEMAGENT_STATE_DIR`
therefore isolates it for tests and drills exactly as it isolates
those, and keying by session id means a `--resume` lands back in the
directory its earlier self used.

It is a **writable root of the sandbox profile** alongside the project,
and a **second root of the built-in file tools**. The second half is
not optional: a model that can see a path and not open it routes around
the file tools with shell redirection, which is less reviewable, not
more contained. A relative path still resolves against the project —
the work directory is reached by the absolute path the prompt names, so
`report.md` keeps meaning the project file it has always meant.

### 2. The client decides what is too big, and spills instead of cutting

`internal/mcp.CallTool` now returns the content blocks whole; the
adapter in `cmd/mcpresult.go` renders them. Text past `OutputCap` is
**written to the work directory** and the model gets the head plus the
path — not a truncation. Non-text blocks are written the same way.

Files are named from the server, the tool, and a hash of the content,
so the same answer fetched twice occupies one file and no clock or
counter is needed to keep names unique.

### 3. Binary comes back as a path, not as an attachment

An image is saved and the model is told to call `view_image` on it.
The bytes deliberately do not ride back inline, even though the
machinery exists (ADR-0012 §5): an attachment is replayed with the
whole conversation every round, which is the exact cost ADR-0027
created the media bucket to avoid. Making the look deliberate keeps
media out of history until the model asks for it — and that is the
IR flow ADR-0012 designed (scan → screenshot → look), which works
again now that the work directory is readable.

### 4. Startup order

The session id is now resolved **before** the MCP servers start. It has
to be: the work directory is keyed by it, the sandbox profile needs the
path, and `${GEMAGENT_WORK_DIR}` in an `mcp.json` args entry is expanded
from the process environment at load time (`internal/mcp/config.go`
already does this, matching Claude Code). The session-log block moved
ahead of `connectMCPServers`; it depends on neither the registry nor
memory nor MCP, so the move is mechanical.

`GEMAGENT_WORK_DIR` is **exported into the process environment** rather
than passed to anything. That is what puts it in front of `shell_exec`'s
child, every MCP server (`internal/mcp` inherits `os.Environ`), and
every hook, without any of them needing to know gem-agent's layout.

### 5. Nothing is deleted automatically

The files are the point — a report the model produced, a screenshot it
was told to look at. Startup **reports** how many earlier work
directories exist and how many bytes they hold; removal is the
operator's. The one exception is a non-recursive `rmdir` of a directory
the session left empty, which cannot remove anything and keeps an
unused session from leaving a trace.

## Consequences

- Oversized MCP results stop entering context, and stop being lost:
  before this, `read_file` was capped and truncated, and MCP was
  uncapped. Now both spill.
- Screenshots from `chrome-pilot-mcp` are reachable for the first time.
- The servers' own `workspace_root` mechanism becomes unnecessary **for
  gem-agent**, which is what allowed the lookup group to drop it. It
  remains correct for artifacts that cannot ride in a JSON result at
  all — audio, video, a PDF a tool renders.
- `tools.OutputCap` is exported. One number, two callers.
- With no work directory (a session whose transcript is disabled), the
  fallback is a truncation **that says the rest is lost**. Losing part
  of an answer silently is the one outcome this must not produce.

## Alternatives considered

- **Widen `Tool.Run` to carry attachments**, so MCP images become
  inline parts automatically. Rejected twice over: it touches every one
  of ~20 tool definitions and every return inside them, and the result
  would replay media through history on every round — the cost ADR-0027
  exists to avoid. The `view_image` round trip is one extra call and
  keeps the decision with the model.
- **Point the MCP servers at the work directory** with
  `--workspace-root ${GEMAGENT_WORK_DIR}` and keep their file
  mediation. This works and needs no code here, but it leaves the
  "too big for a model" judgement in the server, keeps every server
  dependent on the client owning a filesystem it can name, and does
  nothing for a server that does not have the flag.
- **Remove `/private/tmp` and `TMPDIR` from the sandbox write set** and
  point children at `<work dir>/tmp`. The boundary would be
  meaningfully tighter — today the work directory is an *addition* to
  an already-wide allowance. Deferred deliberately: programs that
  hardcode `/tmp` would break, and that needs measuring on real work
  before it is imposed. Recorded here so it is not lost.
- **Delete old work directories on a schedule.** Rejected: a recursive
  delete driven by a pattern, over files the operator may not have
  looked at yet, is not a decision an agent makes on its own.

## References

- `internal/workdir` — layout, sweep, empty-directory removal
- `cmd/mcpresult.go` — the intake: what is inlined, what is saved
- ADR-0012 §4–5 (image input), ADR-0027 (media bucket), ADR-0001
  (sandbox), ADR-0020/0022 (state layout)

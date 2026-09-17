# ADR-0090: an image's bytes reach the screen without ever becoming a path

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-17) — implemented |
| Date | 2026-09-17 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | [ADR-0089](0089-inline-images-declare-their-height.md) §5 deferring the source: it settled how many rows an image costs and refused to name what may be drawn, because the question had been answered three times and refuted three times |
| Relates to | ADR-0089 (the declared box and the accounting), [ADR-0058](0058-session-work-directory.md) (where file-mediated MCP output lands), [ADR-0085](0085-credential-reads-are-operator-only.md) / [ADR-0086](0086-the-kernel-reads-the-file.md) (who may open a file) |

## Context

ADR-0089 built the lane and left the source open. Its §5 records three
drafts and three refutations, and one constraint that survived all of them:
**the view layer opens no file.** A read the view layer performs is not a
tool call, so it reaches neither `Agent.decide` nor ADR-0086's sandboxed
child, and the credential list — the `credentialReadTools` map, keyed on built-in tool
name ([risk.go:176](../../../internal/risk/risk.go)) — cannot see it.

That constraint rules out the obvious design. The MCP intake already writes
an image into the session work directory and hands the model
`[image saved at <path> … use view_image on that path]`
([mcpresult.go:224](../../../cmd/mcpresult.go)), so a path is sitting right
there — and `write` short-circuits on `os.Stat`
([mcpresult.go:247](../../../cmd/mcpresult.go)) while every call hands the
server the work directory as `_meta[workdir.MetaKey]`
([client.go:579](../../../internal/mcp/client.go)). A local server child
therefore knows its own name, its tool name, the bytes it will return and
the directory, so it can plant a symlink at the content-addressed name
before answering; the runtime then writes nothing and the path resolves
where the server chose. Reaching that file through `view_image` is
contained, because the agent resolves symlinks before the enforcers judge
the real path. A view layer that opened it would not be.

### What the plumbing actually is

The third refuted draft said the view layer would be handed "the bytes the
intake already holds". It cannot: `render` returns a `string`
([mcpresult.go:63](../../../cmd/mcpresult.go)), `mcpIntake` keeps a
work-directory getter, a byte cap and a preview length — and, since this
decision, the sink; what it has never kept is the bytes
([mcpresult.go:56](../../../cmd/mcpresult.go)), and the tool contract is
`Run func(ctx, args) (string, error)`
([tools.go:65](../../../internal/tools/tools.go)). After `Run` returns, the
blocks are gone.

But the runtime is not short of a channel. The agent loop already talks to
the UI **during** a tool call — `prog.Send(tui.ToolCall{…})` at
[root.go:944](../../../cmd/root.go) — so an out-of-band route from a tool
result to the screen is an existing, working pattern rather than a new
mechanism. Nothing about the string contract has to move.

### What an image costs on the way

Measured on this machine, feeding real payloads through the counter the
emit path uses (`physicalRows`, which strips ANSI and walks graphemes):

| decoded image | payload | `physicalRows` |
|---|---|---|
| 64 KiB | 85 KiB | 0.35 ms |
| 256 KiB | 341 KiB | 1.19 ms |
| 1 MiB | 1.3 MiB | 3.7 ms |
| 4 MiB | 5.3 MiB | 14.4 ms |
| 8 MiB | 10.7 MiB | 29.2 ms |

Linear, about 3.6 ms per MiB, and that is one of three places the string
lives at once — the emit path, Bubble Tea's queued-message buffer and the
terminal's scrollback. Nothing bounds it today but the JSON-RPC frame cap
(`scannerMax = 10 MiB`, [client.go:28](../../../internal/mcp/client.go)),
which allows roughly 7.5 MiB decoded per block.

## Decision

### 1. The bytes travel out-of-band, on the channel the UI already has

`mcpIntake` gains one optional dependency beside `workDir`: a sink the
runtime supplies, called with the decoded bytes and their MIME type at the
moment the block is taken in. `cmd` wires it to `prog.Send`, the same way
`ToolCall` and `ToolDone` already reach the UI mid-call. The tool result
stays a `string` and the transcript, resume and error paths are untouched.

The sink is **inert** in every entrance that is not an interactive TUI.
It cannot simply be nil there: the MCP servers connect before the runtime
knows whether it has a UI, so the intake is handed a late-bound route
(`tui.Screen`, the shape `tui.Gate` already uses for approvals) which is
bound to the program only if one is built. Unbound, it drops what it is
given. An earlier draft of this record promised nil, which the ordering
does not allow.

### 2. An image is drawn if and only if the intake saved and described it

One condition, not two. A block whose note does not fit the response
budget is already neither saved nor described individually — the guard
sizes `binaryNote` before anything is written
([mcpresult.go:115](../../../cmd/mcpresult.go)) — and is counted into a
leftovers line. Such a block is **not drawn** either.

The alternative — drawing a picture the model was never told about, from a
call whose result was truncated — puts something on the operator's screen
that appears nowhere in the session's record. The screen mirrors what the
session recorded, and that is one rule rather than a second budget.

### 3. The box comes from the picture, the clamp comes from the terminal

`image.DecodeConfig` (standard library) gives the pixel dimensions without
decoding the picture. The rows are chosen from the aspect ratio against a
ceiling of `maxRows`, the columns follow, and `termimg.Fit` clamps the
width below the terminal's — a box wider than the terminal invites the
terminal to wrap the picture and add rows the declaration never claimed
(ADR-0089 §2).

`DecodeConfig` is also the validator: a block whose MIME says `image/*` but
whose bytes do not decode is **not** an image and is not drawn. The MIME
type is the one part of the block a server chooses freely, and it selects a
container, not an escape — the payload itself is base64, whose alphabet
holds no `ESC` and no `BEL`, so bytes reaching the view layer cannot break
out of the escape they are wrapped in.

### 4. Two megabytes, decoded, and the reason is the measurement

A block whose decoded bytes exceed **2 MiB** is saved and described as
usual and is not drawn. At that size the counter costs about 7 ms and the
string is held in three places at once; at the frame cap it is four times
that. The bound is on the decoded bytes because that is what the cost
tracks, and it is checked before any payload is built.

This is a ceiling, not a target. The picture that reaches a terminal is
scaled into a box of at most a screenful of cells, so a larger original is
pixels the terminal discards — the operator loses nothing but the wait.

### 5. Nothing here changes what the model sees

The note, the path and the `view_image` route are exactly as they were. The
model's access to the picture still goes through a tool whose verdict the
enforcers take, with symlinks resolved first. This decision adds a second
audience — the operator — and gives it a channel that opens no file.

## Consequences

- An MCP server's screenshot appears on screen as it is taken, and the
  model still has to ask for it if it wants to look.
- The symlink pre-plant is not repaired by this and is not made worse by
  it: the runtime still writes the file and still hands the model a path,
  and `view_image` still resolves before the enforcers judge. What changes
  is that nothing new opens that path.
- A server can now put a picture on the operator's screen. It could
  already put arbitrary bytes in the transcript, and the image is drawn
  into a box this runtime declares, but it is a surface that did not exist
  before and should be named as one.
- The 2 MiB ceiling will refuse some images an operator wanted. The refusal
  is silent on screen by design — a warning per oversized block is the
  "report is not a control" shape — and the note the model gets is
  unchanged, so the picture is still reachable through `view_image`.
- lagent has the same intake, the same tool contract and the same UI
  channel. This side was implemented first because the lane is; the mirror
  landed as lagent ADR-0020 / ADR-0021, carrying the erase this side's first
  real-terminal run made necessary.

## Alternatives considered

**A1. Widen `Tool.Run` to return structured content.** Rejected: the
string result is what the transcript stores, what resume replays and what
`RemoteError` carries. Widening it to serve one display case changes three
subsystems for a fourth's convenience, and ADR-0089 §5 was refuted three
times for exactly this kind of unenumerated reach.

**A2. Let the view layer read the saved path.** Rejected — it is the
constraint ADR-0089 §5 earned, and the `os.Stat` short-circuit makes the
path a thing a server can choose.

**A3. Draw every image block, budget or not.** Rejected: it puts a picture
on the screen that the session's record does not contain, and it makes the
response budget bound text while images pass freely.

**A4. Bound by pixels rather than bytes.** Rejected as the primary bound:
the measured cost tracks bytes, not pixels, and a small-dimension image can
carry large bytes. `DecodeConfig`'s dimensions are still read — decision 3
needs the aspect ratio — so a pixel bound could be added later on evidence
this record does not have.

**A5. Warn when an image is refused.** Rejected: a line printed on every
oversized block is a report, not a control, and the operator's next action
does not change because of it. The model's note is unchanged and the
picture stays reachable.

## References

- ADR-0089 §5 (the three refuted drafts and the constraint), §2 (the
  declared box and the clamp)
- ADR-0058 (where file-mediated MCP output lands), ADR-0085 / ADR-0086 (who
  may open a file)
- The cost table above: payloads built with `internal/termimg` and fed to
  `internal/tui`'s own `physicalRows`, 20 runs per size

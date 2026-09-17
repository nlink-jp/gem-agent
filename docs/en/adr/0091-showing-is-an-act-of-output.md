# ADR-0091: showing an image is an act of output, not a side effect of a tool result

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-17) |
| Date | 2026-09-17 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "an MCP image block is the mechanism for delivering an image to the MODEL — it was not returned in order to display it", and, before that, "what the model outputs to the operator is the chat message; an inline image is that too" |
| Relates to | [ADR-0089](0089-inline-images-declare-their-height.md) (the lane, the declared box and the accounting — unchanged), [ADR-0090](0090-an-images-bytes-never-become-a-path.md) (**§2's source is withdrawn here**; §1's plumbing stands), [ADR-0012](0012-image-input.md) (how an image reaches the model, and the operator's own bare-path grammar) |

## Context

ADR-0020 built a lane: an emitter declares a box, the row counter is told that
number instead of measuring bytes it cannot see, and an image line erases below
itself. That decision is measured, implemented and verified on a real terminal,
and nothing here touches it.

ADR-0090 then named the source: the MCP intake draws every image block it both
saved and described. That is the part this record withdraws, for three reasons
that arrived in the order below and that each survive on their own.

### 1. The block is addressed to the model, and nobody said otherwise

An MCP image content block is how a tool result carries an image **into the
model's context**. MCP has a separate way to say who a piece of content is for
— the `audience` annotation on a content block, which names `user`,
`assistant`, or both. This runtime never reads it: `Content` carries
`Type`, `Text`, `Data` and `MIME` and nothing else
([client.go:546](../../../internal/mcp/client.go)), so an annotation is dropped
at the parser.

Measured across the 24 servers this operator has registered, **four can emit an
image block at all** — a browser screenshot tool, a URL-scan screenshot tool, a
data workspace's artifact hand-back, and a third-party vault reader — and
**none of them sets an audience**. So the drawing condition was never "the
server asked for this to be shown". It was this runtime inferring "an image
arrived, therefore the operator wants to see it", which is the shape the
recorded rule refuses: intent is declared, not inferred.

### 2. What reaches the operator's screen is authored, and the intake authors nothing

Two of those four servers return a screenshot **so that the model can look at
it**. Under ADR-0021 every such inspection also put a full-size picture into
the operator's scrollback — output neither the operator asked for nor the model
chose to emit, competing for the screen with the model's own reply.

The operator's framing settles it: what the model says to the operator is the
chat message, and an inline image is the same act. A picture on the screen has
an author, and the intake is not one. It is a postal sorting office reading the
mail.

### 3. The route that existed did not serve the case that exists

The common case is an artifact the operator asked for: a generated image. The
in-house generator returns a **path**, under the work-directory contract that
this organization settled and that this runtime's own MCP client honours — so
the intake route never fires for it, and never could.

Asked to show such a file, the model has no route to the screen at all, so it
reaches for the one thing that looks like a display: `shell_exec` with `open`.
The read lane denies that at the kernel — `open` is on `DefaultDenyExec`
([lane.go:125](../../../internal/sandbox/lane.go)) with the other IPC-capable
programs — so the request fails; lifted into a lane where it succeeds, it opens
a window outside the terminal, asks the operator to approve launching a GUI
program rather than to look at a picture, and does nothing at all over ssh.

A runtime that can draw, and a model that wants to show, and no way to connect
them: that is a missing route, not a misbehaving model.

## Decision

### 1. The MCP intake draws nothing

`mcpIntake` loses its sink and goes back to what it did before ADR-0021: save
the block, describe it, hand the model a path and `view_image`. **ADR-0090 §2
is withdrawn**; its §1 plumbing — the late-bound `tui.Screen`, inert until a
program exists — stays, because the sources below need exactly that route.

The architecture test that pins the sink's production wiring follows the sink:
it stops asserting the MCP call sites and starts asserting whichever call sites
carry it after this.

### 2. A tool the model calls to show the operator an image

A built-in tool — working name `show_image` — takes a path, and its effect is
that the operator sees the picture. It is the counterpart of `view_image`, and
the pair differs in exactly one thing, which is the audience:

| tool | who sees it | how it travels |
|---|---|---|
| `view_image` | the model | bytes attached to the tool result, into the context |
| `show_image` | the operator | bytes to `tui.Screen`, drawn in a declared box |

This keeps the constraint ADR-0090 was built to protect — **the view layer
opens no file** — because the file is opened by the TOOL layer, where a read is
a tool call that every enforcer already sees. `show_image` joins
`credentialReadTools` ([risk.go:176](../../../internal/risk/risk.go)) beside
`view_image`, which is the one list `JudgesPath` reads, so its argument is
resolved to a real path and classified before it runs, and it reads through
the same caged child. A model-named path is
judged; that is the whole difference from the drafts ADR-0089 §5 refuted.

The tool's result tells the model what happened — shown, or not shown and why
(no protocol, past the ceiling, not an image, no interactive UI). That is not a
report on the operator's screen; it is the tool answering its caller, and the
model needs the answer to say something useful next.

### 3. The operator can ask for a file directly

`/show <path>` draws a file the **operator** names. The symlink hazard that
rules out a runtime-chosen path does not apply to a path the operator typed:
this is the same trust line ADR-0012 already draws for `@<image>` attachments,
where operator input names a file and the runtime reads it.

This is the route for "show me that again" and for anything the model never
mentioned, and it means the operator is never blocked on the model calling a
tool.

### 4. Nothing else about drawing changes

The declared box, the erase, the columns clamped below the terminal's width,
the 2 MiB ceiling, the silent refusal on the screen, TUI-only drawing and the
capability probe are all ADR-0089's and all unchanged. This record moves the
source and nothing else.

### 5. No prompt paragraph

The tool's own description is where the model learns when to show something. A
system-prompt rule is not added here: this runtime's prompt changes only by
ADR and only after the runtime's facts are true, and a capability nobody
triggers is a measurable outcome rather than a thing to argue about — if the
bench shows `show_image` never fires where it should, that measurement is the
reason to revisit, and it is a smaller change than prose written in advance.

## Consequences

- **A server can no longer put a picture on the operator's screen.** The
  surface ADR-0021 named as new is closed again; what a server returns reaches
  the model, and the model decides what the operator sees.
- **An image the model wants to show costs a tool call.** Showing is an act
  with a round trip, which is what makes it attributable.
- **Two releases behave differently from their successors.** v0.9.0 here and
  v0.83.0 in gem-agent draw from the intake. The next version does not, and the
  changelog says so plainly rather than describing it as a refinement.
- **`view_image` and `show_image` are easy to confuse**, including for the
  model. Their descriptions must lead with the audience, not with the verb.
- **The screen is still silent on refusal**, so a `show_image` that refuses is
  visible only in the tool result — which is the model's to relay, and the
  reason §2 makes the result explicit.
- **lagent ADR-0022 is the same decision on the other side.** Neither
  runtime may hold it alone.

## Alternatives considered

**A1. Keep the intake route, gated on `audience` containing `user`.** Rejected
as the source. It is the right way to read MCP, and it settles nothing today:
the client drops annotations, and zero of the four block-emitting servers set
one, so the gate would be closed everywhere. It also leaves the choice of what
the operator sees with the server rather than with the model. If a server ever
declares an audience, this returns as an ADDITIONAL source — and then the
client has to parse annotations first.

**A2. Markers in the reply, partitioned by the renderer.** An
`![](path)` in the model's text, split out like mermaid art is in gem-agent's
lane. Rejected: the read would be performed by the view layer on a path the
model chose, which is the draft ADR-0089 §5 refuted twice over — no enforcer
sees a view-layer read, and the path can be pre-empted. The tool call in §2 is
the same intent with the read moved to where it is judged.

**A3. Make the in-house servers return image blocks as well as paths.**
Rejected as a fix for this: it changes ten servers, doubles every payload,
keeps display keyed on a server's choice rather than the model's, and still
meets the 2 MiB ceiling. The work-directory contract is not the obstacle — a
server may do both — but it is not this decision.

**A4. Let `view_image` also draw.** One tool, two audiences. Rejected: it
infers again, and a model inspecting twenty screenshots would bury the
conversation under twenty pictures.

**A5. Leave `shell_exec` + `open` as the answer.** Rejected: the read lane
denies it by design, it leaves the terminal, it fails over ssh, and the
approval it raises is for launching a GUI program — the operator would be
approving the wrong question.

## References

- [ADR-0089](0089-inline-images-declare-their-height.md) — the lane, the
  declared box, the accounting, the erase, the probe
- [ADR-0090](0090-an-images-bytes-never-become-a-path.md) — §1's plumbing
  stands; §2's source is withdrawn by this record
- [ADR-0012](0012-image-input.md) — `view_image`, and the operator's
  bare-path grammar this record reuses for `/show`
- lagent ADR-0022 — the same decision, on the other side

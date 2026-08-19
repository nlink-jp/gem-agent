# ADR-0012: Image input — operator-attached and model-viewed

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-08-19 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: multimodal support (images especially) is needed — screenshots are a constant part of the work; and mid-review: an agent also reads image files it obtained via MCP, so the operator does not always name the file |

## Context

The operator's workflow leans on screenshots twice over.

**As input**: a UI glitch, a terminal error, a design to reproduce — on
macOS these arrive on the clipboard (Cmd+Ctrl+Shift+4) or as files
outside the project (Cmd+Shift+4 writes to the Desktop), and the `@`
mechanism as shipped refuses both, one for being binary and one for
being out of tree.

**As tool output**: the org's MCP servers are file-mediated by
convention, and several of them *produce* images — urlscan-lookup's
`get_screenshot` writes the rendered page of a suspicious URL into the
workspace; pcap extraction can emit image objects. In an IR session the
step after "fetch the screenshot" is "look at it", and that look is
initiated by the model mid-loop, not by the operator naming a file.

The model is not the constraint — Gemini 3.x is natively multimodal.
Both input paths are missing. The API constraint that shapes the second
one was settled by measurement (below): a function response's classic
`response` field is structured text, but the API now accepts
**multimodal function responses** — media parts inside
`functionResponse.parts` — and that is the only shape that survives
multi-round tool loops.

## Decision

Two routes in, matched to who chooses the image.

### Operator-attached: the `@` mechanism

1. **`@path/inside/project.png`** — as today, project-confined.
2. **`@/absolute/path.png` and `@~/Desktop/shot.png`** — image files
   may be referenced from anywhere, **for image extensions only**; text
   references stay confined. The asymmetry is sound because of who can
   trigger it: `@` is parsed from the operator's typed input, never
   from model output or tool results, so an out-of-project image is
   always one a human deliberately named. If that ever changes, this
   clause is the first thing to revisit.
3. **`@clipboard`** — captures the clipboard image via macOS
   `osascript` (PNG). gem-agent is macOS-only by design (ADR-0001), so
   leaning on the platform costs nothing that was promised. An empty
   clipboard is a reported problem, never a silent no-op.

### Model-viewed: the `view_image` tool

4. A read-only built-in, `view_image(path)`, **confined to the project
   directory exactly like `read_file`** — the model-triggered route
   gets no out-of-tree exception, that belongs to operator-typed input
   only. File-mediated MCP outputs land in the workspace the agent
   passes, which is the project, so the IR flow (scan → screenshot →
   look) stays inside the boundary.
5. The pixels ride **inside the function response**, as a multimodal
   response part (`functionResponse.parts`, inline data); the text half
   of the response stays metadata. This shape was chosen by
   measurement, not preference: the obvious alternative — a user-role
   message carrying the image, appended after the tool round — worked
   for one round and then failed the next request with 400 ("number of
   function response parts must equal function call parts"), because
   Gemini requires the content after a function-call turn to consist of
   exactly its responses. The multimodal response was then verified
   live across a further tool round, thought-signature replay included,
   with a colour-neutral filename so the model could not answer from
   the name.

### Mechanics common to both

- **Attachments grow binary content** (`Data`, `MIME`) beside their
  text. Image bytes get their own budget (per-image cap, per-message
  count), separate from the text budget — a screenshot must not evict
  the source files attached beside it. MIME is sniffed from bytes, not
  trusted from the extension.
- **The LLM layer emits image parts** in user content after the text
  part; an image-only message is valid and skips the empty-content
  guard.
- **Images cannot be nonce-wrapped.** The isolation stance of ADR-0001
  gets a visual counterpart in the system prompt — text visible inside
  an image is data, never instructions — and this ADR states plainly
  that framing is weaker than tag isolation. It matters most for
  `view_image`, whose subjects include screenshots of attacker-authored
  pages; the compensations are the framing, the approval gate on
  everything mutating, and the sandbox.
- **Transcripts store image bytes** (base64 JSONL), so resume
  (ADR-0005) restores a session with its screenshots intact; sessions
  grow accordingly. Compaction's summariser sees `[image: ref, N
  bytes]`, never bytes.
- **`read_file` refuses image files** and names `view_image` and `@` —
  mojibake in the context helps nobody.

## Consequences

- Both halves of the screenshot workflow work: Cmd+Ctrl+Shift+4 then
  `@clipboard ここがおかしい`, and scan → `get_screenshot` →
  `view_image` inside one agent loop.
- History replay resends images every round until compaction clips
  them; visible in the footer counters. Accepted.
- `view_image` widens what the model can pull into its own context to
  any in-project image. That is the same authority `read_file` already
  has over text, with the same confinement; the new exposure is visual
  injection, handled as above and worth naming in the drill someday.
- `@clipboard` shells out to `osascript`; automation restrictions make
  it fail with the reason reported.

## Alternatives considered

- **Operator-attached only** ("images are operator input, full stop") —
  rejected mid-design, by the operator: MCP-obtained images are read at
  the model's initiative, and the IR flow is exactly that case.
- **A `/paste` command with pending state** — rejected: two attachment
  mechanisms and a "attached to what?" state question; `@clipboard`
  rides the existing parse/display/problem paths.
- **A user-role image message appended after the tool round** —
  rejected by measurement: round 1 passed, and the next request 400'd
  on the call/response pairing rules. It also would have been the
  second time those rules collected a toll from this project.
- **GCS `file_data` URIs for large images** — rejected for now: a
  bucket dependency in a tool whose setup is deliberately "ADC and go".
  Inline with an 8MB cap covers screenshots.

## References

- ADR-0001 (isolation; images get the framing counterpart, stated as weaker)
- ADR-0005 (resume; transcripts now carry image bytes; the measure-first
  precedent for message-shape risks)
- ADR-0006 (compaction; summariser sees placeholders)
- Org MCP conventions (file-mediated outputs; urlscan get_screenshot)

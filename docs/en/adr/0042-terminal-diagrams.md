# ADR-0042: mermaid diagrams render in the terminal — the types that can be drawn faithfully, and only those

| Field | Value |
|-------|-------|
| Status | **Accepted** — the FIT rule and the prompt section are removed by [ADR-0063](0063-diagram-fences-render-in-place.md) |
| Date | 2026-08-22 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: a mermaid block in the chat is unreadable without pasting it into a renderer, yet "don't draw diagrams" is the wrong fix; opening a browser for chat content feels wrong too — tell the model what the terminal can render, draw those inline, show the rest as source, leave files unrestricted |

## Context

glamour renders a ```` ```mermaid ```` fence as an ordinary code block, so
the operator sees the diagram's source. Reading a flowchart from its
source means leaving the conversation to paste it somewhere that draws
it — which breaks the tool's loop (read the result, decide the next
step). Forbidding diagrams throws away a good way to explain structure;
bouncing chat content to a browser (considered first) is a second
surface for a single conversation.

Pure-Go terminal renderers exist. Two were measured against real
diagrams, including Japanese labels — the case that decides usability
here:

- One library (22 advertised types) mangled UTF-8 in flowchart labels,
  misaligned CJK in sequence diagrams, and **silently dropped edges** of
  a state diagram — a rendering that hides part of the graph is worse
  than the source.
- The upstream library (`AlexanderGrooff/mermaid-ascii`, MIT) drew
  Japanese flowcharts with correct cell-width alignment, subgraphs
  included; ER diagrams with Japanese entity names aligned; sequence
  diagrams aligned with ASCII labels but not with CJK ones; node
  *shapes* other than `[box]` were not parsed (a `B{decision}` became a
  literal label plus a stray `B` node); other types error out cleanly;
  and a 6-node horizontal chain fits 91 cells with tight padding.

## Decision

### 1. Advertise exactly what can be drawn, and draw exactly that

The system prompt tells the model, in the TUI only, which mermaid types
the terminal renders: **flowchart/graph** (all directions, subgraphs),
**sequenceDiagram with ASCII labels**, and **erDiagram**. Anything
else — other types, sequence diagrams with wide labels, diagrams too
wide or too tall for the terminal, anything the renderer rejects —
appears as source, unchanged, and the prompt asks the model to add a
one-line caption when it uses such a type in chat. Files are out of
scope: what the model writes into files is not touched and the prompt
says so. The advertised list and the renderer's capability are **one
list in one package**, pinned by a test, so the prompt can never
promise what the renderer does not do.

### 2. Draw at flush time, into the same scrollback discipline

The rewrite runs where Markdown is rendered (the flush of a streamed
segment): each eligible ```` ```mermaid ```` block is replaced by a plain
code block holding the box art; the live region still shows the source
while it streams, exactly as every other Markdown does. The art is
measured before it is accepted: wider than the terminal (minus
glamour's margin) → retry with tight padding → still too wide, or taller
than 80 lines → source. Nothing ever reaches `emit()` that would need
to wrap.

### 3. Normalize shapes, then verify fidelity

Node shapes the renderer does not parse — `{decision}`, `((circle))`,
`([stadium])`, `[/parallelogram/]`, `[[subroutine]]`, `[(database)]`,
`{{hexagon}}`, `(round)` — are rewritten to `[box]` before rendering:
the shape is presentation, the graph is the content, and a wrong graph
drawn confidently is the failure mode to fear. After rendering, every
label extracted from the source (node and edge labels; participants
and message texts; entity names and relationship labels) must appear
in the art, or the block falls back to source: the renderer must never
be allowed to draw less than was written. Presentational statements
(`classDef`, `style`, `linkStyle`, `click`) are dropped before
rendering.

### 4. Plain REPL and one-shot stay verbatim

stdout is model text only (the one-shot contract); the plain REPL
streams as it goes. Neither renders Markdown today, and neither gets
the diagram pass — the prompt section is omitted there, so the model is
not told about a capability that surface lacks.

### 5. Three rules, and nothing else (v0.37.6, operator direction)

Four field reports produced four patches, each a new special case:
a shape normalizer, an edge-syntax normalizer, a refusal of edges to
subgraph ids, an ER complexity cap. That is whack-a-mole, and the
operator named it: *build the minimum necessary judgment instead of
bolting external judgment and correction onto the renderer.* The
package now runs exactly three rules in order:

1. **Translate** — a deterministic mapping of constructs the
   renderer's grammar rejects into ones it accepts, preserving the
   graph (shapes to boxes, `A -- text --> B` to `-->|text|`, `&` in a
   label to ＆, presentation-only statements dropped). Each entry is a
   syntax fact, never a prediction.
2. **Fit** — one layout: the art fits the terminal and the height cap,
   or the source is shown. The tight-padding retry is gone; it was
   measured overwriting label cells in double-width text, and a second
   layout is a second failure mode.
3. **Verify** — every label the source wrote must appear in the art
   (compared through the renderer's own line-art decoration), and a
   flowchart's edge count must equal the arrowheads drawn.

**Teaching beats correcting (v0.38.0, operator direction).** The
system prompt now states the dialect that draws — square-bracket
labels, `-->|label|` edge labels, no `direction`, no styling, no `&`
inside a label — so the model writes what renders instead of having
its diagram rewritten underneath it. Rule 1's table is **frozen** at
that point: it stays as the backstop for a model that does not follow
the guidance, because removing it was measured to cost 2–3 correct
diagrams of 18 and to let one wrong graph through, but a NEW construct
belongs in the prompt, not in the table. Measured after the change:
the model's next three diagrams used the taught dialect with no
violations and all three drew.

Rule 3 is what makes per-construct blacklists unnecessary, and both
blacklists were deleted on that basis: the ER complexity cap judged
beauty rather than correctness, and the subgraph-endpoint refusal was
written from an assumption — measurement showed the renderer draws
those edges correctly in most diagrams, while rule 3 already catches
the ones where it does not (a lost subgraph title, a phantom node).
When a construct breaks in future, the fix belongs in rule 1 if the
renderer's grammar is the problem, and nowhere otherwise: rule 3
already shows the source.

## Consequences

- A flowchart in the chat is readable where the conversation happens,
  Japanese labels included; the operator never leaves the terminal for
  it. The common case the model produces — flow/sequence/ER — is
  covered; the long tail is shown honestly as source with a caption.
- The dependency is MIT and pure Go; it lives behind one package
  (`internal/diagram`) with one function, so a better renderer is a
  swap. Its graph renderer lives in the upstream `cmd` package, which
  also carries a web server's dependencies — the binary grows about
  5MB. Accepted: forking the renderer out is not worth the drift.
- Node shapes are flattened to boxes: a decision diamond reads as a
  box with the decision's text and its yes/no edges. Deliberate —
  fidelity of the graph over fidelity of the glyphs.
- The transcript and the model's history keep the original mermaid
  source; only the display is rewritten.
- Fidelity is enforced structurally, not just by label presence
  (v0.37.2): a flowchart's source edge count must equal the arrowheads
  drawn, `-- text -->` edge labels are normalized to the parsed form,
  and an edge to a subgraph id (a phantom node) falls back to source.
- A complexity cap for dense ER diagrams (v0.37.3) was tried and
  **reverted** (v0.37.4, operator direction): a diagram that fits the
  screen is shown even when its lines cross — readability is the
  operator's call, and "too complex, simplify" is a message to the
  model, not a threshold. The guards that remain are about being
  wrong, never about being ugly. A subgraph's `direction` hint stays
  dropped (the renderer drew it as a node and fused adjacent titles);
  one width model is pinned so box art is not sheared under a CJK
  locale (v0.37.1).
- Measured against every mermaid block from five field sessions
  (v0.37.6): 16 of 18 draw with their edge counts matching; the two
  refusals are diagrams where the renderer genuinely lost something —
  a subgraph title fused with its neighbour, and an edge label dropped
  where two edges share a path. Both are rule 3 working.

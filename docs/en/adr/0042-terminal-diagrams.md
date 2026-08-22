# ADR-0042: mermaid diagrams render in the terminal — the types that can be drawn faithfully, and only those

| Field | Value |
|-------|-------|
| Status | **Accepted** |
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
- Layout quality has limits the guards cannot phrase as "wrong": a
  dense ER diagram (v0.37.3, >5 relationships or an entity at degree
  >3) has its crow's-foot lines cross, so it is shown as source; a
  subgraph's `direction` hint is dropped (the renderer drew it as a
  node and fused adjacent titles). One width model is pinned so box
  art is not sheared under a CJK locale (v0.37.1).

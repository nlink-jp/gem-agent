# ADR-0095: box art comes from mermaid-render

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-30) — implemented |
| Date | 2026-09-30 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | mermaid-render RFP phase 2e (the operator's decisions of 2026-09-30): replace the text-art renderer with an in-house one and remove `mermaid-ascii` |
| Relates to | [ADR-0042](0042-terminal-diagrams.md) (**the renderer, the translation table and the two faithfulness guards are superseded here**; §4, verbatim `-p` and plain REPL, stands), [ADR-0063](0063-diagram-fences-render-in-place.md) (the lane stands: art bypasses the Markdown renderer, a refusal shows the source with the note, an unsupported type passes silently), [ADR-0092](0092-mermaid-fences-render-as-pictures.md) (§1: a TUI with no image protocol keeps the lane — now drawn by this engine), [ADR-0093](0093-outside-text-is-made-inert-for-the-terminal.md) (art still goes through `inertArt`) |

## Context

The box art on terminals that draw no images comes from
`AlexanderGrooff/mermaid-ascii`, community code adopted before the organization
settled that it ships no code written outside it. ADR-0042 wrapped it in a table
rewriting mermaid into its grammar (shapes to boxes, `-- text -->` to
`-->|text|`, `&` in labels to ＆) and two guards (every label present, one
arrowhead per edge), because its internals could not be inspected. Measured on
the real blocks, it drew 21 of 26 flowchart and ER blocks and refused sequence
diagrams with non-ASCII labels; it drew BT as TD and RL as LR, and `Z --> S` with
a phantom node.

mermaid-render now draws flowchart / graph, sequenceDiagram and erDiagram as text
art (`raster.RenderText`), from the same parse as the pictures: the picture's
layered layout run on a grid of cells, ER as tables with cardinality in mermaid's
notation, sequence diagrams with their own columns. Every render is checked on
the grid and a fault is refused. All 43 real blocks draw (Japanese sequence
labels included); the operator passed 15 of them over four review rounds.

## Decision

### 1. The lane draws with mermaid-render

On a TUI with no image protocol, a mermaid fence is handed, as written, to
`raster.RenderText`. Its `TextOptions.Width` is the TUI's own cell measure
(`ansi.StringWidth` per rune), so the art and the rest of the screen agree. The
result is an art segment as today: it bypasses glamour (ADR-0063) and goes
through `inertArt` (ADR-0093).

### 2. What is removed

`mermaid-ascii` (the module leaves go.mod), the translation table, the label
and arrowhead guards, and the refusal of non-ASCII sequence labels. The engine
reads mermaid as mermaid 12.0.0 does and checks its own art; there is nothing
left to rewrite into or reverse-engineer.

### 3. Outcomes

- A drawn fence is art.
- An unsupported diagram type (pie, state, gantt, mindmap and the rest) passes
  through silently as source, as ADR-0063 §4 says.
- Any other error — a syntax error, an unsupported construct, a layout fault, an
  engine panic — shows the source with the one-line note.

`-p` and the plain REPL stay verbatim (ADR-0042 §4).

## Consequences

- Sequence diagrams with CJK labels draw; BT and RL draw in their direction;
  the phantom-node class is gone, because the art reads what the picture reads.
- The binary sheds mermaid-ascii (about 4.3 MB net, RFP §4), and a community
  module leaves the supply chain.
- A terminal set to draw East Asian ambiguous characters double-width still
  misaligns box drawing, as before: undetectable without a query.
- The art looks different from mermaid-ascii's (the operator's review settled
  the new look); diagrams the picture refuses are refused here too.

## Alternatives considered

- **Keep mermaid-ascii for flowcharts only.** Rejected: it keeps community code
  and the rewrite table for no case the engine does not cover.
- **Pictures only, source elsewhere.** Rejected: terminals without an image
  protocol (Terminal.app, multiplexers) would lose the diagrams they have today.

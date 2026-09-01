# ADR-0063: diagram fences render in place, and the runtime says nothing about diagrams

| Field | Value |
|-------|-------|
| Status | **Accepted** |
| Date | 2026-09-02 |
| Binds | gem-agent |
| Supersedes | [ADR-0043](0043-diagram-tool.md) |
| Amends | [ADR-0042](0042-terminal-diagrams.md) |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: the tool costs turns and interleaves status lines into the reply — and two months of sessions show chat diagrams simply disappeared, replaced by hand-drawn box art |

## Context

Measurements over the two months since v0.40.0 shipped ADR-0043
(76 recorded sessions):

- `render_diagram` was called **once**. The call succeeded — and the
  loop the tool was built around (refused, corrected, drawn) was
  exercised approximately never.
- Mermaid fences disappeared from replies, as instructed. What
  replaced them was not the tool: the model **hand-draws box-art
  diagrams inside plain fences**, in replies and in Markdown files
  (seven occurrences since 08-23). Hand-drawn art is unverified,
  shears at any other width, and pollutes files whose diagrams should
  be mermaid.
- The mechanism is the one ADR-0020 §5 measured for memory: **a
  specific prohibition beside a vague recommendation reads as "rarely
  do this."** "Do NOT write a mermaid fence" was specific; "files are
  unaffected" was an aside. The model generalized the prohibition and
  invented a third path nobody had anticipated — or forbidden.

Meanwhile the tool's costs ran continuously: one extra round per
diagram, status lines interleaved into the reply, and a trigger that
had to fire against the model's natural trained behavior (a mermaid
fence in Markdown) instead of riding on it.

ADR-0043's honesty argument — rejecting without telling the author is
not honest — was sound. In practice the channel that closes the loop
is the human: the operator sees the screen and says "simplify" or
"that did not draw," which is response (3) of the standing triage
(teach / verify-and-reject / surface to the human). The in-turn
feedback loop bought by the tool was almost never used.

## Decision

### 1. Fence rendering returns (supersedes ADR-0043 §1)

`diagram.Split` runs at the TUI's Markdown renderer, the single place
completed segments pass through: it partitions the reply around its
mermaid fences and draws them. A fence that draws faithfully is shown
as box art in place; one that does not is shown as source. A
```` ```mermaid ```` line that is content of an enclosing fence (an
example quoted inside another code block) is data and is never drawn.
The plain REPL and one-shot mode are untouched, as before.

This is a **view-layer** concern, and that is the line that reconciles
it with ADR-0043's "do not process what the model produced": glamour
already re-renders every reply it prints — headings, tables, code
blocks. Drawing a fence is the same category, provided (a) the
transcript keeps the model's source verbatim, (b) the transforms
applied before drawing are the frozen syntactic table and nothing
else, and (c) the fallback is the verbatim source. The
meaning-changing corrections ADR-0043 rightly killed stay dead.

### 2. The prompt and the tool set say nothing about diagrams

No tool, no "write mermaid," no "do not hand-draw ASCII art," no
dialect paragraph. The contamination above came from our own
prohibition — negative instructions over-generalize — and a positive
format instruction is equally unnecessary: the model's natural prior
(diagrams in Markdown are mermaid fences) is already the wanted
behavior, on this surface and every other one it writes for. The
runtime renders what arrives instead of steering what is written.

The v0.38.0 dialect teaching retires with the section. The frozen
translation table carries the load alone, as measured when it was
frozen: 16/18 real blocks drew with the table, 13/18 without it. The
table stays; the prompt goes.

A test pins the absence: the system prompt contains no diagram
wording on any surface.

### 3. FIT is deleted (amends ADR-0042 — two rules remain)

No width gate, no height cap. Art wider than the terminal wraps
there; art taller than a screen scrolls, as everything in scrollback
does. One implementation fact makes this honest: **the art must
bypass glamour**. A first probe (a space-free 132-cell line through
`WithWordWrap(80)`) suggested glamour passes code-block lines through
unmodified; the independent review re-measured with real box art —
which contains spaces — and found glamour word-wraps code-block lines
at spaces, shearing a wide drawing into interleaved fragments before
the terminal ever sees it. That is wrongness, not ugliness. So art
segments go to the terminal verbatim, the lane shell output uses,
while the markdown around them renders through glamour as before; the
TUI's scrollback path wraps and never truncates, and the terminal's
own wrap splits overflowing rows in order — losing nothing. What
remains is ugliness, and ugliness is the reader's call, not a gate:
the same standard that reverted the ER complexity cap in v0.37.4.

The guards that remain are wrongness guards, unchanged: every source
label must appear in the art, a flowchart's edge count must equal the
arrowheads drawn, and a sequence diagram with non-ASCII labels stays
source because the renderer misaligns it.

### 4. An attempted draw that fails leaves the fence plus one line

`*diagram shown as source: <why>*` follows the fence — for the
reader, who closes the loop; the model never sees the screen.
Unsupported diagram types pass through silently: a gantt in the chat
is not an error, and a note under every one would be noise. The note
is English, like `warning:` lines (ADR-0029 keeps log-shaped chrome
out of the language catalogs).

### 5. Removed with the tool

`render_diagram`, `diagram.Budget`, and the diagram-budget lines of
`agent_info`.

## Consequences

- A diagram costs zero extra rounds, and the reply reads as the model
  wrote it, art in place of the fence.
- The model writes one diagram format everywhere again; files get
  real mermaid by default.
- The model gets no in-turn rendering feedback. Accepted on the
  measurement above: the loop fired once in two months, and the human
  channel is the one that works.
- If hand-drawn art persists out of habit, session logs will show it;
  the answer is a new measurement, not a new prohibition.
- ADR-0042's three rules become two: TRANSLATE and VERIFY.

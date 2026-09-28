# ADR-0092: mermaid fences render as pictures where the terminal can draw them

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-29; not yet implemented) |
| Date | 2026-09-28 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: "the demand exists; it was suppressed because the text art is too poor to use", and "use images as a rendering of the transcript — a runtime feature, not a model tool, or the model will not use it" |
| Relates to | [ADR-0042](0042-terminal-diagrams.md) and [ADR-0063](0063-diagram-fences-render-in-place.md) (the fence lane, unchanged where no picture is drawn), [ADR-0089](0089-inline-images-declare-their-height.md) (**A3 is superseded here, A2 is answered, and one Consequences line is amended**; the declared box, the lane and the accounting stand), [ADR-0090](0090-an-images-bytes-never-become-a-path.md) and [ADR-0091](0091-showing-is-an-act-of-output.md) (the view layer opens no file; a refused payload is silent) |

## Context

### What the text art could not do

ADR-0063 put mermaid fences back in the reply and drew them as box art with
mermaid-ascii, guarded for faithfulness (every label present, one arrowhead per
edge) and shown as source when the guard fails. It works, and it is not used:
the operator reports that diagrams are left out of work because the art is too
poor to read — misaligned CJK labels, sheared wide flowcharts, sequence
diagrams that refuse non-ASCII. The absence of mermaid in real sessions is not
evidence of no demand; the only sessions that hold mermaid are display tests.

### What changed

`mermaid-render` (lib-series) renders flowchart / graph, erDiagram and
sequenceDiagram to a bitmap, in Go, with only `golang.org/x/image` beyond the
standard library. Its contract is the one ADR-0063 needs:

- **Wrong is refused.** A construct it cannot read, a label character no font
  can draw, or a resource limit exceeded is an error; the caller shows the
  source. It never draws a missing character, and it never refuses a diagram
  for looking bad.
- **It reads mermaid as mermaid 12.0.0 does.** The ER and sequence lexers are
  rule-by-rule ports of the upstream jison grammars; 43 real blocks from this
  runtime's sessions (22 flowcharts, 11 ER, 10 sequence) were read against their
  sources and checked by eye, round after round, by the operator.
- Its layouts are guarded by property tests over 20,000 random diagrams per
  type — no two boxes overlap, every label is drawn inside its box, every head
  has a run of line behind it — and an independent review found and closed the
  resource and crash paths (a source is at most 50,000 characters, as mermaid's
  own limit).

ADR-0089 A3 rejected "render mermaid to PNG instead of box art" because
ADR-0042's faithfulness guards ran, on every render, only against the ASCII
renderer, and a PNG would delete the verification along with the art. That is
still a real trade, and it is not answered by the property tests alone: they run
offline, so a layout defect in a diagram they never generated would draw wrong
with nothing to refuse it. It is answered by §5: the same invariants the
property tests hold run on every render in the engine, and a picture that
breaks one is refused like any other wrong.

### Measured for the box (mermaid-render RFP, stage 0)

On iTerm2 and kitty, the operator compared diagram text at 0.85, 1.00 and 1.15
times the terminal's text and chose **1.00** on both. The diagram is rendered at
Scale 2 (28 px per em). Cell aspect ratios differ by terminal (iTerm2 2.25,
kitty 1.86 on the operator's machines), so it is read, not assumed. The
existing photo box (40 columns, a third of the screen) makes diagram text
unreadable (about 5.6 pt for a 1600×1200 picture) and does not fit here.

## Decision

### 1. A fence becomes a picture only where the TUI draws images

When the TUI's image protocol (ADR-0089 §7, resolved once before the TUI
starts) is iTerm2 or kitty, a mermaid fence in a reply is rendered by
mermaid-render and drawn as an inline image. A TUI with no protocol — including
a multiplexer under `images = "auto"` — keeps the ADR-0063 lane: box art where
it is faithful, source where it is not. One-shot `-p` and the plain REPL print
the model's text verbatim, as ADR-0042 §4 decided; nothing here touches them.
The transcript keeps the model's source verbatim in every case; the picture is
display only.

A forced `images = "iterm"` or `"kitty"` inside a multiplexer that swallows the
payload would lose the diagram. That setting is the operator's assertion that
the terminal draws, and this residual is accepted rather than second-guessed.

### 2. The engine gets the source as written

mermaid-render receives the fence's body **before** ADR-0042's translation
table. The table exists for mermaid-ascii's grammar: it flattens shapes to boxes
and turns `&` in a label into the full-width ＆. Passed through it, the picture
would lose the shapes and change the labels.

### 3. The reply renderer returns segments with declared rows

Today the reply renderer is `func(string) string` and its result is counted as
text. A picture must reach `emitSegments` as a `tui.Segment` that declares its
rows (ADR-0089 §1), the lane tool images already use. The renderer becomes
`func(string) []tui.Segment`: Markdown segments rendered by glamour, art
segments verbatim (ADR-0063 §3), image segments carrying their payload and their
row count. `takeLive` and its callers pass segments through instead of joining a
string. The renderer factory (`mkRender`, today a function of the width) also
receives the protocol, the screen height, the cell aspect and the font. Nothing
else about the accounting changes.

### 4. The box: diagram text at the terminal's own size

A picture of W×H pixels, rendered at 28 px per em, is declared at

- rows = H ÷ 28 ÷ 1.2
- columns = W ÷ 28 ÷ 1.2 × (cell height ÷ cell width)

so one em of diagram text is one line of terminal text. The 1.2 (cell height as
a multiple of the font's em) is a constant, tuned on the operator's terminals;
the aspect is read. The cell's pixel size is read with `TIOCGWINSZ` — an ioctl,
not a terminal query, so it is safe while Bubble Tea owns stdin — at start and
on every resize; when it reports no pixels, the aspect is 2.25 (termimg's
assumption, equal to iTerm2's measured value). The function lives beside
`termimg.BoxFor`; the engine knows nothing about terminals.

This answers ADR-0089 A2 rather than contradicting it: A2 rejected deriving the
row count from pixels because the declared box overrides the aspect and because
`ESC[16t` can go unanswered. Here both dimensions are still declared, the
arithmetic only chooses them, and the cell size comes from an ioctl. ADR-0089's
Consequences line "no aspect-ratio arithmetic and no cell-pixel-size query enter
the runtime" is amended: an ioctl read of the cell size enters, a query does not.
(`BoxFor`'s 2.25 was already aspect arithmetic.)

- **Wider than the terminal less one column:** the box shrinks, keeping the
  aspect; the text gets smaller. That is ugly, not wrong, and the picture is
  still drawn.
- **Taller than the room above the input box and footer:** not shrunk. Its
  top scrolls out into the scrollback, where the operator scrolls up to read
  it, as with a long reply (the operator's decision, 2026-09-29: shrinking a
  tall diagram makes its text unreadable, and scrolling is the ordinary way to
  read something tall). The declared rows are the full height. How such a
  picture interacts with the bottom pin is measured on iTerm2 and kitty during
  implementation and recorded here; if it damages the screen rather than
  scrolling (ADR-0089's stranded frames), that is wrong, not ugly, and this
  bullet is reopened.
- **A resize after a picture is emitted** does not redraw it: pictures already
  in scrollback keep the box they were emitted with, as text does.

### 5. Failure shows the source, never nothing

- An unsupported diagram type (state, class, gantt, …) passes through as source,
  silently, as ADR-0063 does.
- Every other refusal shows the fence as source with ADR-0063 §4's one-line note
  naming why: a syntax error, an unsupported construct (nested subgraphs among
  them, in the engine's first phase), a character no font can draw, a limit, a
  PNG over `termimg.MaxBytes` (2 MiB), and a **drawn layout that breaks the
  engine's own invariants**. mermaid-render gains that check before it is
  adopted: the invariants its property tests hold (no two boxes overlap, a box
  holds its label, a frame holds its members and no other box, an edge keeps its
  label and every head has a run of line) run after layout on every render, and
  a failure is an error. It is the renderer's own counterpart of ADR-0042's
  guards, and like them it checks wrong, never ugly.

A picture never disappears together with its source. `drawImage` refusing a
payload silently (ADR-0090, ADR-0091 §4) is not a path this lane may take, so the
image segment carries the fence's source, and every failure after the fence is
replaced — the size check on the encoded PNG, a payload that cannot be built, a
box of zero — emits that source with the note instead. Rendering runs under
`recover()`: a panic in the engine is a refusal like any other, not the end of a
session.

### 6. The font is loaded once, by the cmd layer, and only when it is needed

The view layer opens no file (ADR-0089 §5). The cmd layer loads the font at
start — Hiragino Sans W3 / W6 by default, about 16 MB read — and hands the loaded
font to the TUI, and only when the resolved protocol draws images; a session
that cannot show a picture reads no font. Configuration, in the user config only
(the project config has no `[tui]` table):

```toml
[tui.diagram]
font = "/path/Font.ttc"      # body text; default: Hiragino Sans W3
font_name = "Font-Regular"   # face in the file, by full or PostScript name
bold_font = "/path/Font.ttc" # bold (entity names, frame titles); default: the body face
bold_font_name = "Font-Bold"
```

`bold_font` without `font` is a configuration error, as `font_name` without
`font` is. The settings panel shows these as read-only rows that apply at the
next start.

A font that fails to load (missing file, unknown face name, a configuration
error) does **not** stop the runtime (the operator's decision, 2026-09-29): the start banner carries one line naming
the setting and the error, and diagrams use the default font. A display font is
not worth a session. If Hiragino itself cannot be loaded, the banner says
diagrams are drawn as text, and every fence takes the ADR-0063 lane.

Reads are bounded (ADR-0073 §4). mermaid-render reads font files itself, where
`TestReadsAreBounded` cannot see, so before adoption its reads refuse anything
but a regular file and stop at a fixed ceiling, set from the largest system font
on macOS.

### 7. The runtime still says nothing about diagrams

No tool, no prompt paragraph (ADR-0063 §2, ADR-0089 §8). The model's natural
prior — a diagram in Markdown is a mermaid fence — is what this feature draws;
the operator asks for diagrams in their instructions when they want them.
`TestSystemPromptSaysNothingAboutDiagrams` stands.

### 8. Where the work happens

- `internal/diagram` gains the picture path beside the art path: `Split` takes
  an optional renderer, and a fence it can draw becomes an image segment
  holding the PNG, its pixel size and its source.
- `internal/tui` turns image segments into payloads with the §4 box, as
  `drawImage` does for tool images. `termimg.Payload` stays callable from
  `internal/tui` only (ADR-0089 §6's archtest).
- Rendering runs where the live text is flushed, in `Update`. Four places flush
  it, two of them keypresses while a reply streams (the approval-mode toggle and
  `/auto`), so a keypress can pay the render. Measured in the engine: a real
  diagram renders in 1–9 ms and encodes in 4–49 ms; a dense one (150 nodes, 300
  links) in 233 ms. A flush in the middle of a fence splits it, and both halves
  show as source — as they already do with box art.
- If the stall is felt on real sessions, rendering moves to a `tea.Cmd`. That is
  not a local change and is not decided up front: `emitJoined` writes a flush and
  what follows it as one ordered write, so an asynchronous picture would need
  a placeholder that later lines wait behind, or tool-call lines would print
  above the picture they follow.

## Consequences

- Diagrams in replies are readable on iTerm2 and kitty: CJK labels, wide
  flowcharts and non-ASCII sequence labels all draw.
- On iTerm2 and kitty, a diagram the engine refuses shows as source where
  mermaid-ascii might have drawn it — nested subgraphs above all, until the
  engine's phase 2. B4 is why this is accepted.
- mermaid-ascii remains, for terminals that cannot draw. Replacing it with a
  text renderer on the same parser is the mermaid-render RFP's phase 2.
- gem-agent gains a dependency on `github.com/nlink-jp/mermaid-render` (an
  organization module) and, through it, `golang.org/x/image`; the module graph
  moves `golang.org/x/sys` and `golang.org/x/text` up one minor version.
  mermaid-render is released first, with the §5 check and the §6 bounded reads,
  and gem-agent adopts a tagged version.
- The reply renderer's type changes; every caller of `takeLive` handles segments.
  The withdrawn-claim test's reason for "stops rendering the reply as one piece"
  (which says the renderer was never changed) is rewritten in the same commit.

## Alternatives considered

**B1. Keep the box art and improve it.** The art's failures are the renderer's
(label alignment, width), and mermaid-ascii is a community module the
organization does not extend. mermaid-render's phase 2 replaces the art on the
same parser instead.

**B2. A diagram tool for the model.** Measured precedent: `render_diagram`
fired once in 76 sessions (ADR-0063). A runtime rendering fires on every fence
the model writes anyway.

**B3. mermaid.js in a headless browser.** A browser and a JavaScript runtime in
the display path, started for every fence, for output the terminal shows as a
bitmap anyway.

**B4. Fall back to box art when the picture fails.** Two renderers' failures
would then compose: a diagram the new renderer refuses as wrong could be drawn
wrong by the old one. The source is the predictable answer.

**B5. The photo box (40 columns, a third of the screen).** Measured unreadable
for diagrams (stage 0).

## References

- mermaid-render RFP (lib-series): §2 "表示の大きさ" (the box), the Discussion
  Log (stage 0 measurements, the operator's checks, the independent reviews).
- ADR-0042 §4, ADR-0063 §2–§4, ADR-0073 §4, ADR-0089 §1, §3, §5–§8 and A2–A3,
  ADR-0090, ADR-0091 §4.

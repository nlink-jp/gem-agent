# ADR-0089: inline images declare their box — the counter is told, never measures

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-16, rewritten 2026-09-17) |
| Date | 2026-09-16 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: when the terminal supports graphics, can a reply draw them inline — here and in lagent? |
| Rewritten because | An independent verification pass returned 17 findings in three classes: claims about adjacent code asserted without reading it, "measured" claims wider than the instrument that produced them, and a lane opened without enumerating the dimensions it opens. Patching seventeen sentences is the response the root-cause rule forbids, so the premises were measured again and the record rebuilt on them |
| Relates to | [ADR-0002](0002-tui.md) (Bubble Tea inline TUI), [ADR-0003](0003-bottom-pinned-layout.md) / [ADR-0028](0028-self-healing-line-counter.md) (the row accounting and its self-heal), [ADR-0042](0042-terminal-diagrams.md) / [ADR-0063](0063-diagram-fences-render-in-place.md) (the view-layer lane), [ADR-0085](0085-credential-reads-are-operator-only.md) / [ADR-0086](0086-the-kernel-reads-the-file.md) (who may open a file) |

## Context

### What the counter can and cannot see

`emit` ([model.go:857](../../../internal/tui/model.go)) prints one line into
scrollback and counts its physical rows; the bottom pinning rests on that
count. Measured against `charmbracelet/x/ansi` v0.11.6, the version
`go.mod:14` pins: `ansi.StringWidth` returns **0** and `ansi.Strip` returns
the empty string for an iTerm2 `OSC 1337 File=`, a kitty `APC _G` and a
sixel `DCS q` alike. `ansi.Hardwrap` leaves all three **byte-identical**, so
`wrapForScrollback` ([model.go:1016](../../../internal/tui/model.go)) does
not shear a base64 run. The independent pass re-measured this with a real
PNG, a chunked kitty sequence and a real sixel, at five widths, both
`preserveSpace` settings — the repository's own test had covered two of the
three families, and `tools/imgpayload` now carries all three.

The counter is not blind, though, and the first draft said it was.
`physicalRows` ([model.go:1029](../../../internal/tui/model.go)) starts at
`rows, cells := 1, 0` and so credits an image line with exactly **one** row
while the terminal advances N. The shortfall is `N-1`, not `N`.

### What a shortfall actually costs

This was measured twice, wrongly, before it was measured with a control.
`tools/rowprobe` reserves rows below the cursor so nothing scrolls while it
compares two cursor positions — necessary for that method, and a deliberate
exclusion of production's condition. `tools/pinprobe` drives the **real**
model (`tui.New`) under the **real** inline program and pushes lines through
the **real** emit path (`tui.Output` → `emitJoined` → `emit`), so
`wrapForScrollback`, `physicalRows` and the bottom-hold accounting are the
production functions. The footer's `ModelName` carries a sentinel, so a
capture locates the pin without a human reading it.

Every row below was taken with the control — the same run, same `-fill`,
payload replaced by a plain line:

| terminal | payload | screen | gap END→pin | frames stranded | control at same fill |
|---|---|---|---|---|---|
| tmux 3.7c | sixel ×3 | full (`-fill 60`) | −16 | **3** | clean (gap 1, 0) |
| tmux 3.7c | sixel ×3 | not full (`-fill 0`) | 18 | 0 | identical (18, 0) |
| iTerm2 3.7.2 | OSC 1337 `height=12` ×1 | full | 14 rows | 0 | identical (14, 0) |
| iTerm2 3.7.2 | OSC 1337 `height=12` ×5 | full | 42 rows | 0 | identical (42, 0) |

Three facts follow, and two of them contradict the first draft:

1. **A terminal that draws what the counter cannot see strands a frame** —
   one per image, reproducibly (2/2 on the leak, 2/2 on the clean control),
   and only once the screen is full. That is the damage this ADR exists to
   prevent, and it is real.
2. **It did not happen on iTerm2.** Five images, each undercounted by 11
   rows, moved nothing: the gap and the pin were identical to the no-image
   control. The first draft asserted the pin drifts by `N-1` per image; the
   measurement does not support that on this terminal.
3. **The regime matters and the code says why.** The pin's padding is
   `height − printed − view − 1` and floors at zero once the screen is full
   ([model.go:1661](../../../internal/tui/model.go)). Both regimes were
   measured; only the full one broke, and only on tmux.

### What the terminal does with a declared box

`rowprobe` on iTerm2 3.7.2, 180×80, 16 of 16 cursor reports answered:

| case | declared | occupies | end col |
|------|----------|----------|---------|
| no size declared | (none) | 10 rows | 41 |
| `height=6` only | 6 | 6 rows | 25 |
| 40×6 box, aspect kept (wide / tall / stretched) | 6 | 6 rows | 41 |
| `height=1` | 1 | 1 row | 5 |
| 40×12 box, aspect kept | 12 | 12 rows | 41 |
| text + image + text on one line | 6 | 6 rows | 38 |

- **The declared box is reserved exactly, in both dimensions, whatever the
  picture does inside it.** The 40×12 case holds a 16:9 image that draws
  about ten rows and occupies twelve; the 40×6 cases end at column 41 though
  the drawing is twenty-four columns wide. `pinprobe` then showed the same
  thing on the production path and on screen rather than through a cursor
  report: the gap between the picture's bottom edge and the following
  marker is the reserved slack the picture did not fill. So no
  aspect-ratio derivation is needed.
- **The cursor is left on the image's last row**, past the last cell written
  on that row — not, as the first draft said, past the *declared box*: that
  reading is contradicted by the table's own `height=6` (25), `height=1` (5)
  and text rows (38), where no width was declared or a suffix followed. What
  the correction depends on is only that the column is never 1, and that
  holds in every row.
- **An undeclared image takes its native size**, recoverable only from the
  cell pixel size. Declaring is the difference between a number we choose
  and one we must go and ask for.
- **Text on the same line lands badly**: prefix on the image's first row,
  suffix on its last. Note the limit: that row's "6 rows" is inferred from
  where the suffix left the cursor, not witnessed from the image, and
  `rowprobe` excludes such lines from testifying that anything drew at all
  ([rowprobe/main.go](../../../tools/rowprobe/main.go), `drew`).

### Not measured, and not asserted

- Whether kitty or Ghostty honour `r=`. Every measurement here is iTerm2 or
  tmux. Decision 3 binds two protocols; one of them has no measurement.
- Whether Terminal.app implements any of the three. Stated in the first
  draft as fact; it is **unverified here** and load-bearing only for how
  common the no-graphics path is.
- The per-image cost. iTerm2 answered the next cursor report 0.8–1.5 s after
  a 2.4 KB payload, which bounds when its parser reached the token and is
  *not* draw latency.
- A terminal that draws without reserving rows (kitty `C=1`, unicode
  placeholders). `consumedRows` would credit it one row where the true
  occupancy is zero — a false positive in the unsafe direction, never
  triggered by a measured run.

## Decision

### 1. The emitter declares the box; the counter is told

`physicalRows` never measures an image. An image segment carries the row
count its payload declares — `height=N` for iTerm2, `r=N` for kitty — and
`emit` uses **that** number **in place of** the one row `physicalRows` would
otherwise floor to, not in addition to it. There is no second path to the
number.

### 2. The declaration covers columns too, or the lane is half-built

`wrapForScrollback` keeps every printed line strictly narrower than the
terminal, and its doc comment says why: the renderer's math is exact only
then, and every line gets its `EraseLineRight`. For an image line that wrap
is **inert** — the payload is zero cells wide, so the one mechanism
enforcing the invariant cannot see the line it most needs to see. So the
emitter declares a column count as well, and clamps it below `m.width`. A
declared box is a cage in two dimensions or it is not a cage.

### 3. An image occupies its own line, on the lane ADR-0063 built

Measured: text sharing the line with an image is split across its first and
last rows. An image segment is alone on its line and goes to the terminal
verbatim — the same lane, and the same reason, as ADR-0063 §3's art.

### 4. Two protocols, and only the ones that can declare

**iTerm2 `OSC 1337 File=` and the kitty graphics protocol**, both of which
take the row count as a parameter — decision 1's precondition.

**Sixel is not taken**, and the measurement above is now the reason: it
cannot declare a row count, and the one terminal measured drawing sixel
stranded a frame for every image. The first draft also cited a "standing
rule" that the dependency floor is stdlib and first-party SDKs. That
sentence is withdrawn: it appears nowhere in this repository outside that
draft, and this module already depends on the community `mermaid-ascii`
package for the very lane decision 3 joins
([diagram.go:41](../../../internal/diagram/diagram.go)). Whether that
dependency is an exception or a debt is a separate question; it is not
settled here and must not be settled by a sentence in passing.

### 5. One source, because the runtime chose its path

**An image content block in an MCP tool result.** The intake already
decodes it and **writes it to the session work directory**, handing the
model `[image saved at <path> … use view_image on that path]`
([mcpresult.go:184](../../../cmd/mcpresult.go)) — the bytes never ride back
inline (ADR-0027). So the view layer draws a file **this runtime named and
this runtime wrote**, and needs no new read of any other path.

**A local image path named by the model is rejected.** It would be a
view-layer file open, which is not a tool call: it never reaches
`Agent.decide`, never runs in the sandboxed read child (ADR-0086), and is
invisible to the one credential list, which is keyed on built-in tool name
([risk.go:176](../../../internal/risk/risk.go), `credentialReadTools` /
`JudgesPath`). That is precisely the class ADR-0085/0086 repaired — "the
write tools' block is not the read tools' protection" — and the first draft
walked into it while decision 6 discussed only escape passthrough. If a
model-named path is ever wanted, it arrives as a **tool** whose verdict the
existing enforcers already take, not as a read the view layer performs.

One source also removes the overlap the pass found: an MCP server that
returns both an image block and its path in text would otherwise have been
drawn twice.

### 6. Only the view layer emits an image escape

Bytes arriving from a tool are data. The view layer decides a segment is an
image and writes the escape; nothing a tool returns is passed through as an
escape because it looks like one.

The implementation commit carries an architecture test enumerating the
sites that may emit one, in the same commit — the machinery exists
(`internal/archtest`). Without that test this sentence is "as of today",
and it should be written that way instead.

This does **not** close the existing surface: tool output is printed without
ANSI stripping — `ansi.Strip` is called at exactly one site in non-test code
([model.go:1034](../../../internal/tui/model.go)), inside `physicalRows`, to
*measure* — so raw escapes from shell output already reach the terminal.
Pre-existing, not widened here, not repaired here.

### 7. Drawing is a TUI-only capability, and the other entrances say so

`tea.NewProgram` is constructed at exactly one site
([root.go:1707](../../../cmd/root.go)). One-shot `-p` and the plain REPL
never build it, so they never draw — the same boundary the diagram lane
already has. The capability is probed **once, before `tea.NewProgram`**, and
cached: Bubble Tea v1 does not decode the kitty/CSI-u protocols
(`AGENTS.md:296`), so a reply arriving mid-session becomes input. The probe
takes seconds, drains before querying, and treats no reply as *no
capability* — an abandoned cursor report is misfiled into the next query,
not lost (measured building `rowprobe`: 11 sent, 5 read, 6 landing on the
shell prompt after exit).

`[tui] images = "auto"` selects it, beside `theme` and `language`
([config.go:179](../../../internal/config/config.go)). Inside a multiplexer
the answer is **off** — not because passthrough is someone else's
configuration, which is what the first draft said, but because the one
multiplexer measured rendering a payload stranded a frame for every image.

### 8. The runtime says nothing about images

No tool, no prompt paragraph. ADR-0063 §2's rule stands. The first draft
argued the model "already produces" these sources; that is a firing-rate
claim and this project has a measured precedent against making one without
a denominator — `render_diagram` fired once in 76 sessions. Decision 5's
single source needs no model behaviour at all: the intake writes the file
whether or not the model mentions it. A test pins the prompt's silence.

## Consequences

- The bottom pin survives images by construction rather than by care, in
  both dimensions.
- No aspect-ratio arithmetic and no cell-pixel-size query enter the runtime.
- Terminal.app — and any terminal that does not draw — loses nothing: the
  fallback is today's behaviour.
- The operator sees a tool's screenshot without asking the model to look at
  it. ADR-0005's counterpart in lagent settled ingestion; this settles the
  screen.
- **What is unmeasured stays unmeasured**: kitty and Ghostty honouring `r=`,
  Terminal.app's protocol support, and the per-image cost. `auto` should not
  be trusted in a streaming turn until the last of those is measured.
- This ADR binds gem-agent; lagent ADR-0020 is the same decision on the
  other side. Neither runtime may hold it alone.

## Alternatives considered

**A1. Measure the image instead of declaring it.** No measurement path
exists: the payload is zero cells wide to every surface the TUI has, and a
cursor round-trip per line is impossible once Bubble Tea owns stdin.

**A2. Derive the row count from the image's pixels and the cell size.**
Measured unnecessary: the declared box overrides the aspect ratio, so the
derivation would compute a number the terminal ignores, and it would add an
`ESC[16t` query that can go unanswered.

**A3. Render mermaid to PNG instead of box art.** Rejected: ADR-0042's
faithfulness guards — every source label present, edge count equal to
arrowheads — exist only for the ASCII renderer, and a PNG would delete the
verification along with the art. (The dependency argument the first draft
also made is withdrawn with decision 4's.)

**A4. Sixel, for breadth.** Decision 4, now with a measurement.

**A5. An alt-screen region that manages images.** Rejected: inline mode and
the native scrollback are what let an image survive being scrolled past.

**A6. Let the model name a file to draw.** Decision 5 — it opens a
view-layer read outside every enforcer.

**A7. Tell the model the terminal can draw.** ADR-0063 §2, and decision 8:
the source that matters needs no model behaviour.

## References

- `tools/rowprobe` — what one image costs, measured by cursor report with
  scrolling deliberately prevented
- `tools/pinprobe` — what the accounting costs on the production path, with
  the control at the same fill in every run
- `tools/imgpayload` — one payload builder, so the two probes cannot drift
- ADR-0003 / ADR-0028 (the pin and its self-heal), ADR-0063 §2–3 (the lane
  and the prompt's silence), ADR-0085 / ADR-0086 (who may open a file)

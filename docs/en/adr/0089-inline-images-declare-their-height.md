# ADR-0089: inline images declare their height — the counter is told, never measures

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-16) |
| Date | 2026-09-16 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: when the terminal supports graphics, can a reply draw them inline — here and in lagent? |
| Relates to | [ADR-0002](0002-tui.md) (Bubble Tea inline TUI), [ADR-0003](0003-bottom-pinned-layout.md) (the row accounting the pin rests on), [ADR-0042](0042-terminal-diagrams.md) / [ADR-0063](0063-diagram-fences-render-in-place.md) (the view-layer lane and the rule that the prompt says nothing) |

## Context

`emit` prints one line into scrollback and counts its physical rows, and the
bottom pinning rests on that count being exact. The drift repairs prove how
exact: a tab counted as zero cells but advancing to the next stop moved the
input line one row per wrapped line (ADR-0021), and a double-width rune
straddling the last column cost one row per CJK line (review round 2 of
ADR-0028).

**An inline image defeats the counter outright.** Measured against
`charmbracelet/x/ansi` v0.11.6, the version this module pins:
`ansi.StringWidth` returns **0** for an iTerm2 `OSC 1337 File=`, a kitty
`APC _G` and a sixel `DCS q` alike, and `ansi.Strip` returns the empty
string. The payload is invisible to every measuring surface the TUI owns,
while the terminal advances real rows. An image is not a wide line; it is a
line the counter cannot see at all.

The same measurement settles the most likely way this could have failed
before it started: `ansi.Hardwrap(payload, 79, true)` leaves all three
payloads **byte-identical**. `wrapForScrollback` does not shear a base64
run.

Two existing architectural facts make the question askable at all. Bubble
Tea runs **inline** (ADR-0002): completed output goes to the terminal's
native scrollback and is never repainted, so an image, once drawn, is the
terminal's to keep — an alt-screen TUI would wipe it on the next frame. And
ADR-0063 already built the lane: `diagram.Split` partitions a reply around
its fences and hands art segments to the terminal **verbatim**, bypassing
glamour, because glamour word-wraps code-block lines at spaces.

### What the terminal actually does

`tools/rowprobe` reserves rows below the cursor (forcing the scroll *before*
the measurement), asks where the cursor is, writes a payload, and asks
again. iTerm2 3.7.2, 180×80 cells, 16 of 16 cursor reports answered:

| case | declared | occupies | cursor Δ | end col |
|------|----------|----------|----------|---------|
| no size declared | (none) | 10 rows | 9 | 41 |
| `height=6` only | 6 | 6 rows | 5 | 25 |
| 40×6 box, aspect kept (wide image) | 6 | 6 rows | 5 | 41 |
| 40×6 box, aspect kept (tall image) | 6 | 6 rows | 5 | 41 |
| 40×6 box, stretched | 6 | 6 rows | 5 | 41 |
| `height=1` | 1 | 1 row | 0 | 5 |
| 40×12 box, aspect kept | 12 | 12 rows | 11 | 41 |
| text + image + text on one line | 6 | 6 rows | 5 | 38 |

Four facts follow, and the decision rests on the first:

1. **The declared box is reserved exactly, in both dimensions, whatever the
   picture does inside it.** The 40×12 case holds a 16:9 image that draws
   about ten rows tall and still occupies twelve; the 40×6 cases leave the
   cursor at column 41 though the drawing is twenty-four columns wide. A
   layer that derives rows from the image's aspect ratio is **not needed** —
   the declaration overrides the aspect ratio.
2. **The cursor is left on the image's last row**, at the column past the
   declared box, never at column 1 on a fresh row. So the raw delta between
   the two reports is one *less* than the occupancy. Read as the count, that
   off-by-one makes a terminal honouring every declaration look like one
   honouring none — which is exactly how this run was first read.
   `consumedRows` and its test carry the correction and the data that
   justifies it.
3. **An undeclared image takes its native size** — ten rows for 320×180, the
   arithmetic of 8×18px cells that the `height=6` (24 cols) and `height=1`
   (4 cols) rows confirm independently. Recovering that number needs the
   cell pixel size. Declaring is not a nicety: it is the difference between
   a number we choose and a number we have to go and ask for.
4. **Text on the same line lands badly**: the prefix on the image's first
   row, the suffix on its last — `before` at the top left, `after` at the
   bottom right.

Two costs are measured and **not** resolved here. After a 2.4 KB payload,
iTerm2 answered the next cursor report 0.8–1.5 s later, past a 250 ms
settle. That bounds when its parser reached the image token; it is *not*
established as draw latency, and the picture appears on screen well before.
Separately, a probe that abandoned a reply after 700 ms desynchronized its
whole run: a cursor report carries no tag, so an abandoned reply is not lost
but **misfiled** into the next query (11 sent, 5 read, 6 landing on the
shell prompt after exit). Both are properties of talking to the terminal,
and both belong to the detection step below.

### The terminals in play

macOS only. **Terminal.app implements none of the three protocols**, so the
no-graphics path is a main road and not an edge case. iTerm2 implements
`OSC 1337` natively; kitty and Ghostty implement the kitty protocol.

## Decision

### 1. The emitter declares the height; the counter is told

`physicalRows` never measures an image and never learns to. An image segment
carries the row count its payload declares, and `emit` adds **that** number
to the accounting. There is no second path to the number, so there is
nothing for a measurement to disagree with: the count is exact by
construction, and the bottom pin that rests on it stays exact.

The declaration is also what is written into the payload — `height=N` for
iTerm2, `r=N` for kitty — so the screen and the accounting read the same
figure from the same place. The cursor lands on the image's last row
(Context fact 2), so the newline `emit` already appends opens the next row
rather than adding one: a segment declaring N rows costs exactly N.

### 2. An image occupies its own line

Measured (Context fact 4): text sharing the line with an image is split
across the image's first and last rows. An image segment is therefore alone
on its line, and it goes to the terminal verbatim — the same lane, and the
same reason, as ADR-0063 §3's art segments.

### 3. Two protocols, and only the ones that can declare

**iTerm2 `OSC 1337 File=` and the kitty graphics protocol.** Both take the
row count as a parameter, which is decision 1's precondition.

**Sixel is not taken.** It cannot declare a row count — the count would have
to be derived from the image's pixel height and the cell pixel size, which
reintroduces exactly the derivation decision 1 removes, resting on a second
terminal query that can fail. It also needs an encoder, and the only ones
available are community packages (the standing rule: the comparison set is
first-party artefacts, and the dependency floor is stdlib and first-party
SDKs). PNG and JPEG go to the two protocols above as bytes, with
`image.DecodeConfig` from the standard library the only decoding this
runtime does.

### 4. The capability is probed once, before Bubble Tea owns stdin

`$TERM` cannot answer the question and is not asked. The terminal is
queried — the kitty protocol's `a=q` probe, and `TERM_PROGRAM` plus
`XTVERSION` for iTerm2 — **once, at startup, before `tea.NewProgram`**, and
the answer is cached for the session. Never during the session: Bubble Tea
v1 does not decode the kitty/CSI-u protocols (AGENTS.md), so a reply
arriving mid-session becomes garbage in the input box.

The probe follows what `tools/rowprobe` had to learn: a generous budget
(seconds, not milliseconds), the stream drained before the query, and a
reply that never arrives treated as *no capability* rather than skipped —
because an abandoned reply is misfiled, not lost.

`[tui] images = "auto"` selects it, alongside `theme` and `language`:
`auto` probes, `off` never draws, `iterm` / `kitty` force a protocol for the
case where the probe is wrong. Inside a multiplexer the answer is **off**
unless a protocol is forced: passthrough is the multiplexer's configuration,
not this runtime's to assume.

### 5. What may be drawn is a list, not a rule

Two entries:

- an **image content block in an MCP tool result** — already decoded to
  `Content{Data, MIME}` in `internal/mcp/client.go` and today only forwarded
  to the model;
- a **local image file named by the reply**, as a Markdown image link, whose
  bytes decode as PNG or JPEG via `image.DecodeConfig`.

A third source is a new entry in this list, argued on its own, not a rule
that generalizes these two. An entry is cheap; a rule is the smell
(ADR-0086).

### 6. Only the view layer emits an image escape

Bytes that arrive from a tool are data. The view layer decides that a
segment is an image and writes the escape; nothing a tool returns is ever
passed through as an escape sequence because it looks like one.

This does **not** claim to close the existing surface: tool output is
printed without ANSI stripping today — `ansi.Strip` is called only to
*measure* width (`physicalRows`) — so raw escapes from shell output already
reach the terminal. That is pre-existing, it is not widened here, and it is
not repaired here either. It is written down so the next review finds it
named rather than missed.

### 7. The runtime says nothing about images

No tool, no prompt paragraph, no "this terminal can draw." ADR-0063 §2's
rule and its reason are unchanged: the runtime renders what arrives instead
of steering what is written. The two sources in decision 5 are things the
model already produces for its own reasons — a tool returns a screenshot, a
reply links a file it just wrote — so there is no capability waiting on a
trigger that must be taught.

A test pins the absence, as ADR-0063's does.

## Consequences

- The bottom pin survives images by construction rather than by care. The
  number `physicalRows` needs is chosen by the emitter, not recovered from
  the screen.
- No aspect-ratio arithmetic and no cell-pixel-size query enter the runtime.
  `image.DecodeConfig` is used to know an image *is* one, not to size it.
- Terminal.app loses nothing: the fallback is today's behaviour — box art
  for diagrams, a path for an image.
- **The per-image cost is unmeasured.** The 0.8–1.5 s figure above is a
  bound on the terminal's parser, not on drawing, and streaming a reply
  through it has not been tried. Measuring that comes before `auto` is
  trusted in a streaming turn.
- An image a tool produced is shown to the operator whether or not the model
  is given it. Display and ingestion are separate surfaces, and the
  ingestion side is already settled in the sibling runtime: lagent ADR-0005
  attaches a dropped image path and restored `view_image`, so its model does
  receive images. What neither runtime has is the screen.
- This ADR binds gem-agent. lagent has the same inline TUI and the same
  accounting and does not have `internal/diagram`; taking this decision in
  one runtime and not the other creates the asymmetry class both runtimes
  have been repaired for before. The lane has to be built there too, or the
  decision is not finished.

## Alternatives considered

**A1. Measure the image instead of declaring it.** There is no measurement
path: the payload is zero cells wide to every surface the TUI has
(Context), and asking the terminal per line means a cursor-position
round-trip inside the loop — impossible once Bubble Tea owns stdin, and
measured at 0.8–1.5 s per image when it is possible at all.

**A2. Derive the row count from the image's pixels and the cell size.**
Measured unnecessary: the declared box overrides the aspect ratio
(Context fact 1), so the derivation would compute a number the terminal
ignores in favour of the one we already chose — and it would add an
`ESC[16t` query that can go unanswered.

**A3. Render mermaid to PNG instead of box art.** Rejected twice over. It
needs node and a headless browser, against the dependency floor; and
ADR-0042's faithfulness guards — every source label present, edge count
equal to arrowheads drawn — exist only for the ASCII renderer. A PNG would
delete the verification along with the art. The box art stays; images are a
separate lane.

**A4. Sixel, for breadth.** See decision 3: no declarable row count, and an
encoder that would have to come from a community package.

**A5. An alt-screen region that manages images.** Rejected: inline mode
(ADR-0002) and the native scrollback are precisely what let an image survive
being scrolled past. A managed region would have to redraw images the
terminal is already keeping, and lose them on exit.

**A6. Tell the model the terminal can draw.** ADR-0063 §2 measured what a
prompt paragraph about drawing does: a specific prohibition beside a vague
recommendation taught the model a third path nobody had anticipated. Nothing
here needs the model to behave differently.

## References

- `tools/rowprobe` — the measurement, its test table, and the misreading it
  corrects
- ADR-0003 (bottom pinning), ADR-0021 (tabs), ADR-0028 (straddling CJK) —
  what an inexact row count costs
- ADR-0063 §2 and §3 — the prompt says nothing; art bypasses glamour
- ADR-0086 — an entry is cheap, a rule is the smell

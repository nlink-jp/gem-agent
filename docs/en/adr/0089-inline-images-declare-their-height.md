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
not shear a base64 run. An independent pass re-measured it with a
real PNG and a real sixel — the repository's own test had covered two of the
three families, and `tools/imgpayload` now carries all three. What that
pass ran beyond this (more widths, both `preserveSpace` settings, a
multi-chunk kitty sequence) lived in a scratch directory and is **not** in
this repository; an earlier draft cited it as though it were. What is
checkable here is one width, `preserveSpace` true, and a single-chunk kitty
payload, which is what a 320×180 PNG produces.

The counter is not blind, though, and the first draft said it was.
`physicalRows` ([model.go:1029](../../../internal/tui/model.go)) starts at
`rows, cells := 1, 0` and so credits an image line with exactly **one** row
while the terminal advances N. The shortfall is `N-1`, not `N`.

### What a shortfall actually costs

Measured twice wrongly before it was measured with a control **and in the
right regime**. `tools/rowprobe` reserves rows so nothing scrolls while it
compares two cursor positions — necessary for that method and a deliberate
exclusion of production's condition. `tools/pinprobe` drives the **real**
model (`tui.New`) under the **real** inline program and pushes lines through
the **real** emit path (`tui.Output` → `emitJoined` → `emit`); `-drive` runs
the experiment under tmux, reads the screen back with `capture-pane`, and
prints the table it computed.

The regime is arranged, not assumed. The pin's padding is
`height − printed − view − 1`, and production labels the positive branch
"screen not full" ([model.go:1726](../../../internal/tui/model.go)). A
filler count chosen for a 30-row tmux pane left an 80-row iTerm2 window on
the other side of that branch, and an earlier draft of this record reported
those runs as "full". The filler is computed from the terminal's own height
now, and every run prints what it arranged.

| terminal | payload | regime | stranded | control at same fill | instrument |
|---|---|---|---|---|---|
| iTerm2 3.7.2, 180×80 | OSC 1337 `height=12` ×3 | full (fill 90, rows 80) | **3** | PLAIN ×3, clean | operator's run; transcript frozen in `tools/pinprobe/testdata`, frame count only |
| tmux 3.7c, 120×30 | sixel ×3, at 36 / 72 / 144px | full | **3** each | PLAIN ×3, clean at gap 1 | `pinprobe -drive`; captures frozen in testdata |
| tmux 3.7c, 120×30 | OSC 1337 and kitty ×3 | full | 0 | — | same run: this tmux swallows them, so they are controls, not measurements of drawing |
| both | every case | not full | 0 | identical | same runs |

Four facts:

1. **A terminal that draws what the counter cannot see strands one frame
   per image, once the screen is full** — measured on two terminals and two
   protocols, each against a control at the same fill. That is the damage
   this record exists to prevent.
2. **Nothing happens while the screen is not yet full.** The pad absorbs it,
   which is why the regime has to be arranged before anything is claimed.
3. Under tmux the gap widens with the picture — −13, −16, −22 rows for 36,
   72, 144px — while the count stays 1.
4. **An earlier draft said five images on iTerm2 moved nothing. Withdrawn:**
   that run was in the not-full regime. The second verification pass deduced
   it from the pad arithmetic before the re-take confirmed it.

One instrument note belongs here, because it nearly hid the result: the
footer is `<model> · ctx … · <dir>`, and an image drawn over a stranded
frame's left half covers the model sentinel. Counting by that alone reported
a clean pin for a screen carrying three stranded frames. Both sentinels
count now, and a test pins the case.

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
  placeholders). An earlier draft said `consumedRows` would credit it one
  row too many; that is wrong for the configuration decision 3 mandates —
  with the payload alone on its line the cursor stays at column 1 and
  `consumedRows` returns 0, which its own test pins. The real exposure is in
  the runtime, not the probe: decision 1 takes the count from the
  declaration, so such a terminal would be over-counted by the whole N.

## Decision

### 1. The emitter declares the box; the counter is told

`physicalRows` never measures an image. An image segment carries the row
count its payload declares — `height=N` for iTerm2, `r=N` for kitty — and
`emit` uses **that** number **in place of** the one row `physicalRows` would
otherwise floor to, not in addition to it. There is no second path to the
number.

### 2. The declaration covers columns too, or the lane is half-built

The emitter declares a column count as well and clamps it below `m.width`.
A declared box is a cage in two dimensions or it is not a cage.

The reason is the **row** count, not the renderer. An earlier draft argued
that `wrapForScrollback`'s strictly-narrower invariant goes unenforced for
an image line, and a verification pass refuted it: Bubble Tea's inline
renderer gates on `ansi.StringWidth(line) < r.width` too, so a zero-width
line always gets its `EraseLineRight` and queued lines are never truncated.
The renderer is blind in the same way and comes to no harm. What does harm
is an image wider than the terminal: the terminal wraps the picture itself,
adding rows the declared height never claimed, and decision 1's count is
wrong again by a number nobody declared. Clamping the width is how the
declared height stays true.

### 3. An image occupies its own line, on the lane ADR-0063 built

Measured: text sharing the line with an image is split across its first and
last rows. An image segment is alone on its line and goes to the terminal
verbatim — the same lane, and the same reason, as ADR-0063 §3's art.

### 4. Two protocols, and only the ones that can declare

**iTerm2 `OSC 1337 File=` and the kitty graphics protocol**, both of which
take the row count as a parameter — decision 1's precondition.

**Sixel is not taken.** It cannot declare a row count, which is decision
1's precondition, and that reason stands alone without any measurement.

A second reason was withdrawn in the rewrite and is **restored here, because
withdrawing it was the error.** The organization's supply-chain rule — the
dependency floor is stdlib, then a vendor's own SDK, then the REST API
directly — is a standing 是, not a per-case question, and a sixel encoder
would have to come from a community package. The rewrite dropped it on the
ground that it "appears nowhere in this repository", which was a negative
claim made without enumerating: the rule is written in the sibling runtime's
RFP and in `internal/llm/openai.go` there, and its absence from
`CONVENTIONS.md` was already recorded as a known property rather than as
evidence of absence. `mermaid-ascii` is not the counter-example it was taken
for either: ADR-0042 is dated 2026-08-22, before the rule, so it is a
pre-policy dependency. Whether it is an exception or a debt is still a
separate question, and still not settled here.

### 5. One source, because the runtime chose its path

**The bytes of an image content block in an MCP tool result — the bytes,
never a path.** The intake decodes the block and writes it to the session
work directory, handing the model
`[image saved at <path> … use view_image on that path]`
([mcpresult.go:184](../../../cmd/mcpresult.go)) — the bytes do not ride back
inline ([ADR-0058](0058-session-work-directory.md) names where file-mediated MCP output lands; an earlier draft cited ADR-0027, which
is about operator audio and video and in fact permits inline bytes).

The view layer is handed **the decoded bytes the intake already holds**, and
opens nothing. That is not a convenience: a verification pass found that
`write` short-circuits on `os.Stat(path) == nil`
([mcpresult.go:207](../../../cmd/mcpresult.go)), and an MCP server is a
local child process that knows its own name, its tool name and the bytes it
is about to return — so it can compute the content-addressed filename in
advance and plant a symlink there. The runtime then writes nothing and the
path resolves wherever the server chose. Today that is contained, because
reaching the file means `view_image` and `Agent.decide` resolves it to the
real path before the enforcers judge it. A view layer that opened the path
would have walked straight back into the class this decision rejects a
model-named path for. Carrying the bytes removes the file open, and with it
the question.

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

`tea.NewProgram` is constructed at one site in the product
([root.go:1707](../../../cmd/root.go)), reached only when the session is
interactive; one-shot `-p` and the plain REPL return before it, so they
never draw — the same boundary the diagram lane already has. (An earlier
draft said "exactly one site" in the module, which `tools/pinprobe` has
since made false: it constructs one too, which is what lets it drive the
real model.) The capability is probed **once, before `tea.NewProgram`**, and cached. The
reason is raw-mode stdin ownership, not protocol decoding: once Bubble Tea
owns stdin, a terminal's reply to a query arrives in the input box as
phantom keystrokes — the recorded instance is `newGlamourRenderer`'s note on
why `WithAutoStyle` is deliberately absent
([model.go:449](../../../internal/tui/model.go)). An earlier draft cited
`AGENTS.md:296`, which is off by one and, more to the point, is about
disambiguating *keyboard* input, not query replies. The probe takes seconds,
drains before querying, and treats no reply as *no capability* — an
abandoned cursor report is misfiled into the next query, not lost (measured
building `rowprobe`: 11 sent, 5 read, 6 landing on the shell prompt after
exit).

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
  it. **lagent ADR-0005** settled ingestion on that side; this settles the
  screen. (Unprefixed numbers in this record mean gem-agent's own log, and
  gem-agent ADR-0005 is a different decision entirely — an earlier draft
  wrote it bare.)
- **What is unmeasured stays unmeasured**: kitty and Ghostty honouring `r=`,
  Terminal.app's protocol support, and the per-image cost. `auto` should not
  be trusted in a streaming turn until the last of those is measured.
- Two verification passes produced 17 and 21 findings. The classes were, in
  order of what they cost: a lane opened without enumerating its dimensions
  (closed by decisions 2 and 5), claims about adjacent code asserted without
  reading it (closed by citing file and line, and by this round's repair of
  four citations that were still wrong), "measured" claims wider than the
  instrument (closed by making `pinprobe` carry its own numbers and by
  arranging the regime), and a withdrawal that swept the documents but not
  the instruments (closed in `tools/`). Two findings are recorded and **not
  adopted**: the ADR-number collision with the ported `gem-agent ADR-0020`
  in lagent, because every citation there is already qualified and the
  architecture test passes; and the request to name the instrument for the
  iTerm2 row as anything better than a human reading a transcript, because
  no screen reader for that terminal exists here — the transcript is frozen
  instead, and the row claims only what a transcript can carry.
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
- `tools/pinprobe` — what the accounting costs on the production path:
  `-drive` runs the experiment and prints the table it measured, the regime
  is arranged from the terminal's height, every run carries its control, and
  the captures behind the rows above are frozen in its `testdata`
- `tools/imgpayload` — one payload builder, so the two probes cannot drift
- ADR-0003 / ADR-0028 (the pin and its self-heal), ADR-0063 §2–3 (the lane
  and the prompt's silence), ADR-0085 / ADR-0086 (who may open a file)

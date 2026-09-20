# ADR-0089: inline images declare their box — the counter is told, never measures

| Field | Value |
|-------|-------|
| Status | **Accepted** (2026-09-17) — implemented; decision 3 gained the erase after the first run on a real terminal |
| Date | 2026-09-16 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Operator: when the terminal supports graphics, can a reply draw them inline — here and in lagent? |
| Rewritten because | An independent verification pass returned 17 findings in three classes: claims about adjacent code asserted without reading it, "measured" claims wider than the instrument that produced them, and a lane opened without enumerating the dimensions it opens. Patching seventeen sentences is the response the root-cause rule forbids, so the premises were measured again and the record rebuilt on them |
| Relates to | [ADR-0002](0002-tui.md) (Bubble Tea inline TUI), [ADR-0003](0003-bottom-pinned-layout.md) / [ADR-0028](0028-self-healing-line-counter.md) (the row accounting and its self-heal), [ADR-0042](0042-terminal-diagrams.md) / [ADR-0063](0063-diagram-fences-render-in-place.md) (the view-layer lane), [ADR-0085](0085-credential-reads-are-operator-only.md) / [ADR-0086](0086-the-kernel-reads-the-file.md) (who may open a file) |

## Context

### What the counter can and cannot see

`emit` ([model.go:872](../../../internal/tui/model.go)) prints one line into
scrollback and counts its physical rows; the bottom pinning rests on that
count. Measured against `charmbracelet/x/ansi` v0.11.6, the version
`go.mod:14` pins: `ansi.StringWidth` returns **0** and `ansi.Strip` returns
the empty string for an iTerm2 `OSC 1337 File=`, a kitty `APC _G` and a
sixel `DCS q` alike. `ansi.Hardwrap` leaves all three **byte-identical**, so
`wrapForScrollback` ([model.go:1130](../../../internal/tui/model.go)) does
not shear a base64 run. An independent pass re-measured it with a
real PNG and a real sixel — the repository's own test had covered two of the
three families, and `tools/imgpayload` now carries all three. A draft then over-corrected the
other way, saying the wider re-measurement (more widths, both
`preserveSpace` settings, a multi-chunk kitty sequence) "is not in this
repository". The *runs* are not frozen; every input is. A later pass
reproduced all of it from `tools/imgpayload` alone — seven payloads, nine
widths, both settings, 126 combinations, every one byte-identical — and
`TestKittyChunks` already builds the multi-chunk sequence. What is missing
is a test that asserts it, not the ability to.

The counter is not blind, though, and the first draft said it was.
`physicalRows` ([model.go:1143](../../../internal/tui/model.go)) starts at
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
"screen not full" ([model.go:1840](../../../internal/tui/model.go)). A
filler count chosen for a 30-row tmux pane left an 80-row iTerm2 window on
the other side of that branch, and an earlier draft of this record reported
those runs as "full". The filler is computed from the terminal's own height
now, and every run prints what it arranged.

| terminal | payload | regime | stranded | control at same fill | instrument |
|---|---|---|---|---|---|
| iTerm2 3.7.2, 180×80 | OSC 1337 `height=12` ×3 | full (fill 90, rows 80) | **3** | PLAIN ×3, clean | operator's run; transcript frozen in `tools/pinprobe/testdata`, frame count only |
| tmux 3.7c, 120×30 | sixel ×3, at 36 / 72 / 144px | full | **3** each | PLAIN ×3, clean at gap 1 | hand-driven tmux + `capture-pane` on the pre-`-drive` build; only the 72px capture is frozen in testdata, and `-drive` reproduces the experiment but produced none of these files |
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
  tmux. Decision 4 binds two protocols; one of them has no measurement.
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
last rows. An image segment is alone on its line and goes to the terminal verbatim,
preceded by one erase — the same lane as ADR-0063 §3's art, plus a thing
art never needed.

**The erase is not decoration, and only a real terminal found it.** Bubble
Tea flushes a queued line from the top of its own frame and appends
`EraseLineRight`, which clears ONE row. An image then draws down N rows at
its declared width, so every cell of the old frame to the RIGHT of a
narrower picture survives on every row the picture covers. Measured on
iTerm2 with the declared count already correct: three images, three
stranded footers — the same damage this record was written to prevent,
from a second cause it had not enumerated. `ansi.EraseScreenBelow` before
the payload removes the frame the renderer is about to repaint below us
anyway, and touches nothing above the cursor. Four verification passes
missed this; none of them had a terminal.

### 4. Two protocols, and only the ones that can declare

**iTerm2 `OSC 1337 File=` and the kitty graphics protocol**, both of which
take the row count as a parameter — decision 1's precondition.

**Sixel is not taken.** It cannot declare a row count, which is decision
1's precondition, and that reason stands alone.

A second reason was tried twice and is **removed rather than adjudicated a
third time**. The organization's supply-chain rule was first withdrawn here
as unsupported (wrong: it is written in the sibling runtime's RFP and its
`internal/llm`), then restored as a reason to refuse a sixel encoder, then
narrowed to "API clients" on a downstream project's wording — three
readings in three rounds, on a standing organization 是 that this record has
no business scoping. **Its reach is not settled here and decision 4 does not
rest on it.** If it is ever written into `CONVENTIONS.md`, that is where the
question belongs.

### 5. What may be drawn is NOT settled here — one constraint is

This decision has been written three times and refuted three times, each
time by a verification pass, and each time the refutation was the worst
finding of its round:

| draft | the source it named | why it failed |
|---|---|---|
| first | a local image path the model names | a view-layer file open is not a tool call: it never reaches `Agent.decide`, never runs in ADR-0086's sandboxed child, and is invisible to the credential list, which is keyed on built-in tool name ([risk.go:176](../../../internal/risk/risk.go)) — the class ADR-0085/0086 repaired |
| second | the path the MCP intake wrote | `write` short-circuits on `os.Stat(path) == nil` ([mcpresult.go:234](../../../cmd/mcpresult.go)), and a server knows its own name, its tool name, the bytes it will return **and the work directory, which every call hands it** as `_meta[workdir.MetaKey]` ([client.go:605](../../../internal/mcp/client.go)) — so it can plant a symlink at the content-addressed path and the runtime writes nothing |
| third | the decoded bytes the intake holds | **there is no such carrier.** `render` returns a `string` ([mcpresult.go:53](../../../cmd/mcpresult.go)), `mcpIntake` retains no bytes, and `Tool.Run` is `func(ctx, args) (string, error)` ([tools.go:65](../../../internal/tools/tools.go)). The bytes are a local; after `Run` returns they are unreachable |

Three drafts in the same place is not three mistakes, it is one: **the source
cannot be named until the plumbing exists.** Getting an image's bytes from an
MCP tool result to the view layer means a channel beside the string-only tool
contract that the transcript, resume and error paths all rest on. That is a
decision with its own dimensions to enumerate, and writing it as a bullet
inside a record about row arithmetic is what produced three refutations.

So this record settles the accounting and **defers the source**. [ADR-0090](0090-an-images-bytes-never-become-a-path.md)
decides what may be drawn and how its bytes travel, and it inherits one
constraint this one did earn:

**The view layer opens no file.** Whatever the source turns out to be, the
bytes must arrive by a channel the enforcers already govern, because a read
performed by the view layer is not a tool call and no enforcer in either
runtime can see it. That constraint held against all three drafts; it is the
part of this question that is settled.

Two things the deferred decision must also answer, found in the same passes
and recorded so they are not rediscovered: nothing today bounds an image's
size except the JSON-RPC frame cap (`scannerMax = 10 MiB`,
[client.go:28](../../../internal/mcp/client.go)) — the response budget bounds
the *note*, not the data — and a block whose `binaryNote` does not fit the
response budget is neither saved nor described individually
([mcpresult.go:105](../../../cmd/mcpresult.go)), so whether it may still be
drawn is undecided.

### 6. Only the view layer emits an image escape

Bytes arriving from a tool are data. The view layer decides a segment is an
image and writes the escape; nothing a tool returns is passed through as an
escape because it looks like one.

`internal/archtest` enumerates the sites that may emit one:
`termimg.Payload` is callable from `internal/tui` and nowhere else.
**This record said the implementation commit would carry that test, and it
did not** — an independent pass found the sentence standing alone, which by
the rule written into it makes the claim "as of today". The test was
written afterwards, so the sentence now describes what exists rather than
what was intended.

This does **not** close the existing surface: tool output is printed without
ANSI stripping — `ansi.Strip` is called at exactly one site in non-test code
([model.go:1148](../../../internal/tui/model.go)), inside `physicalRows`, to
*measure* — so raw escapes from shell output already reach the terminal.
Pre-existing, not widened here, not repaired here.

### 7. Drawing is a TUI-only capability, and the other entrances say so

`tea.NewProgram` is constructed at one site in the product
([root.go:1751](../../../cmd/root.go)), reached only when the session is
interactive; one-shot `-p` and the plain REPL return before it, so they
never draw — the same boundary the diagram lane already has. (An earlier
draft said "exactly one site" in the module, which `tools/pinprobe` has
since made false: it constructs one too, which is what lets it drive the
real model.) The capability is probed **once, before `tea.NewProgram`**, and cached. The
reason is raw-mode stdin ownership, not protocol decoding: once Bubble Tea
owns stdin, a terminal's reply to a query arrives in the input box as
phantom keystrokes — the recorded instance is `newGlamourRenderer`'s note on
why `WithAutoStyle` is deliberately absent
([model.go:463](../../../internal/tui/model.go); the rule itself is in
`AGENTS.md`'s "Never query the terminal after Bubble Tea starts", which was
the right citation all along). An earlier draft cited
`AGENTS.md:296`, which is off by one and, more to the point, is about
disambiguating *keyboard* input, not query replies. The probe drains before
querying and treats no reply as *no capability* — an abandoned cursor report
is misfiled into the next query, not lost (measured building `rowprobe`: 11
sent, 5 read, 6 landing on the shell prompt after exit).

**The probe asks a second question, and this record first said it merely
"takes seconds".** That was the whole defect: the graphics query has no
negative answer, so silence could not be told from slowness and every
terminal that cannot draw paid the entire budget. Measured on Apple
Terminal: **2.001 s at every start**, with `TERM_PROGRAM` set, plus the
query's own body — `Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA` — printed on the
operator's screen, since that terminal does not parse APC. A
device-attributes request now rides in the same write; every VT-compatible
terminal answers it, so a DA1 reply with no graphics reply before it IS the
no. Re-measured with the same instrument: **under 1 ms, and a clean
screen.** The budget stays as the backstop for a terminal that answers
neither.

**The probe left a reader behind, and that reader took the terminal's next
input (corrected 2026-09-21).** The first implementation read the reply from
a goroutine and returned on the verdict, leaving the goroutine blocked in
`tty.Read`. On macOS `/dev/tty` cannot join kqueue, so the descriptor is a
blocking one and `Close` does not wake a read already in it. Measured on
Apple Terminal (macOS 27, three runs of three): 300 ms after `Close` had
returned, the stale reader received `ESC ] 11 ; rgb:1616/1818/2121 BEL` — the
answer to the background query `[tui] theme = "auto"` had just sent — and
termenv read the fallback `#000000` instead of `#161821`. On this dark
profile the fallback happened to be right, which is why nobody saw it; on a
light one the theme is wrong, and with a fixed theme the same read takes the
operator's first keystroke. It affected every terminal the environment cannot
classify, which is where `auto` asks: Apple Terminal, the VS Code terminal,
SSH. The same defect hid a second one. The parser stopped at the graphics
reply, so on a terminal that draws, the DA1 reply was never read by the
probe — the stale reader was what swallowed it.

The probe now reads on the calling goroutine, each wait bounded by
`select(2)` (not `poll`, which macOS does not support on `/dev/tty`), and
reads until DA1 has arrived: when it returns, nothing of it is reading the
terminal and nothing of the answer is left. Re-measured on the same terminal,
three of three: `#161821`, equal to the control with no probe; the verdict
still takes under a millisecond. `askKitty` had no test that touched a
terminal; it now has pseudo-terminal tests, and the one that states this
property fails on the first implementation for all three verdicts.

`[tui] images = "auto"` selects it
([config.go:194](../../../internal/config/config.go)), beside `theme` and
`language` (`:179` and `:183`). Inside a multiplexer
the answer is **off** — not because passthrough is someone else's
configuration, which is what the first draft said, but because the one
multiplexer measured rendering a payload stranded a frame for every image.

### 8. The runtime says nothing about images

No tool, no prompt paragraph. ADR-0063 §2's rule stands. The first draft
argued the model "already produces" these sources; that is a firing-rate
claim and this project has a measured precedent against making one without
a denominator — `render_diagram` fired once in 76 sessions. And §5 defers the
source, so there is no source for a prompt to steer toward — an earlier
draft argued from "decision 5's single source", which was the second of the
three refuted drafts, resurrected inside the record that refutes it. No test pins this yet: `cmd/prompt_test.go` pins the *diagram* silence
ADR-0063 asked for, and the prompt already mentions images elsewhere, so an
images clause needs its own absence test written with the implementation —
an obligation, not the present-tense claim an earlier draft made.

## Consequences

- The bottom pin survives images by construction rather than by care, in
  both dimensions.
- No aspect-ratio arithmetic and no cell-pixel-size query enter the runtime.
- Terminal.app — and any terminal that does not draw — loses nothing: the
  fallback is today's behaviour.
- The operator **does** see a tool's screenshot: [ADR-0090](0090-an-images-bytes-never-become-a-path.md)
  settled the source §5 deferred — the MCP intake, for a block it both saved
  and described — and the rows it costs were already known when it did.
  **lagent ADR-0005** settled ingestion on that side, and lagent ADR-0020 /
  ADR-0021 now mirror this decision and its source. (Unprefixed numbers here
  mean gem-agent's own log, and gem-agent ADR-0005 is a different decision
  entirely — an earlier draft wrote it bare.)
- **What is unmeasured stays unmeasured**: kitty and Ghostty honouring `r=`,
  Terminal.app's protocol support, and the per-image cost. `auto` should not
  be trusted in a streaming turn until the last of those is measured.
- Four verification passes. Two classes are closed by construction — the
  lane's dimensions (§2 for columns, §5 for the source, by refusing to name
  one) and "measured" claims wider than their instrument, inside `tools/`,
  where `pinprobe` now computes its own table and arranges its own regime.
- Two classes are **narrowed, not closed**, and the tests that narrow them
  are lints rather than proofs. `internal/archtest/adrcite_test.go` resolves
  every `file.go:NNN` link in these records and catches most line drift — it
  caught the off-by-two this round's own repair introduced — but a measured
  15–18% of ±N perturbations still pass, because a markdown table is one
  paragraph and pools its token set, and roughly a third of the line
  references in these records are in forms its regex does not match.
  `internal/archtest/withdrawn_test.go` catches verbatim repetition of nine
  English phrases across every read surface (217 at the time of writing), and is blind to paraphrase, to
  the Japanese half of every document, to a phrase that wraps at a line
  break, and to `cmd/` and `internal/`. Both are worth keeping. Neither
  entitles anyone to say the class is closed, and an earlier draft of this
  bullet said exactly that.
- Recorded and **not adopted**: the ADR-number collision with the ported
  `gem-agent ADR-0020` in lagent, because every citation there is qualified
  and the architecture test keeps it so; and the request to name a better
  instrument for the iTerm2 row than a human reading a transcript, because
  no screen reader for that terminal exists here — the transcript is frozen
  instead and the row claims only what a transcript can carry.
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

**A6. Let the model name a file to draw.** §5 — a view-layer read sits
outside every enforcer, and that constraint survived all three drafts of the
source question even though none of the sources did.

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

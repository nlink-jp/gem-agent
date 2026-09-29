# ADR-0093: text from outside the runtime is made inert before the terminal sees it

| Field | Value |
|-------|-------|
| Status | **Proposed** (2026-09-29) — implemented on main, unreleased; awaiting the operator's decision |
| Date | 2026-09-29 |
| Binds | gem-agent |
| Decision makers | nlink-jp maintainers |
| Triggered by | Independent pre-release review, 2026-09-29: model text reaches the terminal with escape sequences intact — `\x1b]0;T\a` passes the glamour renderer untouched in both the `notty` and `dark` styles, in plain paragraphs, code blocks and mermaid fences, and nothing sanitizes `TextDelta` before the live region or scrollback. Not introduced by the ADR-0092 work |
| Relates to | [ADR-0002](0002-tui.md) (the inline TUI), [ADR-0042](0042-terminal-diagrams.md) (**§4 is amended here**), [ADR-0089](0089-inline-images-declare-their-height.md) (§6 named this surface as pre-existing and not repaired; it is repaired here for the TUI), [ADR-0033](0033-turn-observability.md) (thoughts), [ADR-0047](0047-declared-purpose.md) (purpose), [ADR-0036](0036-ask-user-tool.md) (the ask dialog) |

## Context

### Who writes the text on this screen

The model's text is untrusted. A prompt-injected web page, file or tool result
can steer what the model writes, and the model quotes tool output it has read.
So can everything the model *writes as an argument*: a tool call's detail and
declared purpose are the model's words, shown in the event line and in the
approval dialog. A terminal does not read text; it executes some of the bytes
it is sent. An OSC sets the window title, writes the clipboard (OSC 52 on
terminals that allow it) or opens a hyperlink; a CSI moves the cursor or erases
the screen; APC `_G` (kitty) and OSC 1337 (iTerm2) draw images — the very lane
ADR-0089 built a declared-row account for, which an undeclared image breaks.

### Measured: what survives the renderers

The production renderer (`newGlamourRenderer`) and the model's own live and
flush paths were fed eighteen hostile strings — OSC 0, 2, 8, 52 and 1337, APC
`_G`, DCS, CSI erase / cursor position / SGR, C1 CSI and OSC as UTF-8 and as
raw 8-bit bytes, CR, BS, BEL, DEL and U+202E — each inside a paragraph, a code
block and a mermaid fence, in both styles. Every control character of every
string came out. The two styles differ only in what they do *around* a
sequence: `dark` styles text between an `ESC` and its parameters, which breaks
a CSI by accident in a paragraph, and both turn a raw 8-bit byte into U+FFFD.
Neither is a defence: the live region shows the raw bytes before either runs.

### Measured: what a real terminal does with them

`make escprobe` (tools/escprobe) runs the real model under the real inline
program in a private tmux 3.7c server with `set-clipboard on`, delivers each
string on each channel text reaches the TUI by, and reads the terminal back.
Against v0.85.0, **58 of 80 deliveries acted**:

| Case | reply, live | reply, flushed | thought | tool event | approval dialog |
|---|---|---|---|---|---|
| OSC 0 / OSC 2 (title) | set | set | set | set | set |
| OSC 52 (clipboard) | written | written | written | written | written |
| CSI 2J (erase screen) | erased | erased | erased | erased | erased |
| CSI 3;60H (cursor) | moved | — ¹ | moved | moved | moved |
| CR | hid the line start | hid | hid | hid | hid |
| BS | overwrote | overwrote | overwrote | overwrote | overwrote |
| OSC never terminated | hid the rest | hid | hid | hid | hid |
| OSC 8, OSC 1337, DCS | parsed | parsed | parsed | parsed | parsed |
| APC `_G` | parsed | — ¹ | parsed | parsed | parsed |
| C1 CSI / OSC, UTF-8 or 8-bit | — ² | — ² | — ² | — ² | — ² |

¹ glamour's `dark` styling split the sequence — the accident above, not a guard.
² tmux does not act on C1 in UTF-8 mode; that is tmux's parser, not this
runtime, and a terminal that honours 8-bit controls would.

Two rows matter beyond decoration. **The approval dialog is spoofable today**:
a CR in a `shell_exec` command hides everything before it, and an OSC that is
never terminated hides everything after it, so the command the operator reads
is not the command that runs. And an image escape in text reaches the screen
through no declared box, which ADR-0089 §6 said only the view layer could emit
— its test pins the *code* that builds payloads, and text was never code.

"parsed" means the terminal consumed the bytes as a control (tmux does not draw
kitty, iTerm2 or sixel images; a terminal that does would draw them).

### The other entrances

The plain REPL and `-p` write the model's deltas with `fmt.Fprint` to stdout
(ADR-0042 §4: "stdout is model text only"), and tool events, approval prompts
and the ask dialog to stderr. When either stream is a terminal, the same bytes
reach it with nothing in between.

## Decision

### 1. What is removed: control characters, and nothing else

A rune is removed when it is:

- a C0 control (U+0000–U+001F) other than tab and newline — ESC, CR, BS and
  BEL among them;
- DEL (U+007F) or a C1 control (U+0080–U+009F);
- a bidirectional embedding, override or isolate (U+202A–U+202E,
  U+2066–U+2069), which reorder what the operator reads — in an approval
  dialog, a command — on a terminal that implements bidi.

Invalid UTF-8 becomes U+FFFD, so a raw 8-bit C1 byte cannot survive as a byte.
The set is finite and is one predicate in one package (`internal/inert`).

**The characters are removed; the sequence bodies are not.** Every terminal
control begins with ESC or a C1 introducer, so once those are gone nothing that
remains can start a sequence, and what was a sequence's body is ordinary text:
`\x1b]0;T\a` shows as `]0;T`. Removing the whole sequence was the obvious
reading of the finding and is rejected (A1): an OSC, DCS or APC runs until its
terminator, and one that is never terminated runs to the end of the string —
stripping it would do in the runtime exactly what the terminal does in the
table above, hide the rest of an approval command. Removing characters hides
nothing the operator could have read. The residue is evidence that something
was attempted, and the transcript keeps the bytes as they were.

Nothing is replaced with a visible marker (A2): the operator asked for the
characters to be stripped, and a marker per character is a report, not a
control — the removal is the control.

### 2. Where: once, at the TUI's ingress

Every message whose type is declared in `internal/tui` has every string it
carries rewritten at the top of `Update`, before any branch reads it: fields,
slices of strings, nested structs, and an error's text (the error is wrapped,
so `errors.Is` still sees the original). It is done by reflection, so a field
added later is covered the day it is added. A byte slice is not text — the
bytes of a `tui.Image` are a picture the view layer encodes — and is left alone.

The string-returning callbacks that can carry text the model wrote are wrapped
once, in `New`: the slash handler (`/memory` lists what `save_memory` saved),
the skill expander's error, and the banner lines (startup notes quote MCP
server output and paths).

Nothing downstream calls the function. glamour's styling, lipgloss's colours,
Bubble Tea's cursor control and `termimg` payloads are written *after* the
ingress, by the runtime, and pass untouched. The diagram note is built from a
source that is already inert, so `noteSafe` stays what it is — a Markdown
guard. A renderer that also sanitized would be a second mechanism, and the
knowledge base records what that costs: the one hides the absence of the other.

### 3. The plain REPL and `-p`: inert on a terminal, verbatim elsewhere

`runREPL` replaces the command's stdout and stderr, once, at its top, with an
inert writer **when that stream is a terminal**, and leaves it alone otherwise.
This is the host's own convention: `ls` prints non-graphic characters as `?`
"by default when output is to a terminal" and raw when it is not (`ls -q` /
`ls -w`, macOS `ls(1)`).

ADR-0042 §4's contract — stdout is model text only — keeps its meaning for the
consumer it was written for: a program reading a pipe or a file gets every
byte. A terminal is not a consumer of bytes but a device that executes some of
them, and `-p` typed at a prompt is the ordinary manual use. Always-inert would
break the contract for no one it protects; never-inert would leave the
approval prompt on stderr spoofable exactly as the TUI's was.

The writer keeps an incomplete UTF-8 sequence at the end of one write for the
next, so a rune split between writes is not turned into two U+FFFD.

### 4. What stays outside, and why that is accepted

- **`gem-agent -p … | cat`, `| less -R`.** The operator hands raw bytes to a
  terminal by choosing the pipe, as with `ls | cat`.
- **The completion candidates and the settings panel.** File names and
  configured values; their authors are the operator's filesystem, the
  operator's configuration and the MCP servers the operator runs. The model's
  route to a file name is a write, and its approval now shows the name inert.
- **The transcript.** Verbatim by design: it is the record, and the evidence.
- **Other subcommands** (`sessions`, `workdirs`, `trust`) print the operator's
  own state.
- **`!` output is covered**, because it arrives as a message. A program forced
  to colour through a pipe loses its colours and shows `[32m`-style residue;
  most programs do not colour a pipe.

### 5. How it is held

- A behaviour test reads the message types off `msgs.go` with `go/ast`, fails
  while a type has no case, fills every string of every case with a hostile
  string by reflection, drives it through `Update`, the flush and `View`, and
  asserts two things: no control character of the hostile string is in the
  output, and its text *is* — without the second, a message that shows nothing
  would pass vacuously.
- An architecture test pins the callers of `internal/inert` to the two
  ingresses (the TUI's and `runREPL`'s streams), so a renderer that starts
  sanitizing on its own fails the build rather than hiding a gap.
- `make escprobe` re-runs the terminal table.

### 6. lagent

lagent's `internal/tui` has the same path (`case TextDelta` appends the raw
chunk) and so the same defect. AGENTS.md's rule is that a defect in a shared
mechanism is fixed in both; this work was scoped to gem-agent by the operator,
and the port is the next piece of work. This line is the record of the
divergence until it lands.

## Consequences

- The measured table goes to zero deliveries acting on every channel.
- The row account ADR-0089 rests on is exact for text again: a line of model
  text can no longer carry a zero-width escape that moves the cursor.
- A model can no longer emit a hyperlink, a colour or a picture through its
  text. None of these was ever offered to it.
- Legitimate text with a CR (a CRLF line ending) loses the CR. Tabs and
  newlines are kept.
- **ADR-0042 §4 is amended**: plain REPL and one-shot output is verbatim to a
  pipe or a file and inert to a terminal.
- **ADR-0089 §6's pre-existing surface is repaired for the TUI** — `!` shell
  output reaches the screen as a message and is now inert.

## Alternatives considered

- **A1. Strip whole escape sequences** (the finding's wording). Rejected: an
  unterminated string sequence runs to the end of the text, so stripping it
  hides the rest of a command in the approval dialog — the measured spoof,
  reproduced by the defence. A CSI's final byte is a letter, so stripping one
  also eats a letter of the text after it.
- **A2. Replace each control with a visible picture (␛, ␍).** Faithful, and a
  report per character; the operator asked for removal, and the transcript
  keeps the bytes.
- **A3. Sanitize at each print site** — the live view, the flush, the note, the
  dialog, the event line. Rejected: the knowledge base records a CLI where the
  independent review found five print sites the call had been forgotten at, all
  short fields nobody suspected. The channels measured here are five, not one.
- **A4. Filter Bubble Tea's output stream** with an allowlist. Rejected: after
  rendering, the runtime's own cursor control, erases and image payloads are
  the same bytes an attacker needs; the author cannot be told apart there.
- **A5. `ansi.Strip` from x/ansi.** Rejected: it removes whole sequences (A1)
  by a parser of another project's choosing, for a decision this small.
- **A6. Sanitize in the agent core, before the transcript.** Rejected: it
  changes the record and the data the model receives. Making text inert for a
  display is the display's job.
- **A7. Plain and `-p`: always verbatim, or always inert.** Rejected in §3.

## References

- The knowledge base, security: "Make third-party text inert for a terminal
  once, over the whole result — not at each print site".
- macOS `ls(1)`, `-q` and `-w`.
- `tools/escprobe`, `make escprobe`.

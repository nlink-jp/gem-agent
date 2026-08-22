# Attachments: @-references

`@<path>` attaches a file or directory to your message. The reference
is parsed from what **you** type — never from model output or tool
results — which is what makes the out-of-project exceptions below safe.
Tab completes the path; anything that cannot be attached is reported
rather than silently dropped, and attached content reaches the model
isolated as untrusted data, exactly like tool output.

## Files and directories

`@src/main.go これ直して` sends the file with the instruction;
`@docs/` sends a directory listing. References resolve inside the
project only, symlinks included.

## Images (ADR-0012)

Screenshots are first-class input. Three ways in, plus one for the
model:

| Route | Example |
|---|---|
| Project file | `@docs/mock.png これを再現して` |
| Anywhere (attachment extensions: images, documents, audio/video) | `@~/Desktop/スクリーンショット.png` |
| Clipboard | Cmd+Ctrl+Shift+4, then `@clipboard ここがおかしい` |
| Model-initiated | the `view_image` tool (project-confined, like `read_file`) |

MIME is sniffed from bytes (a renamed binary is refused), images are
capped at 8MB each and 4 per message, and a too-large image is refused
whole — a truncated PNG is a broken file, not a smaller picture.

Images cannot be nonce-wrapped, so the isolation stance is stated as
framing instead — text visible inside an image is data, never
instructions — which is weaker than tag isolation, and the docs say so
rather than pretending otherwise. Transcripts store the bytes, so a
resumed session keeps the screenshots it was looking at.

## Documents (ADR-0026)

PDFs go to the model as-is — it reads layout, tables, and scans
natively (measured: accepted both as an operator attachment and inside
a tool response, with clean continuation). Word/Excel/PowerPoint
(.docx/.xlsx/.pptx) are extracted to text locally with the standard
library: paragraphs, tab-separated sheet rows, numbered slides.

Two paths per ADR-0012's split: `@report.pdf` / `@data.xlsx` when you
choose the file (absolute and ~ paths allowed — you typed them), the
`read_document` tool when the model does (project-confined). Legacy
.doc/.xls/.ppt are out of scope, stated loudly. PDFs cap at 12MB
(a clipped PDF is a broken file), Office files at 32MB compressed with
a bounded extraction budget.

## Audio and video (ADR-0027)

Attach recordings and clips with `@memo.m4a` / `@clip.mp4` (in-project,
absolute, or ~ paths) — the model transcribes and understands them
natively.

With `[gcp] bucket` set, media ALWAYS routes through your GCS bucket as
a `gs://` URI (content-addressed, deduplicated, verified against the
hash during upload, never deleted by gem-agent — set a bucket lifecycle
rule): inline bytes would be re-sent with every round's history replay,
while a URI is a few dozen bytes. Without a bucket, media attaches
inline up to 15MB and larger files are refused naming both remedies.
Uploads run under the turn's context, so Ctrl+C reaches them.

Verified live: an inline voice memo transcribed exactly, and a
bucket-routed video answered from both its audio track and its frames.

One retention consequence: a transcript that references a `gs://`
object replays it on every turn. If your lifecycle rule deletes the
object, the turn error says so and names the way out (`/clear`, or
`/compact` past the attachment).

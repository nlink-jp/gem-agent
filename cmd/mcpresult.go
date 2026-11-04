package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// mcpIntake turns one MCP tool result into what the model sees.
//
// It exists because deciding that a result is too large, or is not text
// at all, is the client's job and was being done in the wrong place.
// Built-in tool results have always been bounded by tools.OutputCap;
// MCP results were passed through whole, so every server in the fleet
// grew a workspace_root of its own to keep from flooding a context it
// cannot measure. A server cannot know the model's context window. This
// side can (ADR-0058).
//
// Nothing is dropped. Text past the cap is written to the session work
// directory and the model is handed a preview and the path — the work
// directory is a root of the file tools, so read_file reaches it.
// Non-text blocks are written the same way; an image comes back as a
// path the model can hand to view_image, which is the deliberate look
// ADR-0012 designed and keeps media out of the replayed history that
// ADR-0027 was written to protect.
//
// Every text block is saved, not only the oversized ones (ADR-0105 §1):
// a result that exists only in the model's context can be kept as
// evidence only by re-typing it, and a re-typed file has no provenance.
// What was saved is reported beside the text, as tools.Saved, for the
// executor to record where no server can write.
type mcpIntake struct {
	// workDir is where oversized and binary blocks land. Empty means
	// there is nowhere to write, and the fallback is a truncation that
	// says so — losing part of an answer silently is the one outcome
	// this must never produce.
	// It is read per call: /clear rotates the directory (ADR-0071 §2).
	workDir func() string
	// cap is the byte size above which a text block is spilled.
	cap int
	// headRunes and tailRunes bound the preview of a spilled block
	// (ADR-0096 §1). The tail is where formats that append metadata put
	// it — a server's `"truncated": true, "total_rows": 200` arrives as
	// the last bytes — and keeping it needs no parsing of any format.
	headRunes, tailRunes int
}

func newMCPIntake(workDir func() string) mcpIntake {
	return mcpIntake{workDir: workDir, cap: tools.OutputCap, headRunes: 600, tailRunes: 200}
}

// render assembles the text the model receives for one call, and
// reports every file it wrote (ADR-0105 §1). A result the server marked
// isError renders the same way; the adapter wraps it in a
// tools.RemoteError and the executor says whose words it is (ADR-0075
// §1) — the intake stops prefixing `error:` itself.
func (in mcpIntake) render(server, tool string, blocks []mcp.Content) (string, []tools.Saved) {
	parts := make([]string, 0, len(blocks))
	var saved []tools.Saved
	// The inline text blocks are saved together, after the loop, as one
	// file: what the model received inline, one record and one note
	// however many blocks there are (independent review: a note per
	// block bypassed the one budget — twenty thousand empty blocks
	// were twenty thousand notes). A single block is saved as its own
	// exact bytes.
	var inline []string
	// One budget for the whole response (ADR-0072 §4.5): many blocks
	// each under the cap used to add up without limit. A block that no
	// longer fits the remaining budget is spilled like an oversized
	// one, so the inline text never exceeds one cap.
	budget := in.cap
	var rest []string // text blocks past the budget, saved together
	leftBinary, savedUnlisted := 0, 0
	fits := func(piece string) bool {
		sep := 0
		if len(parts) > 0 {
			sep = 1
		}
		return len(piece)+sep <= budget
	}
	spend := func(piece string) bool {
		// Every rendered piece — inline text, a spill preview and its
		// path, a binary note — is paid from the one budget, plus the
		// separator before it (review after v0.68.2, R06: previews of
		// oversized blocks were free, and a hundred of them dwarfed the
		// cap). The first piece pays no separator, so a block of exactly
		// the cap still renders inline as it always did.
		sep := 0
		if len(parts) > 0 {
			sep = 1
		}
		if len(piece)+sep > budget {
			return false
		}
		budget -= len(piece) + sep
		parts = append(parts, piece)
		return true
	}
	for _, b := range blocks {
		if b.Type == "text" || (b.Type == "" && len(b.Data) == 0) {
			if len(rest) > 0 {
				rest = append(rest, b.Text)
				continue
			}
			if spend(b.Text) {
				// Inline, and saved all the same (ADR-0105 §1). The note
				// that says so is the executor's, outside the nonce tag,
				// so the inline text stays exactly the server's.
				inline = append(inline, b.Text)
				continue
			}
			if len(b.Text) > in.cap || len(parts) == 0 {
				// Oversized, or the first block that does not fit: the
				// preview path, so the reader sees its head. Its note is
				// sized before anything is written — the path is known
				// from the content — so a preview that will not fit
				// leaves no file behind that nothing records.
				if fits(in.spillNote(server, tool, b.Text)) {
					piece, rec := in.spillText(server, tool, b.Text)
					if spend(piece) {
						saved = append(saved, rec)
						continue
					}
				}
			}
			rest = append(rest, b.Text)
			continue
		}
		// A non-text block past the budget is neither saved nor
		// described one by one (review after v0.68.2, R06: a note per
		// block was itself unbounded); the leftovers are one line. The
		// note's length is known before the write — the path is
		// content-addressed — so nothing is saved that will not be
		// listed (pre-release review).
		if len(b.Data) == 0 || len(in.binaryNote(server, tool, b, in.pathFor(server, tool, extForMIME(b.MIME, b.Type), b.Data)))+1 > budget {
			leftBinary++
			continue
		}
		piece, rec := in.binary(server, tool, b)
		if rec.Path != "" {
			// On disk whether or not its note fits: the record says
			// what exists, the note what the model was told.
			saved = append(saved, rec)
		}
		if !spend(piece) {
			leftBinary++
			// Only a block that really reached disk is counted as
			// saved-but-unlisted. The guard above sizes the exact
			// success note, so today this cannot happen at all; before
			// the count was conditioned on the write it fired on the one
			// case that IS possible here — a failed write whose note
			// happens to be longer — and reported the block as saved.
			if rec.Path != "" {
				savedUnlisted++
			}
			continue
		}
		// Nothing is drawn here (ADR-0091 §1). An image block is how a
		// tool result carries an image into the MODEL's context; what
		// the operator sees is the model's to decide, through
		// show_image, or the operator's own through /show.
	}
	if len(inline) > 0 {
		joined := strings.Join(inline, "\n")
		saved = append([]tools.Saved{in.save(server, tool, textExt(joined), []byte(joined))}, saved...)
	}
	if len(rest) > 0 {
		piece, rec := in.spillRest(server, tool, rest)
		parts = append(parts, piece)
		saved = append(saved, rec)
	}
	if leftBinary > 0 {
		note := fmt.Sprintf("[%d more non-text block(s) past the response budget — not saved; narrow the call and ask again]", leftBinary)
		if savedUnlisted > 0 {
			note = fmt.Sprintf("[%d more non-text block(s) past the response budget — %d saved in the work directory but not listed, %d not saved; narrow the call and ask again]", leftBinary, savedUnlisted, leftBinary-savedUnlisted)
		}
		parts = append(parts, note)
	}
	out := strings.Join(parts, "\n")
	if out == "" {
		out = "(no content)"
	}
	return out, saved
}

// spillRest saves the text blocks past the response budget as one file
// and tells the model where, with no preview.
func (in mcpIntake) spillRest(server, tool string, texts []string) (string, tools.Saved) {
	joined := strings.Join(texts, "\n")
	rec := in.save(server, tool, ".txt", []byte(joined))
	if rec.Err != "" {
		return fmt.Sprintf("[%d more text block(s), %d bytes, past the response budget and not saved (%s) — narrow the call and ask again]",
			len(texts), len(joined), rec.Err), rec
	}
	return fmt.Sprintf("[%d more text block(s), %d bytes — past the response budget, saved whole. Read them with read_file (offset/length reach any byte), or narrow the call and ask again: read_file %s]",
		len(texts), len(joined), rec.Path), rec
}

// textExt names a text block's file: .json when it reads as JSON, so the
// operator (and any shell command the model writes) can tell.
func textExt(s string) string {
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		return ".json"
	}
	return ".txt"
}

// spillText saves a block to the work directory and returns a preview
// of its head and tail with the path.
func (in mcpIntake) spillText(server, tool, s string) (string, tools.Saved) {
	rec := in.save(server, tool, textExt(s), []byte(s))
	if rec.Err != "" {
		preview, shown := headAndTail(s, in.headRunes, in.tailRunes)
		return fmt.Sprintf("%s\n\n[%d bytes: only %s are shown above and the rest could not be saved (%s), so it is lost — narrow the call and ask again]",
			preview, len(s), shown, rec.Err), rec
	}
	return in.spillPiece(s, rec.Path), rec
}

// spillNote is the success piece spillText would render, built before
// any write from the content-addressed path, for the budget to judge.
func (in mcpIntake) spillNote(server, tool, s string) string {
	return in.spillPiece(s, in.pathFor(server, tool, textExt(s), []byte(s)))
}

func (in mcpIntake) spillPiece(s, path string) string {
	preview, shown := headAndTail(s, in.headRunes, in.tailRunes)
	// The spans are bytes, the unit read_file's offset takes: a rune
	// count subtracted from a byte count gives the wrong offset for any
	// non-ASCII text (ADR-0096 §1).
	// The path is the last thing before the closing bracket on purpose:
	// a path followed by a full stop reads ambiguously, and the model
	// has to hand it to read_file character for character.
	return fmt.Sprintf("%s\n\n[%d bytes — too large to hold inline, so the whole result is saved. Shown above: %s. Read the rest with read_file offset/length, or narrow the call and ask again: read_file %s]",
		preview, len(s), shown, path)
}

// headAndTail returns the first head and last tail runes of s with an
// elision line between them, and the byte spans they cover ("bytes
// 0–600 and 252439–252639"). A string no longer than both is returned
// whole. Both cuts fall on rune boundaries, so neither edge shows a
// broken character.
func headAndTail(s string, head, tail int) (preview, shown string) {
	h := 0
	for i := 0; i < head && h < len(s); i++ {
		_, n := utf8.DecodeRuneInString(s[h:])
		h += n
	}
	t := len(s)
	for i := 0; i < tail && t > h; i++ {
		_, n := utf8.DecodeLastRuneInString(s[:t])
		t -= n
	}
	if t <= h {
		return s, fmt.Sprintf("bytes 0–%d", len(s))
	}
	return s[:h] + fmt.Sprintf("\n[… bytes %d–%d not shown …]\n", h, t) + s[t:],
		fmt.Sprintf("bytes 0–%d and %d–%d", h, t, len(s))
}

// binary writes a non-text block and tells the model how to look at it.
// The bytes never ride back inline: an attachment is replayed with the
// conversation every round (ADR-0027), so it belongs in history only
// when the model deliberately asks for it.
// The record's Path is set only when the bytes actually reached the work
// directory. It is returned rather than inferred, because the only other
// evidence is the note's length and the two notes are not ordered: a
// failure note can be SHORTER than a success note (which carries a full
// path), so a caller reading the budget verdict concluded "saved" for a
// write that failed — and drew a picture with no file behind it, which
// is the one thing ADR-0090 §2 forbids.
func (in mcpIntake) binary(server, tool string, b mcp.Content) (string, tools.Saved) {
	if len(b.Data) == 0 {
		return fmt.Sprintf("[%s content with no data]", b.Type), tools.Saved{}
	}
	rec := in.save(server, tool, extForMIME(b.MIME, b.Type), b.Data)
	if rec.Err != "" {
		return fmt.Sprintf("[%s content (%d bytes, %s) could not be saved: %s]", b.Type, len(b.Data), b.MIME, rec.Err), rec
	}
	return in.binaryNote(server, tool, b, rec.Path), rec
}

// isImageBlock is the ONE predicate. The note calls a block an image, and
// the screen draws one, only where this says so. Two predicates — the
// note's Type and a draw gate on the MIME prefix — could disagree, and a
// picture on screen that the session's record calls a "resource" is exactly
// the mismatch ADR-0090 §2 exists to prevent. What the bytes really are is
// decided later, by termimg.Measure.
func isImageBlock(b mcp.Content) bool { return b.Type == "image" }

// binaryNote is what the model reads for a saved non-text block; the
// same text is sized before the write so the response budget can
// refuse the block without saving it.
func (in mcpIntake) binaryNote(_, _ string, b mcp.Content, path string) string {
	if isImageBlock(b) {
		return fmt.Sprintf("[image saved at %s (%d bytes, %s) — use view_image on that path to look at it]", path, len(b.Data), b.MIME)
	}
	return fmt.Sprintf("[%s content saved at %s (%d bytes, %s)]", b.Type, path, len(b.Data), b.MIME)
}

// pathFor is the content-addressed path a block would be saved at. The
// name carries the whole sha256 (ADR-0105 §1): the path is then known
// exactly before the write — the binary pre-sizing above relies on it —
// and two different results can never be given one name, which with a
// 4-byte prefix the evidence route could not rule out.
func (in mcpIntake) pathFor(server, tool, ext string, data []byte) string {
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("%s-%s-%s%s", sanitizeToolName(server), sanitizeToolName(tool), hex.EncodeToString(sum[:]), ext)
	return filepath.Join(in.workDir(), name)
}

// save writes data to the work directory and reports the result as the
// record the executor keeps. A failure is a record too: the model is
// told there is no file to copy rather than left to assume one.
func (in mcpIntake) save(server, tool, ext string, data []byte) tools.Saved {
	sum := sha256.Sum256(data)
	rec := tools.Saved{Bytes: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
	path, err := in.write(server, tool, ext, data)
	if err != nil {
		rec.Err = err.Error()
		return rec
	}
	rec.Path = path
	return rec
}

// write puts data in the session work directory under a name derived
// from the server, the tool and the content itself. Content addressing
// means the same answer fetched twice occupies one file rather than
// two, and it needs no clock and no counter to stay unique.
//
// A file already at that name is reused only when its own bytes hash to
// the name. MCP servers and write-lane commands can write the work
// directory (ADR-0058 §4), so a name proves nothing about its content;
// a mismatching file is replaced (ADR-0105 §1).
func (in mcpIntake) write(server, tool, ext string, data []byte) (string, error) {
	dir := in.workDir()
	if dir == "" {
		return "", fmt.Errorf("no session work directory to save the result in")
	}
	path := in.pathFor(server, tool, ext, data)
	name := filepath.Base(path)
	if sum, err := fileSHA256(path, int64(len(data))); err == nil && sum == sha256.Sum256(data) {
		return path, nil
	}
	// Temp + rename, so a reader never sees a half-written file and a
	// crash never leaves one that looks complete.
	tmp, err := os.CreateTemp(dir, name+".tmp-")
	if err != nil {
		return "", err
	}
	if _, err := tmp.Write(data); err != nil {
		// Cleanup on a path that has already failed: the write error is
		// what the caller needs, and a failure to tidy up after it does
		// not change the answer.
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	// This Close is checked, unlike the one above: it is the last thing
	// between the data and durability, and a file that closed badly must
	// never be renamed into place looking complete.
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return path, nil
}

// fileSHA256 hashes the file at path as a stream, when it is a regular
// file of the expected size. What sits at a name in the work directory
// may be anything — a FIFO would block the open, a link to /dev/zero
// would hash for ever — so the name is lstat'd first and anything else
// is simply not reused (independent review).
func fileSHA256(path string, size int64) ([sha256.Size]byte, error) {
	var sum [sha256.Size]byte
	st, err := os.Lstat(path)
	if err != nil {
		return sum, err
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return sum, fmt.Errorf("%s is not the saved result", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return sum, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return sum, err
	}
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// extForMIME maps a content type to a file extension. An unknown type
// gets .bin: a wrong extension on a file the model is about to hand to
// view_image would fail confusingly, and .bin fails plainly.
func extForMIME(mime, blockType string) string {
	switch strings.ToLower(strings.TrimSpace(strings.SplitN(mime, ";", 2)[0])) {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "image/heic":
		return ".heic"
	case "application/pdf":
		return ".pdf"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "video/mp4":
		return ".mp4"
	case "application/json":
		return ".json"
	case "text/plain":
		return ".txt"
	}
	if blockType == "image" {
		return ".png"
	}
	return ".bin"
}

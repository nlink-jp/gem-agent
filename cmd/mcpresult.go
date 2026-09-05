package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
type mcpIntake struct {
	// workDir is where oversized and binary blocks land. Empty means
	// there is nowhere to write, and the fallback is a truncation that
	// says so — losing part of an answer silently is the one outcome
	// this must never produce.
	// It is read per call: /clear rotates the directory (ADR-0071 §2).
	workDir func() string
	// cap is the byte size above which a text block is spilled.
	cap int
	// previewRunes bounds the head of a spilled block shown inline.
	previewRunes int
}

func newMCPIntake(workDir func() string) mcpIntake {
	return mcpIntake{workDir: workDir, cap: tools.OutputCap, previewRunes: 800}
}

// render assembles the text the model receives for one call. A result
// the server marked isError renders the same way; the adapter wraps it
// in a tools.RemoteError and the executor says whose words it is
// (ADR-0075 §1) — the intake stops prefixing `error:` itself.
func (in mcpIntake) render(server, tool string, blocks []mcp.Content) string {
	parts := make([]string, 0, len(blocks))
	// One budget for the whole response (ADR-0072 §4.5): many blocks
	// each under the cap used to add up without limit. A block that no
	// longer fits the remaining budget is spilled like an oversized
	// one, so the inline text never exceeds one cap.
	budget := in.cap
	var rest []string // text blocks past the budget, saved together
	leftBinary, savedUnlisted := 0, 0
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
			if len(b.Text) <= budget && spend(b.Text) {
				continue
			}
			if len(b.Text) > in.cap || len(parts) == 0 {
				// Oversized, or the first block that does not fit: the
				// preview path, so the reader sees its head.
				if piece := in.spillText(server, tool, b.Text); spend(piece) {
					continue
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
		piece := in.binary(server, tool, b)
		if !spend(piece) {
			leftBinary++
			savedUnlisted++
		}
	}
	if len(rest) > 0 {
		parts = append(parts, in.spillRest(server, tool, rest))
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
	return out
}

// spillRest saves the text blocks past the response budget as one file
// and tells the model where, with no preview.
func (in mcpIntake) spillRest(server, tool string, texts []string) string {
	joined := strings.Join(texts, "\n")
	path, err := in.write(server, tool, ".txt", []byte(joined))
	if err != nil {
		return fmt.Sprintf("[%d more text block(s), %d bytes, past the response budget and not saved (%v) — narrow the call and ask again]",
			len(texts), len(joined), err)
	}
	return fmt.Sprintf("[%d more text block(s), %d bytes — past the response budget, saved whole. Read them, or narrow the call and ask again: read_file %s]",
		len(texts), len(joined), path)
}

// spillText saves a block to the work directory and returns a preview
// with the path.
func (in mcpIntake) spillText(server, tool, s string) string {
	ext := ".txt"
	if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
		ext = ".json"
	}
	path, err := in.write(server, tool, ext, []byte(s))
	if err != nil {
		return fmt.Sprintf("%s\n\n[%d bytes: only the head above is shown and the rest could not be saved (%v), so it is lost — narrow the call and ask again]",
			clipRunes(s, in.previewRunes), len(s), err)
	}
	// The path is the last thing before the closing bracket on purpose:
	// a path followed by a full stop reads ambiguously, and the model
	// has to hand it to read_file character for character.
	return fmt.Sprintf("%s\n\n[%d bytes — too large to hold inline, so the whole result is saved. Read it, or narrow the call and ask again: read_file %s]",
		clipRunes(s, in.previewRunes), len(s), path)
}

// binary writes a non-text block and tells the model how to look at it.
// The bytes never ride back inline: an attachment is replayed with the
// conversation every round (ADR-0027), so it belongs in history only
// when the model deliberately asks for it.
func (in mcpIntake) binary(server, tool string, b mcp.Content) string {
	if len(b.Data) == 0 {
		return fmt.Sprintf("[%s content with no data]", b.Type)
	}
	path, err := in.write(server, tool, extForMIME(b.MIME, b.Type), b.Data)
	if err != nil {
		return fmt.Sprintf("[%s content (%d bytes, %s) could not be saved: %v]", b.Type, len(b.Data), b.MIME, err)
	}
	return in.binaryNote(server, tool, b, path)
}

// binaryNote is what the model reads for a saved non-text block; the
// same text is sized before the write so the response budget can
// refuse the block without saving it.
func (in mcpIntake) binaryNote(_, _ string, b mcp.Content, path string) string {
	if b.Type == "image" {
		return fmt.Sprintf("[image saved at %s (%d bytes, %s) — use view_image on that path to look at it]", path, len(b.Data), b.MIME)
	}
	return fmt.Sprintf("[%s content saved at %s (%d bytes, %s)]", b.Type, path, len(b.Data), b.MIME)
}

// pathFor is the content-addressed path a block would be saved at.
func (in mcpIntake) pathFor(server, tool, ext string, data []byte) string {
	sum := sha256.Sum256(data)
	name := fmt.Sprintf("%s-%s-%s%s", sanitizeToolName(server), sanitizeToolName(tool), hex.EncodeToString(sum[:4]), ext)
	return filepath.Join(in.workDir(), name)
}

// write puts data in the session work directory under a name derived
// from the server, the tool and the content itself. Content addressing
// means the same answer fetched twice occupies one file rather than
// two, and it needs no clock and no counter to stay unique.
func (in mcpIntake) write(server, tool, ext string, data []byte) (string, error) {
	dir := in.workDir()
	if dir == "" {
		return "", fmt.Errorf("no session work directory to save the result in")
	}
	path := in.pathFor(server, tool, ext, data)
	name := filepath.Base(path)
	if _, err := os.Stat(path); err == nil {
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

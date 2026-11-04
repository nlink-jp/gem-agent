package agent

import (
	"fmt"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// savedFiles converts the intake's records to the transcript's shape.
func savedFiles(in []tools.Saved) []llm.SavedFile {
	if len(in) == 0 {
		return nil
	}
	out := make([]llm.SavedFile, len(in))
	for i, s := range in {
		out[i] = llm.SavedFile{Path: s.Path, Bytes: s.Bytes, SHA256: s.SHA256, Error: s.Err}
	}
	return out
}

// savedNote is gem-agent's own line about what it saved for a result
// (ADR-0105 §1, §3). It rides outside the nonce tag, like ADR-0075's
// runtime note, because it is the runtime's word: a server can print a
// line shaped like it inside the result, and that line stays wrapped.
// It offers the route — a copy by write_file `from` — at the moment the
// result arrives, and says so plainly when there is nothing to copy.
func savedNote(files []llm.SavedFile) string {
	var lines []string
	for _, f := range files {
		if f.Path == "" {
			lines = append(lines, fmt.Sprintf(
				"[gem-agent could not save this result (%s), so there is no file to copy]", f.Error))
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"[gem-agent saved this result whole: %s (%d bytes, sha256 %s) — to keep it as evidence, write_file with from set to that path copies it byte for byte]",
			f.Path, f.Bytes, f.SHA256))
	}
	return strings.Join(lines, "\n")
}

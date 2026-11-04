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

// runtimeSuffix is what the runtime appends to a tool result outside
// the nonce tag at send time, in this order: ADR-0075 §3's runtime note
// (a remote tool's repeated identical failure) and the note of what was
// saved (ADR-0105 §1, §3). Provenance is the field; a result whose text
// merely looks like either note stays wrapped. The wrap and the token
// estimate share it, so the bytes the model receives are the bytes
// counted.
func runtimeSuffix(m llm.Message) string {
	var b strings.Builder
	if m.RuntimeNote != "" {
		b.WriteString("\n\n" + m.RuntimeNote)
	}
	if note := savedNote(m.Saved); note != "" {
		b.WriteString("\n\n" + note)
	}
	return b.String()
}

// savedNote is gem-agent's own line about what it saved for a result
// (ADR-0105 §1, §3). It rides outside the nonce tag, like ADR-0075's
// runtime note, because it is the runtime's word: a server can print a
// line shaped like it inside the result, and that line stays wrapped.
// It offers the route — a copy by write_file `from` — at the moment the
// result arrives, and says so plainly when there is nothing to copy.
//
// A result split across files (the inline blocks, a spilled block, the
// rest past the budget, an image) is never called whole: each line names
// its part, and copying the whole result means copying every part
// (lagent's independent review of the port — a part announced as "whole"
// is the lost rows ADR-0105 exists to stop).
func savedNote(files []llm.SavedFile) string {
	var lines []string
	n := len(files)
	for i, f := range files {
		what := "this result"
		if n > 1 {
			what = fmt.Sprintf("part %d of %d of this result", i+1, n)
		}
		if f.Path == "" {
			lines = append(lines, fmt.Sprintf(
				"[gem-agent could not save %s (%s), so there is no file to copy]", what, f.Error))
			continue
		}
		if n > 1 {
			lines = append(lines, fmt.Sprintf(
				"[gem-agent saved %s: %s (%d bytes, sha256 %s) — to keep the whole result as evidence, copy every part, each with write_file from set to its path]",
				what, f.Path, f.Bytes, f.SHA256))
			continue
		}
		lines = append(lines, fmt.Sprintf(
			"[gem-agent saved this result whole: %s (%d bytes, sha256 %s) — to keep it as evidence, write_file with from set to that path copies it byte for byte]",
			f.Path, f.Bytes, f.SHA256))
	}
	return strings.Join(lines, "\n")
}

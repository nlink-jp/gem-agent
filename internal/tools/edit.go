package tools

// edit_file v2 (ADR-0015): batched, atomic, diagnosed, self-verifying
// exact-string replacement. The anchor stays a unique literal string —
// line-number addressing writes to the wrong place *silently* when the
// number is stale, while a string anchor fails loudly or works.

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// maxEditBytes bounds the file edit_file loads whole: 8 MiB covers
// every source file and refuses the sparse or generated giant that
// would exhaust memory.
const maxEditBytes = 8 << 20

const (
	// editSnippetContext is how many lines around a change the success
	// report includes — the evidence that replaces a read-back round.
	editSnippetContext = 2
	// editSnippetCap bounds one report snippet.
	editSnippetCap = 2000
	// nearMissCap bounds the quoted near-miss text in a diagnosis.
	nearMissCap = 600
	// maxOccurrencesListed bounds the "appears N times" line list.
	maxOccurrencesListed = 8
)

// editOp is one replacement.
type editOp struct {
	oldStr, newStr string
	replaceAll     bool
}

func (r *Registry) editFile() *Tool {
	return &Tool{
		Name: "edit_file",
		Description: "Replace exact strings in a project file. Either one old_string/new_string pair, " +
			"or edits: an array of {old_string, new_string, replace_all?} applied in order and " +
			"atomically — each edit sees the previous edits' output, and if any edit fails nothing " +
			"is written. old_string must match exactly once (include surrounding lines to make it " +
			"unique; the error lists occurrence lines otherwise) unless replace_all is true. " +
			"A failed match reports the closest whitespace-insensitive near-match with its real " +
			"text, and success returns the changed region — verify from that, no re-read needed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the project root.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "Exact text to replace (single-edit form).",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "Replacement text (single-edit form).",
				},
				"replace_all": map[string]any{
					"type":        "boolean",
					"description": "Replace every occurrence instead of requiring uniqueness.",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "Batch form: applied in order, atomically.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_string":  map[string]any{"type": "string"},
							"new_string":  map[string]any{"type": "string"},
							"replace_all": map[string]any{"type": "boolean"},
						},
						"required": []string{"old_string", "new_string"},
					},
				},
			},
			"required": []string{"path"},
		},
		Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, ok := strArg(args, "path")
			if !ok {
				return "", errors.New("path is required")
			}
			ops, err := parseEditOps(args)
			if err != nil {
				return "", err
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			// Every syscall-shaped step consults ctx (ADR-0065 §1): a
			// call the floor abandoned during a slow read must not go
			// on to write after the operator was told "interrupted"
			// (review after v0.68.0).
			if err := ctx.Err(); err != nil {
				return "", err
			}
			info, err := r.statIn(abs)
			if err != nil {
				return "", err
			}
			// An edit needs the whole file in memory; a file past the
			// cap (or a sparse one) is refused by its size before any
			// read, and the read itself is bounded (review after
			// v0.68.2 — io.ReadAll had no limit).
			if info.Size() > maxEditBytes {
				return "", fmt.Errorf("%s is %d bytes; edit_file handles files up to %d bytes — use a shell command for larger ones", p, info.Size(), maxEditBytes)
			}
			in, err := r.openRead(abs)
			if err != nil {
				return "", err
			}
			data, more, err := readAllCapped(in, maxEditBytes)
			_ = in.Close()
			if err != nil {
				return "", err
			}
			if more {
				return "", fmt.Errorf("%s grew past %d bytes while being read — use a shell command for it", p, maxEditBytes)
			}

			content, reports, err := applyEdits(string(data), ops)
			if err != nil {
				// Atomic: nothing was written; the error already names
				// the failing edit and carries the diagnosis.
				return "", fmt.Errorf("%s: %w (file unchanged)", p, err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("%w (file unchanged)", err)
			}
			if err := r.replaceFile(abs, info.Mode().Perm(), []byte(content)); err != nil {
				return "", err
			}
			return fmt.Sprintf("edited %s (%d edit(s)):\n%s", p, len(ops), strings.Join(reports, "\n")), nil
		},
	}
}

// parseEditOps accepts the single-pair form or the edits array, not both.
func parseEditOps(args map[string]any) ([]editOp, error) {
	single, hasOld := strArg(args, "old_string")
	newStr, hasNew := strArg(args, "new_string")
	rawEdits, hasEdits := args["edits"].([]any)

	if hasEdits && (hasOld || hasNew) {
		return nil, errors.New("pass either old_string/new_string or edits, not both")
	}
	if hasEdits {
		if len(rawEdits) == 0 {
			return nil, errors.New("edits is empty")
		}
		ops := make([]editOp, 0, len(rawEdits))
		for i, raw := range rawEdits {
			m, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("edit %d is not an object", i+1)
			}
			op := editOp{}
			op.oldStr, _ = m["old_string"].(string)
			op.newStr, _ = m["new_string"].(string)
			op.replaceAll, _ = m["replace_all"].(bool)
			if err := validateOp(op, i+1); err != nil {
				return nil, err
			}
			ops = append(ops, op)
		}
		return ops, nil
	}
	if !hasOld || single == "" {
		return nil, errors.New("old_string is required (or pass edits)")
	}
	if !hasNew {
		return nil, errors.New("new_string is required")
	}
	replaceAll, _ := args["replace_all"].(bool)
	op := editOp{oldStr: single, newStr: newStr, replaceAll: replaceAll}
	if err := validateOp(op, 1); err != nil {
		return nil, err
	}
	return []editOp{op}, nil
}

func validateOp(op editOp, n int) error {
	if op.oldStr == "" {
		return fmt.Errorf("edit %d: old_string is empty", n)
	}
	if op.oldStr == op.newStr {
		return fmt.Errorf("edit %d: old_string and new_string are identical — no change", n)
	}
	return nil
}

// applyEdits applies ops in order to an in-memory copy and returns the
// result with one report line per edit. Any failure returns the original
// content untouched (the caller writes nothing) and an error that names
// the edit and carries the diagnosis.
func applyEdits(content string, ops []editOp) (string, []string, error) {
	reports := make([]string, 0, len(ops))
	out := content
	for i, op := range ops {
		n := strings.Count(out, op.oldStr)
		switch {
		case n == 0:
			return content, nil, fmt.Errorf("edit %d: old_string not found. %s", i+1, diagnoseMiss(out, op.oldStr, i))
		case n > 1 && !op.replaceAll:
			return content, nil, fmt.Errorf("edit %d: old_string appears %d times (lines %s) — add surrounding lines to make it unique, or set replace_all",
				i+1, n, occurrenceLines(out, op.oldStr))
		}
		if op.replaceAll {
			out = strings.ReplaceAll(out, op.oldStr, op.newStr)
			reports = append(reports, fmt.Sprintf("- edit %d: replaced %d occurrence(s)", i+1, n))
			continue
		}
		offset := strings.Index(out, op.oldStr)
		out = out[:offset] + op.newStr + out[offset+len(op.oldStr):]
		reports = append(reports, editReport(i+1, out, offset, len(op.newStr)))
	}
	return out, reports, nil
}

// diagnoseMiss explains a failed match. Models miss on whitespace —
// indentation drift, tabs vs spaces — far more often than on words, so
// the file is re-searched with whitespace normalized and the closest
// candidate is quoted with its REAL text: the fix becomes a copy-paste
// instead of a re-read. Heuristic by design: it only ever shapes the
// error message, never what gets written.
func diagnoseMiss(content, needle string, editIdx int) string {
	if editIdx > 0 {
		// In a batch, the classic cause is an earlier edit having
		// already changed this text.
		if line, snippet, ok := nearMiss(content, needle); ok {
			return fmt.Sprintf("A close match (whitespace differs) starts at line %d:\n%s\nNote: earlier edits in this batch have already been applied — old_string must match the file as those edits left it.", line, snippet)
		}
		return "Note: earlier edits in this batch have already been applied — old_string must match the file as those edits left it. No similar text was found."
	}
	if line, snippet, ok := nearMiss(content, needle); ok {
		return fmt.Sprintf("A close match (whitespace differs) starts at line %d — use this exact text:\n%s", line, snippet)
	}
	return "No similar text found either — read the relevant lines first (read_file with start_line/end_line)."
}

// nearMiss searches for needle with whitespace normalized per line.
func nearMiss(content, needle string) (line int, snippet string, ok bool) {
	needleLines := normalizeLines(needle)
	if len(needleLines) == 0 {
		return 0, "", false
	}
	// The content's normalized lines must stay 1:1 with the raw lines:
	// normalizeLines drops leading blank lines, and on a file starting
	// with blanks that shifted every reported line number and made the
	// quoted "use this exact text" snippet come from the wrong region —
	// real file text, so the follow-up edit landed there (ADR-0021).
	contentRaw := strings.Split(content, "\n")
	contentNorm := make([]string, len(contentRaw))
	for i, l := range contentRaw {
		contentNorm[i] = strings.Join(strings.Fields(l), " ")
	}
	for i := 0; i+len(needleLines) <= len(contentNorm); i++ {
		match := true
		for j := range needleLines {
			if contentNorm[i+j] != needleLines[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		end := min(i+len(needleLines), len(contentRaw))
		quoted := strings.Join(contentRaw[i:end], "\n")
		if len(quoted) > nearMissCap {
			quoted = cutRunes(quoted, nearMissCap) + "…"
		}
		return i + 1, quoted, true
	}
	return 0, "", false
}

// normalizeLines trims each line and collapses internal whitespace runs.
func normalizeLines(s string) []string {
	lines := strings.Split(s, "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.Join(strings.Fields(l), " ")
	}
	// Drop leading/trailing all-blank lines so a stray newline in the
	// needle does not defeat the search.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// occurrenceLines lists the 1-based lines where needle occurs.
func occurrenceLines(content, needle string) string {
	var lines []string
	offset := 0
	for len(lines) < maxOccurrencesListed {
		i := strings.Index(content[offset:], needle)
		if i < 0 {
			break
		}
		lines = append(lines, fmt.Sprint(1+strings.Count(content[:offset+i], "\n")))
		offset += i + len(needle)
	}
	out := strings.Join(lines, ", ")
	if len(lines) == maxOccurrencesListed {
		out += ", …"
	}
	return out
}

// editReport renders the changed region with context — the evidence
// that replaces a read-back verification round. The line span lives in
// the header, never as per-line prefixes on content (ADR-0014: numbered
// content poisons the exact-match contract the moment it is copied).
func editReport(n int, content string, offset, newLen int) string {
	lines := strings.Split(content, "\n")
	startLine := strings.Count(content[:offset], "\n") // 0-based
	end := offset + newLen
	// A replacement ending in '\n' terminates its own last line; that
	// newline must not count as reaching the next line (ADR-0021).
	if newLen > 0 && content[end-1] == '\n' {
		end--
	}
	endLine := strings.Count(content[:end], "\n") // 0-based, inclusive
	from := max(0, startLine-editSnippetContext)
	to := min(len(lines), endLine+editSnippetContext+1)
	snippet := strings.Join(lines[from:to], "\n")
	if len(snippet) > editSnippetCap {
		snippet = cutRunes(snippet, editSnippetCap) + "…"
	}
	return fmt.Sprintf("- edit %d: lines %d–%d now read:\n%s", n, startLine+1, endLine+1, snippet)
}

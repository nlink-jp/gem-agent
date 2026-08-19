package tools

// file_info (ADR-0016): the `file` command's judgement plus metadata and
// the MD5/SHA1/SHA256 trio the org's malware-lookup MCP consumes — the
// IR opening moves (identify, date, hash, look up) in one read-only call.

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	// hashSizeCap skips hashing beyond this rather than stalling a turn.
	hashSizeCap = 512 * 1024 * 1024
	// fileInfoBatchCap bounds the paths form.
	fileInfoBatchCap = 20
	// typeSniffBytes is how much of the head the type judgement reads.
	typeSniffBytes = 8192
)

func (r *Registry) fileInfo() *Tool {
	return &Tool{
		Name: "file_info",
		Description: "Report what a file IS without reading it into context: content-judged type " +
			"(the `file` command's job — executables, archives, scripts, text/binary; the extension " +
			"is shown but never trusted), size, mode, modified and created times, and MD5/SHA1/SHA256 " +
			"hashes — the trio hash-lookup tools take. Pass paths (array) for a batch. Symlinks are " +
			"reported with their target, not silently followed; directories get entry counts, no hashes.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "file path relative to the project root"},
				"paths": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "batch form (max 20)"},
			},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			paths, err := fileInfoPaths(args)
			if err != nil {
				return "", err
			}
			var out []string
			for _, p := range paths {
				info, err := r.describeFile(p)
				if err != nil {
					// In a batch, one bad path must not hide the others.
					info = fmt.Sprintf("%s: error: %v", p, err)
				}
				out = append(out, info)
			}
			return truncate(strings.Join(out, "\n\n"), outputCap), nil
		},
	}
}

func fileInfoPaths(args map[string]any) ([]string, error) {
	single, hasSingle := strArg(args, "path")
	raw, hasBatch := args["paths"].([]any)
	switch {
	case hasSingle && hasBatch:
		return nil, errors.New("pass either path or paths, not both")
	case hasSingle:
		return []string{single}, nil
	case hasBatch:
		if len(raw) == 0 {
			return nil, errors.New("paths is empty")
		}
		if len(raw) > fileInfoBatchCap {
			return nil, fmt.Errorf("at most %d paths per call", fileInfoBatchCap)
		}
		paths := make([]string, 0, len(raw))
		for _, v := range raw {
			s, ok := v.(string)
			if !ok || s == "" {
				return nil, errors.New("paths must be non-empty strings")
			}
			paths = append(paths, s)
		}
		return paths, nil
	default:
		return nil, errors.New("path (or paths) is required")
	}
}

// describeFile renders one file's report.
func (r *Registry) describeFile(p string) (string, error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		// For file_info, an in-project symlink that ESCAPES is a
		// reportable fact, not a dead end — resolvePath refuses it (as
		// it must for every reading tool), so recognise exactly that
		// case here: the lexical path is inside the project and the
		// entry itself is a link. Nothing about the target is inspected.
		lex := filepath.Clean(p)
		if !filepath.IsAbs(lex) {
			lex = filepath.Clean(filepath.Join(r.projectDir, p))
		}
		if within(r.projectDir, lex) {
			if lst, lerr := os.Lstat(lex); lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
				target, _ := os.Readlink(lex)
				return fmt.Sprintf("%s:\n  symlink → %s\n  target: outside the project — not inspected", p, target), nil
			}
		}
		return "", err
	}
	lst, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("not found")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s:", p)

	if lst.Mode()&os.ModeSymlink != 0 {
		target, _ := os.Readlink(abs)
		fmt.Fprintf(&b, "\n  symlink → %s", target)
		real, err := filepath.EvalSymlinks(abs)
		if err != nil || !within(r.projectDir, real) {
			// Reported, never silently followed (ADR-0016 §4).
			b.WriteString("\n  target: outside the project — not inspected")
			return b.String(), nil
		}
		b.WriteString("\n  (target is inside the project; details below are the target's)")
		abs = real
		if lst, err = os.Stat(abs); err != nil {
			return "", err
		}
	}

	if lst.IsDir() {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return "", err
		}
		files, dirs, total := 0, 0, int64(0)
		for _, e := range entries {
			if e.IsDir() {
				dirs++
				continue
			}
			files++
			if fi, err := e.Info(); err == nil {
				total += fi.Size()
			}
		}
		fmt.Fprintf(&b, "\n  type: directory\n  entries: %d files, %d dirs (shallow size %d bytes)", files, dirs, total)
		appendTimes(&b, lst)
		return b.String(), nil
	}

	fmt.Fprintf(&b, "\n  size: %d bytes\n  mode: %s", lst.Size(), lst.Mode().Perm())
	if lst.Mode().Perm()&0o111 != 0 {
		b.WriteString(" (executable)")
	}
	appendTimes(&b, lst)

	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("unreadable")
	}
	defer f.Close()

	head := make([]byte, typeSniffBytes)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	kind, isText := detectType(head, p)
	fmt.Fprintf(&b, "\n  type: %s", kind)
	if ext := filepath.Ext(p); ext != "" && !strings.Contains(kind, strings.TrimPrefix(ext, ".")) {
		fmt.Fprintf(&b, " (extension says %s — judged from content)", ext)
	}

	if lst.Size() > hashSizeCap {
		fmt.Fprintf(&b, "\n  hashes: skipped (file exceeds %d bytes)", hashSizeCap)
		return b.String(), nil
	}
	md5h, sha1h, sha256h := md5.New(), sha1.New(), sha256.New()
	lineCounter := &newlineCounter{}
	w := io.MultiWriter(md5h, sha1h, sha256h, lineCounter)
	if _, err := w.Write(head); err != nil {
		return "", err
	}
	if _, err := io.Copy(w, f); err != nil {
		return "", fmt.Errorf("hashing failed: %v", err)
	}
	if isText {
		lines := lineCounter.newlines
		if lst.Size() > 0 && !lineCounter.endsWithNewline {
			lines++
		}
		fmt.Fprintf(&b, "\n  lines: %d", lines)
	}
	fmt.Fprintf(&b, "\n  md5:    %s\n  sha1:   %s\n  sha256: %s",
		hex.EncodeToString(md5h.Sum(nil)),
		hex.EncodeToString(sha1h.Sum(nil)),
		hex.EncodeToString(sha256h.Sum(nil)))
	return b.String(), nil
}

// appendTimes adds modified and — macOS-only by design, so the Darwin
// field costs no promised portability — birth time.
func appendTimes(b *strings.Builder, fi os.FileInfo) {
	fmt.Fprintf(b, "\n  modified: %s", fi.ModTime().Format(time.RFC3339))
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		birth := time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
		fmt.Fprintf(b, "\n  created:  %s", birth.Format(time.RFC3339))
	}
}

// magic is one content signature. Offset 0 unless noted.
type magic struct {
	prefix []byte
	kind   string
}

// magics covers what IR work actually meets. Finite and test-first by
// design — extending it is an edit, not a libmagic dependency.
var magics = []magic{
	{[]byte{0xcf, 0xfa, 0xed, 0xfe}, "Mach-O 64-bit executable"},
	{[]byte{0xce, 0xfa, 0xed, 0xfe}, "Mach-O 32-bit executable"},
	{[]byte{0xfe, 0xed, 0xfa, 0xcf}, "Mach-O 64-bit executable (big-endian)"},
	{[]byte{0xfe, 0xed, 0xfa, 0xce}, "Mach-O 32-bit executable (big-endian)"},
	{[]byte{0xca, 0xfe, 0xba, 0xbe}, "Mach-O universal (fat) binary"},
	{[]byte{0x7f, 'E', 'L', 'F'}, "ELF executable"},
	{[]byte("MZ"), "PE/DOS executable (Windows)"},
	{[]byte("PK\x03\x04"), "zip archive (also jar/docx/xlsx family)"},
	{[]byte{0x1f, 0x8b}, "gzip compressed data"},
	{[]byte("%PDF"), "PDF document"},
	{[]byte("SQLite format 3\x00"), "SQLite database"},
}

// detectType judges a file from its head bytes — never the extension.
func detectType(head []byte, name string) (kind string, isText bool) {
	if len(head) == 0 {
		return "empty file", false
	}
	for _, m := range magics {
		if bytes.HasPrefix(head, m.prefix) {
			return m.kind, false
		}
	}
	if bytes.HasPrefix(head, []byte("#!")) {
		line := head
		if i := bytes.IndexByte(line, '\n'); i > 0 {
			line = line[:i]
		}
		return fmt.Sprintf("script (%s)", strings.TrimSpace(string(line))), true
	}
	if bytes.IndexByte(head, 0) >= 0 {
		// Binary but unrecognised: fall back to the stdlib sniffer for
		// media types, else be honest.
		if mime := http.DetectContentType(head); mime != "application/octet-stream" {
			return mime, false
		}
		return "data (binary, unrecognised)", false
	}
	if utf8.Valid(head) {
		return "text (UTF-8)", true
	}
	return "text (non-UTF-8 encoding)", true
}

// newlineCounter counts lines during the hash pass — one read serves
// both jobs.
type newlineCounter struct {
	newlines        int
	endsWithNewline bool
}

func (c *newlineCounter) Write(p []byte) (int, error) {
	c.newlines += bytes.Count(p, []byte{'\n'})
	if len(p) > 0 {
		c.endsWithNewline = p[len(p)-1] == '\n'
	}
	return len(p), nil
}

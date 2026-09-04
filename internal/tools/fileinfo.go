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
			return truncate(strings.Join(out, "\n\n"), OutputCap), nil
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
		if withinAny(r.roots(), lex) {
			// The PARENT must genuinely resolve inside the project:
			// Lstat follows intermediate symlinks, so a lexically
			// in-project path under a model-planted escaping link would
			// report link targets of files wholly outside (ADR-0021).
			parent, perr := filepath.EvalSymlinks(filepath.Dir(lex))
			if perr == nil && withinAny(r.roots(), parent) {
				entry := filepath.Join(parent, filepath.Base(lex))
				if lst, lerr := r.lstatIn(entry); lerr == nil && lst.Mode()&os.ModeSymlink != 0 {
					target, _ := r.readlinkIn(entry)
					return fmt.Sprintf("%s:\n  symlink → %s\n  target: outside the project — not inspected", p, target), nil
				}
			}
		}
		return "", err
	}
	lst, err := r.lstatIn(abs)
	if err != nil {
		return "", fmt.Errorf("not found")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s:", p)

	if lst.Mode()&os.ModeSymlink != 0 {
		target, _ := r.readlinkIn(abs)
		fmt.Fprintf(&b, "\n  symlink → %s", target)
		real, err := filepath.EvalSymlinks(abs)
		if err != nil || !withinAny(r.roots(), real) {
			// Reported, never silently followed (ADR-0016 §4).
			b.WriteString("\n  target: outside the project — not inspected")
			return b.String(), nil
		}
		b.WriteString("\n  (target is inside the project; details below are the target's)")
		abs = real
		if lst, err = r.statIn(abs); err != nil {
			return "", err
		}
	}

	if lst.IsDir() {
		entries, err := r.readDirIn(abs)
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

	f, err := r.openRead(abs)
	if err != nil {
		return "", fmt.Errorf("unreadable")
	}
	defer func() { _ = f.Close() }()

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

// magic is one content signature: a prefix at an offset, optionally
// refined by a closer look at the head bytes (format families, and the
// famous 0xCAFEBABE collision between Java class files and fat Mach-O
// — file(1) distinguishes them by the architecture count, and so do we).
type magic struct {
	offset int
	prefix []byte
	kind   string
	refine func(head []byte) string
	// valid, when set, must confirm the match — short ASCII magics
	// ("BM", "ID3", "BZh") collide with ordinary text beginnings, and a
	// text file misread as an image is exactly the mistake a type
	// judgement exists to prevent.
	valid func(head []byte) bool
}

// magics covers the majors plus what IR work actually meets. Finite and
// test-first by design — extending it is an edit, not a libmagic
// dependency. Order matters where prefixes overlap: first match wins.
var magics = []magic{
	// Executables and libraries.
	{0, []byte{0xcf, 0xfa, 0xed, 0xfe}, "Mach-O 64-bit executable", refineMachO, nil},
	{0, []byte{0xce, 0xfa, 0xed, 0xfe}, "Mach-O 32-bit executable", nil, nil},
	{0, []byte{0xfe, 0xed, 0xfa, 0xcf}, "Mach-O 64-bit executable (big-endian)", nil, nil},
	{0, []byte{0xfe, 0xed, 0xfa, 0xce}, "Mach-O 32-bit executable (big-endian)", nil, nil},
	{0, []byte{0xca, 0xfe, 0xba, 0xbe}, "Mach-O universal (fat) binary", refineCafebabe, nil},
	{0, []byte{0x7f, 'E', 'L', 'F'}, "ELF executable", nil, nil},
	{0, []byte("MZ"), "PE/DOS executable (Windows)", nil, nil},
	{0, []byte("\x00asm"), "WebAssembly binary", nil, nil},
	// Images.
	{0, []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, "PNG image", nil, nil},
	{0, []byte{0xff, 0xd8, 0xff}, "JPEG image", nil, nil},
	{0, []byte("GIF87a"), "GIF image", nil, nil},
	{0, []byte("GIF89a"), "GIF image", nil, nil},
	{0, []byte("II*\x00"), "TIFF image (little-endian)", nil, nil},
	{0, []byte("MM\x00*"), "TIFF image (big-endian)", nil, nil},
	{0, []byte("BM"), "BMP image", nil, func(h []byte) bool {
		// Reserved words at 6–9 are zero in every real BMP.
		return len(h) >= 10 && h[6] == 0 && h[7] == 0 && h[8] == 0 && h[9] == 0
	}},
	// Container formats branded at offset 4 (MP4/MOV/HEIC family) and
	// RIFF at 0 (WebP/WAV/AVI).
	{4, []byte("ftyp"), "ISO media (MP4 family)", refineFtyp, nil},
	{0, []byte("RIFF"), "RIFF container", refineRIFF, nil},
	// Audio.
	{0, []byte("ID3"), "MP3 audio (ID3 tagged)", nil, func(h []byte) bool {
		return len(h) >= 7 && h[3] <= 10 && h[6]&0x80 == 0 // sane version + syncsafe size
	}},
	{0, []byte("OggS"), "Ogg container", nil, func(h []byte) bool { return len(h) >= 5 && h[4] == 0 }},
	{0, []byte("fLaC"), "FLAC audio", nil, nil},
	// Archives and compression.
	{0, []byte("PK\x03\x04"), "zip archive (also jar/docx/xlsx family)", nil, nil},
	{0, []byte{0x1f, 0x8b}, "gzip compressed data", nil, nil},
	{257, []byte("ustar"), "tar archive", nil, nil},
	{0, []byte("7z\xbc\xaf\x27\x1c"), "7-zip archive", nil, nil},
	{0, []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "xz compressed data", nil, nil},
	{0, []byte("BZh"), "bzip2 compressed data", nil, func(h []byte) bool {
		return len(h) >= 4 && h[3] >= '1' && h[3] <= '9' // block-size digit
	}},
	{0, []byte{0x28, 0xb5, 0x2f, 0xfd}, "zstd compressed data", nil, nil},
	{0, []byte("Rar!\x1a\x07"), "RAR archive", nil, nil},
	// Documents and data.
	{0, []byte("%PDF"), "PDF document", nil, nil},
	{0, []byte("SQLite format 3\x00"), "SQLite database", nil, nil},
	{0, []byte("bplist00"), "binary property list (plist)", nil, nil},
	{0, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, "OLE compound document (legacy Office / msi)", nil, nil},
}

// refineCafebabe settles 0xCAFEBABE: a fat Mach-O header follows the
// magic with the architecture count — a small number — while a Java
// class file follows it with minor/major version, which reads as a much
// larger big-endian value. file(1) draws the same line.
func refineCafebabe(head []byte) string {
	if len(head) < 8 {
		return ""
	}
	v := uint32(head[4])<<24 | uint32(head[5])<<16 | uint32(head[6])<<8 | uint32(head[7])
	if v >= 0x14 { // 20 — no real fat binary carries this many architectures
		return "Java class file"
	}
	return ""
}

// refineMachO names the common non-executable filetypes (little-endian
// 64-bit header: filetype at offset 12).
func refineMachO(head []byte) string {
	if len(head) < 16 {
		return ""
	}
	v := uint32(head[12]) | uint32(head[13])<<8 | uint32(head[14])<<16 | uint32(head[15])<<24
	switch v {
	case 1:
		return "Mach-O 64-bit object file"
	case 6:
		return "Mach-O 64-bit dynamic library"
	case 8:
		return "Mach-O 64-bit bundle"
	case 10:
		return "Mach-O 64-bit dSYM companion"
	}
	return ""
}

func refineFtyp(head []byte) string {
	if len(head) < 12 {
		return ""
	}
	switch brand := string(head[8:12]); {
	case strings.HasPrefix(brand, "hei"), strings.HasPrefix(brand, "hev"), brand == "mif1":
		return "HEIC/HEIF image"
	case brand == "avif":
		return "AVIF image"
	case brand == "qt  ":
		return "QuickTime movie (MOV)"
	case brand == "M4A ":
		return "M4A audio"
	}
	return "MP4 media"
}

func refineRIFF(head []byte) string {
	if len(head) < 12 {
		return ""
	}
	switch string(head[8:12]) {
	case "WEBP":
		return "WebP image"
	case "WAVE":
		return "WAV audio"
	case "AVI ":
		return "AVI video"
	}
	return ""
}

// detectType judges a file from its head bytes — never the extension.
func detectType(head []byte, name string) (kind string, isText bool) {
	if len(head) == 0 {
		return "empty file", false
	}
	for _, m := range magics {
		if len(head) < m.offset+len(m.prefix) ||
			!bytes.Equal(head[m.offset:m.offset+len(m.prefix)], m.prefix) {
			continue
		}
		if m.valid != nil && !m.valid(head) {
			continue
		}
		if m.refine != nil {
			if refined := m.refine(head); refined != "" {
				return refined, false
			}
		}
		return m.kind, false
	}
	if bytes.HasPrefix(head, []byte("#!")) {
		line := head
		if i := bytes.IndexByte(line, '\n'); i > 0 {
			line = line[:i]
		}
		return fmt.Sprintf("script (%s)", strings.TrimSpace(string(line))), true
	}
	if bytes.IndexByte(head, 0) >= 0 {
		// Binary but unrecognised: fall back to the stdlib sniffer, else
		// be honest.
		if mime := http.DetectContentType(head); mime != "application/octet-stream" {
			return mime, false
		}
		return "data (binary, unrecognised)", false
	}
	if utf8.Valid(head) {
		if bytes.HasPrefix(head, []byte("-----BEGIN ")) {
			return "PEM encoded data (text)", true
		}
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

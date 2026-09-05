// Package mention resolves @-references in user input to project files
// and directories, so "@src/main.go これ直して" carries the file with it.
//
// Resolution is confined to the project directory (symlinks included):
// an @-reference is a convenience, not a way around the containment the
// file tools enforce.
package mention

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/docext"
)

// Limits bound how much an expansion may add to the prompt.
type Limits struct {
	PerFileBytes int
	TotalBytes   int
	DirEntries   int
	// Image budgets are separate from the text budget (ADR-0012): one
	// screenshot must not evict the source files attached beside it.
	ImageBytes int // per image
	MaxImages  int // per message
	// Clipboard captures the clipboard image as PNG bytes — the
	// @clipboard route. nil reports the reference as unavailable.
	Clipboard func() ([]byte, error)
	// DocumentBytes caps one attached PDF (ADR-0026); extracted Office
	// text rides the ordinary per-file budget.
	DocumentBytes int
	// MediaBytes caps one INLINE audio/video attachment (ADR-0027).
	MediaBytes int
	// UploadMedia, when set, stores a media file in the operator's GCS
	// bucket and returns its gs:// URI. A configured bucket always
	// wins over inline: the history replays every round, and inline
	// media bytes would be re-sent with each one. nil = no bucket.
	// The ctx is the TURN's context: a multi-minute upload the
	// operator's Ctrl+C could not reach looked like a hang (review
	// round 2).
	// It receives the file mention opened through its confinement root
	// and the reference's name (for the extension), never a path to
	// reopen: a path re-resolved after the check was a swap away from
	// reading outside (review after v0.68.2, R05).
	UploadMedia func(ctx context.Context, f *os.File, name, mime string) (string, error)
}

// DefaultLimits are sized so a handful of source files fit comfortably
// while a stray @ on a huge tree cannot blow up the context.
func DefaultLimits() Limits {
	return Limits{
		PerFileBytes: 64 * 1024, TotalBytes: 256 * 1024, DirEntries: 200,
		ImageBytes: 8 * 1024 * 1024, MaxImages: 4,
		DocumentBytes: 12 * 1024 * 1024,
		MediaBytes:    15 * 1024 * 1024,
	}
}

// Attachment is one resolved reference.
type Attachment struct {
	Ref     string // as typed, without the @
	Kind    string // "file", "directory" or "image"
	Content string
	Bytes   int
	// Data and MIME are set for images (ADR-0012), inline documents
	// (ADR-0026), and inline media (ADR-0027).
	Data []byte
	MIME string
	// URI is set instead of Data for bucket-routed media (ADR-0027):
	// a gs:// object Vertex reads natively.
	URI string
}

// ClipboardRef is the pseudo-reference that attaches the clipboard
// image — the fastest screenshot route on macOS (Cmd+Ctrl+Shift+4,
// then "@clipboard ここがおかしい").
const ClipboardRef = "clipboard"

// imageMIME maps recognised image extensions. Recognition gates the
// out-of-project exception: only files that look like images by name
// AND sniff as images by content may come from outside the project.
var imageMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".webp": "image/webp", ".gif": "image/gif",
	".heic": "image/heic", ".heif": "image/heif",
}

// IsImagePath reports whether a reference names an image by extension.
func IsImagePath(ref string) bool {
	_, ok := imageMIME[strings.ToLower(filepath.Ext(ref))]
	return ok
}

// documentExts gates the document route (ADR-0026). Like images, the
// extension only routes; the bytes decide the claim.
var documentExts = map[string]bool{
	".pdf": true, ".docx": true, ".xlsx": true, ".pptx": true,
}

// IsDocumentPath reports whether a reference names a document by
// extension.
func IsDocumentPath(ref string) bool {
	return documentExts[strings.ToLower(filepath.Ext(ref))]
}

// mediaMIME maps the common audio/video containers (ADR-0027). MIME is
// extension-declared: sniffing media containers properly means a
// parser per format, and a wrong MIME fails loudly at the API — the
// same failure one step later. The obvious mistake (plain text under a
// media extension) is screened here.
var mediaMIME = map[string]string{
	".mp3": "audio/mpeg", ".wav": "audio/wav", ".m4a": "audio/mp4",
	".aac": "audio/aac", ".flac": "audio/flac", ".ogg": "audio/ogg",
	".aiff": "audio/aiff", ".aif": "audio/aiff",
	".mp4": "video/mp4", ".mov": "video/quicktime", ".webm": "video/webm",
	".mpeg": "video/mpeg", ".mpg": "video/mpeg", ".avi": "video/x-msvideo",
}

// IsMediaPath reports whether a reference names audio or video by
// extension.
func IsMediaPath(ref string) bool {
	_, ok := mediaMIME[strings.ToLower(filepath.Ext(ref))]
	return ok
}

// Problem is a reference that could not be attached, with the reason to
// show the operator — a silent drop would look like the file was read.
type Problem struct {
	Ref    string
	Reason string
}

// pathChars are the characters that make a preceding rune part of a
// word: an @ after one of these is mid-word (an email address, a Python
// decorator, a Go module path) and not a reference. Everything else —
// spaces, brackets, and punctuation, including Japanese punctuation
// with no space after it ("…してください。@src/main.go") — may precede
// a reference.
const pathChars = `._-/~\`

// stoppers end a reference. Japanese punctuation is included because
// "@README.md、これ直して" has no space to stop at.
const stoppers = `,;:)]}"'、。，．「」『』（）【】〜|` + "`"

// Refs extracts @-references from text: an @ at a word start, followed
// by a run of path characters.
func Refs(text string) []string {
	var refs []string
	seen := map[string]bool{}
	for i, r := range text {
		if r != '@' {
			continue
		}
		if i > 0 {
			// Decode the previous RUNE, not the previous byte: a
			// multi-byte opener like 「 would otherwise look like an
			// ordinary character and the reference would be missed.
			prev, _ := utf8.DecodeLastRuneInString(text[:i])
			if unicode.IsLetter(prev) || unicode.IsDigit(prev) || strings.ContainsRune(pathChars, prev) {
				continue
			}
		}
		rest := text[i+1:]
		end := strings.IndexFunc(rest, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune(stoppers, r)
		})
		if end < 0 {
			end = len(rest)
		}
		ref := strings.TrimRight(rest[:end], ".")
		if ref == "" {
			continue
		}
		if !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

// Expand resolves every reference in text against projectDir — and, for
// absolute paths, the session work directory (ADR-0058): spilled MCP
// results and staged intermediates land there, and an operator who can
// see a path in the conversation must be able to @-reference it. Text
// itself is not modified: what the operator typed stays what the model
// sees as the instruction, with the contents delivered alongside.
func Expand(ctx context.Context, text, projectDir, workDir string, lim Limits) ([]Attachment, []Problem) {
	var atts []Attachment
	var problems []Problem
	total := 0

	images := 0
	for _, ref := range Refs(text) {
		// Images ride the same syntax but their own rules: a separate
		// byte budget, a per-message count, and — because an @ is
		// always operator-typed, never model-triggered — permission to
		// come from outside the project (ADR-0012).
		if ref == ClipboardRef || IsImagePath(ref) {
			att, err := attachImage(ref, projectDir, lim, &images)
			if err != nil {
				problems = append(problems, Problem{ref, err.Error()})
				continue
			}
			atts = append(atts, att)
			continue
		}
		// Documents share the operator-typed exception (ADR-0026): a
		// PDF attaches as bytes for the model to read natively, the
		// Office XML formats attach as locally extracted text.
		if IsDocumentPath(ref) {
			att, err := attachDocument(ref, projectDir, lim)
			if err != nil {
				problems = append(problems, Problem{ref, err.Error()})
				continue
			}
			atts = append(atts, att)
			continue
		}
		// Audio and video (ADR-0027): via the operator's bucket when
		// configured, inline under the media cap otherwise.
		if IsMediaPath(ref) {
			att, err := attachMedia(ctx, ref, projectDir, lim)
			if err != nil {
				problems = append(problems, Problem{ref, err.Error()})
				continue
			}
			atts = append(atts, att)
			continue
		}
		abs, err := resolve(projectDir, workDir, ref)
		if err != nil {
			problems = append(problems, Problem{ref, err.Error()})
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			problems = append(problems, Problem{ref, "not found"})
			continue
		}
		// The total budget is a real cap: each attachment may take at
		// most what is left, and a sliver too small to be useful is
		// skipped with a reason rather than attached.
		remaining := lim.TotalBytes - total
		if remaining < minUsefulBytes {
			problems = append(problems, Problem{ref, "skipped: attachment budget exhausted"})
			continue
		}
		perFile := min(lim.PerFileBytes, remaining)

		var att Attachment
		if info.IsDir() {
			att, err = attachDir(ref, abs, projectDir, workDir, lim)
		} else {
			att, err = attachFile(ref, abs, projectDir, workDir, perFile)
		}
		if err != nil {
			problems = append(problems, Problem{ref, err.Error()})
			continue
		}
		total += att.Bytes
		atts = append(atts, att)
	}
	return atts, problems
}

// minUsefulBytes is the floor below which a remaining budget buys
// nothing worth attaching.
const minUsefulBytes = 512

// attachImage loads one image reference: the clipboard, a project
// path, or — image extensions only — an absolute or ~ path anywhere.
func attachImage(ref, projectDir string, lim Limits, images *int) (Attachment, error) {
	if *images >= lim.MaxImages {
		return Attachment{}, fmt.Errorf("skipped: at most %d images per message", lim.MaxImages)
	}
	var data []byte
	var err error
	switch ref {
	case ClipboardRef:
		if lim.Clipboard == nil {
			return Attachment{}, fmt.Errorf("clipboard capture is unavailable here")
		}
		data, err = lim.Clipboard()
		if err != nil {
			return Attachment{}, err
		}
	default:
		abs, rerr := resolveImagePath(projectDir, ref)
		if rerr != nil {
			return Attachment{}, rerr
		}
		// Size on the open descriptor, before the read (ADR-0072
		// §4.5): the file was read whole and measured after.
		data, err = readBounded(abs, projectDir, lim.ImageBytes)
		if err != nil {
			return Attachment{}, err
		}
	}
	if len(data) == 0 {
		return Attachment{}, fmt.Errorf("empty image")
	}
	if len(data) > lim.ImageBytes {
		// Images cannot be truncated like text — a clipped PNG is not a
		// smaller picture, it is a broken file.
		return Attachment{}, fmt.Errorf("image is %d bytes; the limit is %d", len(data), lim.ImageBytes)
	}
	// Sniff the real type: the extension gated the route, the bytes
	// decide the claim. http.DetectContentType knows the common image
	// magics; HEIC/HEIF is identified by its ftyp box (ADR-0072 §4.8).
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		mime = heifMIME(data)
	}
	if mime == "" {
		return Attachment{}, fmt.Errorf("not an image (detected %s)", http.DetectContentType(data))
	}
	*images++
	return Attachment{Ref: ref, Kind: "image", Bytes: len(data), Data: data, MIME: mime}, nil
}

// attachDocument attaches one document (ADR-0026): a PDF as bytes the
// model reads natively; the Office XML formats as locally extracted
// text. Absolute and ~ paths are allowed on the same rationale as
// images — the reference is operator-typed, never model-triggered.
func attachDocument(ref, projectDir string, lim Limits) (Attachment, error) {
	abs, err := resolveImagePath(projectDir, ref) // operator-typed path resolution, shared with images
	if err != nil {
		return Attachment{}, err
	}
	// The Office branch read the file with NO size cap while the PDF
	// branch capped — an inconsistent missing guard on the same path
	// (review round 2). 32MiB mirrors read_document's file cap.
	data, err := readBounded(abs, projectDir, 32*1024*1024)
	if err != nil {
		return Attachment{}, err
	}
	if bytes.HasPrefix(data, []byte("%PDF-")) {
		cap := lim.DocumentBytes
		if cap <= 0 {
			cap = 12 * 1024 * 1024
		}
		if len(data) > cap {
			// A PDF cannot be truncated like text — a clipped file is a
			// broken file.
			return Attachment{}, fmt.Errorf("PDF is %d bytes; the limit is %d", len(data), cap)
		}
		return Attachment{Ref: ref, Kind: "document", Bytes: len(data), Data: data, MIME: "application/pdf"}, nil
	}
	text, note, err := docext.Extract(data, docext.DefaultLimits())
	if err != nil {
		return Attachment{}, err
	}
	if note != "" {
		text += "\n[" + note + "]"
	}
	return Attachment{Ref: ref, Kind: "document", Bytes: len(text), Content: text}, nil
}

// attachMedia attaches one audio/video file (ADR-0027). A configured
// bucket always wins over inline — inline media bytes are re-sent with
// every round's history replay; a gs:// URI is a few dozen bytes.
func attachMedia(ctx context.Context, ref, projectDir string, lim Limits) (Attachment, error) {
	abs, err := resolveImagePath(projectDir, ref) // operator-typed path resolution
	if err != nil {
		return Attachment{}, err
	}
	mime := mediaMIME[strings.ToLower(filepath.Ext(ref))]
	f, err := openConfined(abs, projectDir)
	if err != nil {
		return Attachment{}, fmt.Errorf("not found")
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return Attachment{}, fmt.Errorf("not found")
	}
	if lim.UploadMedia != nil {
		uri, err := lim.UploadMedia(ctx, f, ref, mime)
		if err != nil {
			return Attachment{}, fmt.Errorf("upload: %v", err)
		}
		return Attachment{Ref: ref, Kind: "media", Bytes: int(info.Size()), URI: uri, MIME: mime}, nil
	}
	cap := lim.MediaBytes
	if cap <= 0 {
		cap = 15 * 1024 * 1024
	}
	if info.Size() > int64(cap) {
		// A media file cannot be truncated — a clipped mp4 is a broken
		// file. Name both remedies.
		return Attachment{}, fmt.Errorf("media is %d bytes; the inline limit is %d — split the file, or set [gcp].bucket to route media through your GCS bucket", info.Size(), cap)
	}
	data, err := readBounded(abs, projectDir, cap)
	if err != nil {
		return Attachment{}, err
	}
	if strings.HasPrefix(http.DetectContentType(data), "text/") {
		return Attachment{}, fmt.Errorf("not a media file (plain text under a media extension)")
	}
	return Attachment{Ref: ref, Kind: "media", Bytes: len(data), Data: data, MIME: mime}, nil
}

// resolveImagePath resolves an image reference. In-project paths go
// through the same confinement as everything else; absolute and ~
// paths are allowed for images because the reference is operator-typed
// (ADR-0012 — revisit if @ ever parses anything but typed input).
func resolveImagePath(projectDir, ref string) (string, error) {
	p := ref
	if strings.HasPrefix(p, "~/") || p == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot resolve ~")
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		// A relative image reference means the project; absolute paths
		// take the anywhere branch below, work directory included.
		return resolve(projectDir, "", ref)
	}
	p = filepath.Clean(p)
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("not found")
	}
	info, err := os.Stat(real)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("not a file")
	}
	return real, nil
}

func attachFile(ref, abs, projectDir, workDir string, cap int) (Attachment, error) {
	f, err := openConfined(abs, projectDir, workDir)
	if err != nil {
		return Attachment{}, fmt.Errorf("unreadable")
	}
	defer func() { _ = f.Close() }()
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	// Bounded read and a rune-boundary cut (ADR-0072 §4.5): the file
	// was read whole and sliced by byte.
	data, err := io.ReadAll(io.LimitReader(f, int64(cap)+1))
	if err != nil {
		return Attachment{}, fmt.Errorf("unreadable")
	}
	content := string(data)
	if len(content) > cap {
		cut := cutRunes(content, cap)
		content = cut + fmt.Sprintf("\n[truncated: %d of %d bytes shown]", len(cut), size)
	}
	return Attachment{Ref: ref, Kind: "file", Content: content, Bytes: len(content)}, nil
}

// openConfined opens abs for reading. A path inside the project or the
// session work directory goes through an os.Root at that root, so a
// link swapped between the resolve and the open is refused (the file
// tools' rule); a path elsewhere — the operator-typed image, document
// and media grants — is opened directly. The work directory was
// missing here at first and its text references opened bare (review
// after v0.68.2, R05).
func openConfined(abs string, roots ...string) (*os.File, error) {
	for _, dir := range roots {
		if dir == "" || !within(dir, abs) {
			continue
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			return nil, err
		}
		defer func() { _ = root.Close() }()
		rel, err := filepath.Rel(dir, abs)
		if err != nil {
			return nil, err
		}
		return openRegular(func(flag int) (*os.File, error) { return root.OpenFile(rel, flag, 0) })
	}
	return openRegular(func(flag int) (*os.File, error) { return os.OpenFile(abs, flag, 0) })
}

// openRegular opens for reading without blocking and admits only a
// regular file or a directory, checked on the opened descriptor: an
// `@fifo` with no writer blocked the open past any cancel (ADR-0072
// §4.8). O_NONBLOCK does not change how a regular file reads, and is
// cleared afterwards.
func openRegular(open func(flag int) (*os.File, error)) (*os.File, error) {
	f, err := open(os.O_RDONLY | syscall.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !st.Mode().IsRegular() && !st.IsDir() {
		_ = f.Close()
		return nil, fmt.Errorf("not a regular file (%s)", st.Mode().Type())
	}
	_ = syscall.SetNonblock(int(f.Fd()), false)
	return f, nil
}

// readBounded reads abs whole only when its size is within cap — the
// size is taken on the open descriptor, and the read is limited to
// cap+1 so a file that grew meanwhile is refused, never held.
func readBounded(abs, projectDir string, cap int) ([]byte, error) {
	f, err := openConfined(abs, projectDir)
	if err != nil {
		return nil, fmt.Errorf("unreadable")
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err == nil && st.Size() > int64(cap) {
		return nil, fmt.Errorf("file is %d bytes; the limit is %d", st.Size(), cap)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(cap)+1))
	if err != nil {
		return nil, fmt.Errorf("unreadable")
	}
	if len(data) > cap {
		return nil, fmt.Errorf("file exceeds the %d byte limit", cap)
	}
	return data, nil
}

// cutRunes truncates s to at most n bytes without splitting a UTF-8
// sequence.
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func attachDir(ref, abs, projectDir, workDir string, lim Limits) (Attachment, error) {
	// Bounded listing through the root (ADR-0072 §4.5): one entry past
	// the display cap is enough to say "more" — how many more is not
	// known, and is not claimed.
	entries, err := readDirCapped(abs, lim.DirEntries+1, projectDir, workDir)
	if err != nil {
		return Attachment{}, fmt.Errorf("unreadable")
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) > lim.DirEntries {
		names = names[:lim.DirEntries]
		names = append(names, fmt.Sprintf("[more than %d entries — the rest not shown]", lim.DirEntries))
	}
	content := strings.Join(names, "\n")
	if content == "" {
		content = "(empty directory)"
	}
	return Attachment{Ref: ref, Kind: "directory", Content: content, Bytes: len(content)}, nil
}

// resolve confines a reference to the project directory and, when the
// session has one, the work directory — checking both the lexical path
// and the symlink-resolved one. A relative reference always means the
// project; the work directory is reached only by the absolute path the
// conversation shows.
func resolve(projectDir, workDir, ref string) (string, error) {
	if projectDir == "" {
		return "", fmt.Errorf("no project directory")
	}
	inRoots := func(p string) bool {
		return within(projectDir, p) || (workDir != "" && within(workDir, p))
	}
	outside := func(suffix string) error {
		if workDir == "" {
			return fmt.Errorf("outside the project directory%s", suffix)
		}
		return fmt.Errorf("outside the project and work directories%s", suffix)
	}
	p := ref
	if strings.HasPrefix(p, "~") {
		return "", outside("")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	p = filepath.Clean(p)
	if !inRoots(p) {
		return "", outside("")
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("not found")
	}
	if !inRoots(real) {
		return "", outside(" (via symlink)")
	}
	return p, nil
}

func within(base, p string) bool {
	base = filepath.Clean(base)
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// Complete returns project-relative paths starting with prefix, for the
// input box's Tab completion. Directories carry a trailing separator so
// the next Tab can descend into them.
func Complete(projectDir, prefix string, max int) []string {
	dir, base := ".", ""
	switch {
	case prefix == "":
		// dir/base stay at their defaults: list the project root.
	case strings.HasSuffix(prefix, string(filepath.Separator)):
		dir = strings.TrimSuffix(prefix, string(filepath.Separator))
	case strings.Contains(prefix, string(filepath.Separator)):
		dir, base = filepath.Dir(prefix), filepath.Base(prefix)
	default:
		base = prefix
	}
	// Completion suggests project-relative names only; an absolute
	// work-dir path is typed (or pasted) whole, not completed.
	abs, err := resolve(projectDir, "", dir)
	if err != nil {
		return nil
	}
	entries, err := readDirCapped(abs, completionEntryCap, projectDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue // hidden files only when explicitly asked for
		}
		if base != "" && !strings.HasPrefix(name, base) {
			continue
		}
		rel := name
		if dir != "." {
			rel = filepath.Join(dir, name)
		}
		if e.IsDir() {
			rel += string(filepath.Separator)
		}
		out = append(out, rel)
		if len(out) >= max {
			break
		}
	}
	sort.Strings(out)
	return out
}

// CommonPrefix returns the longest shared prefix of the candidates, so
// Tab can advance as far as the choice is unambiguous.
func CommonPrefix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := candidates[0]
	for _, c := range candidates[1:] {
		for !strings.HasPrefix(c, prefix) {
			prefix = prefix[:len(prefix)-1]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// completionEntryCap bounds a directory read for Tab completion.
const completionEntryCap = 2000

// readDirCapped lists at most n entries of a directory, opened through
// its confinement root.
func readDirCapped(abs string, n int, roots ...string) ([]os.DirEntry, error) {
	d, err := openConfined(abs, roots...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = d.Close() }()
	entries, err := d.ReadDir(n)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return entries, nil
}

// heifMIME identifies a HEIF/HEIC file by its ftyp box — box length
// checked against the data, brand from the major brand or the
// compatible list — or "" (the same rule as the view_image tool).
func heifMIME(data []byte) string {
	if len(data) < 16 || string(data[4:8]) != "ftyp" {
		return ""
	}
	size := int(uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3]))
	if size < 16 || size > len(data) || size%4 != 0 {
		return ""
	}
	brands := []string{string(data[8:12])}
	for i := 16; i+4 <= size; i += 4 {
		brands = append(brands, string(data[i:i+4]))
	}
	for _, b := range brands {
		switch b {
		case "heic", "heix", "hevc", "hevx", "heim", "heis":
			return "image/heic"
		case "mif1", "msf1", "heif":
			return "image/heif"
		}
	}
	return ""
}

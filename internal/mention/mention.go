// Package mention resolves @-references in user input to project files
// and directories, so "@src/main.go これ直して" carries the file with it.
//
// Resolution is confined to the project directory (symlinks included):
// an @-reference is a convenience, not a way around the containment the
// file tools enforce.
package mention

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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
}

// DefaultLimits are sized so a handful of source files fit comfortably
// while a stray @ on a huge tree cannot blow up the context.
func DefaultLimits() Limits {
	return Limits{
		PerFileBytes: 64 * 1024, TotalBytes: 256 * 1024, DirEntries: 200,
		ImageBytes: 8 * 1024 * 1024, MaxImages: 4,
	}
}

// Attachment is one resolved reference.
type Attachment struct {
	Ref     string // as typed, without the @
	Kind    string // "file", "directory" or "image"
	Content string
	Bytes   int
	// Data and MIME are set for images (ADR-0012).
	Data []byte
	MIME string
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

// Expand resolves every reference in text against projectDir and returns
// the attachments plus the references that failed. Text itself is not
// modified: what the operator typed stays what the model sees as the
// instruction, with the contents delivered alongside.
func Expand(text, projectDir string, lim Limits) ([]Attachment, []Problem) {
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
		abs, err := resolve(projectDir, ref)
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
			att, err = attachDir(ref, abs, lim)
		} else {
			att, err = attachFile(ref, abs, perFile)
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
	switch {
	case ref == ClipboardRef:
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
		data, err = os.ReadFile(abs)
		if err != nil {
			return Attachment{}, fmt.Errorf("unreadable")
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
	// decide the claim. http.DetectContentType knows the image magics.
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return Attachment{}, fmt.Errorf("not an image (detected %s)", mime)
	}
	*images++
	return Attachment{Ref: ref, Kind: "image", Bytes: len(data), Data: data, MIME: mime}, nil
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
		return resolve(projectDir, ref)
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

func attachFile(ref, abs string, cap int) (Attachment, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return Attachment{}, fmt.Errorf("unreadable")
	}
	content := string(data)
	if len(content) > cap {
		content = content[:cap] +
			fmt.Sprintf("\n[truncated: %d of %d bytes shown]", cap, len(data))
	}
	return Attachment{Ref: ref, Kind: "file", Content: content, Bytes: len(content)}, nil
}

func attachDir(ref, abs string, lim Limits) (Attachment, error) {
	entries, err := os.ReadDir(abs)
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
	total := len(names)
	if total > lim.DirEntries {
		names = names[:lim.DirEntries]
		names = append(names, fmt.Sprintf("[%d more entries not shown]", total-lim.DirEntries))
	}
	content := strings.Join(names, "\n")
	if content == "" {
		content = "(empty directory)"
	}
	return Attachment{Ref: ref, Kind: "directory", Content: content, Bytes: len(content)}, nil
}

// resolve confines a reference to the project directory, checking both
// the lexical path and the symlink-resolved one.
func resolve(projectDir, ref string) (string, error) {
	if projectDir == "" {
		return "", fmt.Errorf("no project directory")
	}
	p := ref
	if strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("outside the project directory")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	p = filepath.Clean(p)
	if !within(projectDir, p) {
		return "", fmt.Errorf("outside the project directory")
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("not found")
	}
	if !within(projectDir, real) {
		return "", fmt.Errorf("outside the project directory (via symlink)")
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
	abs, err := resolve(projectDir, dir)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(abs)
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

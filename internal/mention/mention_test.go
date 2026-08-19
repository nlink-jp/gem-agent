package mention

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func projectWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(real, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return real
}

func TestRefs(t *testing.T) {
	cases := []struct {
		text string
		want []string
	}{
		{"@README.md を読んで", []string{"README.md"}},
		{"@src/main.go と @docs/ を比較", []string{"src/main.go", "docs/"}},
		{"先頭以外の user@example.com は無視", nil},
		// Japanese sentences run punctuation straight into the next
		// word, with no space for the parser to lean on.
		{"これ直して。@src/main.go も見て", []string{"src/main.go"}},
		{"次は、@docs/ を確認", []string{"docs/"}},
		{"パス風の前置きは無視: ./tools@v1", nil},
		{"@decorator は参照扱い", []string{"decorator"}},
		{"「@a.txt」と(@b.txt)", []string{"a.txt", "b.txt"}},
		{"@README.md、これ直して。", []string{"README.md"}},
		{"重複 @x.txt @x.txt", []string{"x.txt"}},
		{"@ だけ", nil},
		{"no refs here", nil},
	}
	for _, c := range cases {
		got := Refs(c.text)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("Refs(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

func TestExpandFileAndDir(t *testing.T) {
	proj := projectWith(t, map[string]string{
		"README.md":  "# hello",
		"src/a.go":   "package a",
		"src/b.go":   "package b",
		"docs/x.txt": "doc",
	})
	atts, problems := Expand("@README.md と @src/ を見て", proj, DefaultLimits())
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if len(atts) != 2 {
		t.Fatalf("attachments = %d", len(atts))
	}
	if atts[0].Kind != "file" || atts[0].Content != "# hello" {
		t.Errorf("file attachment = %+v", atts[0])
	}
	if atts[1].Kind != "directory" || !strings.Contains(atts[1].Content, "a.go") {
		t.Errorf("directory attachment = %+v", atts[1])
	}
}

// TestExpandConfinement: an @-reference must not become a way around the
// project containment the file tools enforce.
func TestExpandConfinement(t *testing.T) {
	proj := projectWith(t, map[string]string{"in.txt": "inside"})
	outside := projectWith(t, map[string]string{"secret.txt": "s3cret"})

	// Symlink pointing out of the project.
	if err := os.Symlink(outside, filepath.Join(proj, "link")); err != nil {
		t.Fatal(err)
	}

	for _, ref := range []string{
		"/etc/passwd",
		"../escape.txt",
		"~/.ssh/id_rsa",
		filepath.Join(outside, "secret.txt"),
		"link/secret.txt",
	} {
		atts, problems := Expand("@"+ref, proj, DefaultLimits())
		if len(atts) != 0 {
			t.Errorf("%q attached %d files, want 0", ref, len(atts))
		}
		if len(problems) != 1 {
			t.Errorf("%q should report a problem, got %v", ref, problems)
		}
	}
}

func TestExpandMissingFileIsReported(t *testing.T) {
	proj := projectWith(t, nil)
	atts, problems := Expand("@nope.txt", proj, DefaultLimits())
	if len(atts) != 0 || len(problems) != 1 || problems[0].Reason != "not found" {
		t.Errorf("atts=%v problems=%v", atts, problems)
	}
}

func TestExpandLimits(t *testing.T) {
	big := strings.Repeat("x", 5000)
	proj := projectWith(t, map[string]string{"big.txt": big, "big2.txt": big})
	lim := Limits{PerFileBytes: 1000, TotalBytes: 1500, DirEntries: 10}

	atts, problems := Expand("@big.txt @big2.txt", proj, lim)
	if len(atts) != 1 {
		t.Fatalf("budget should stop the second file: atts=%d", len(atts))
	}
	if !strings.Contains(atts[0].Content, "[truncated") {
		t.Error("oversized file should carry a truncation marker")
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "budget") {
		t.Errorf("budget exhaustion should be reported: %v", problems)
	}
}

func TestComplete(t *testing.T) {
	proj := projectWith(t, map[string]string{
		"README.md": "x", "main.go": "x", "makefile": "x",
		"src/a.go": "x", ".hidden": "x",
	})

	got := Complete(proj, "ma", 20)
	if strings.Join(got, "|") != "main.go|makefile" {
		t.Errorf("Complete(ma) = %v", got)
	}
	if pre := CommonPrefix(got); pre != "ma" {
		t.Errorf("CommonPrefix = %q", pre)
	}

	// Directories carry a separator so the next Tab descends.
	got = Complete(proj, "sr", 20)
	if len(got) != 1 || got[0] != "src/" {
		t.Errorf("Complete(sr) = %v", got)
	}
	got = Complete(proj, "src/", 20)
	if len(got) != 1 || got[0] != "src/a.go" {
		t.Errorf("Complete(src/) = %v", got)
	}

	// Hidden entries only when explicitly asked for.
	if got = Complete(proj, "", 20); contains(got, ".hidden") {
		t.Errorf("bare completion should hide dotfiles: %v", got)
	}
	if got = Complete(proj, ".h", 20); !contains(got, ".hidden") {
		t.Errorf("explicit dot prefix should show dotfiles: %v", got)
	}

	// Completion is confined too.
	if got = Complete(proj, "../", 20); len(got) != 0 {
		t.Errorf("completion escaped the project: %v", got)
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- images (ADR-0012) ---

// realTempDir resolves the macOS /var → /private/var symlink, as the
// production caller does for the project dir (registry.ProjectDir()).
func realTempDir(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// tinyPNG is a valid 1x1 PNG (pre-encoded); DetectContentType must see
// image/png in its magic bytes.
var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
	0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
	0x00, 0x00, 0x03, 0x00, 0x01, 0xCE, 0xFC, 0x53, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

func TestExpandAttachesProjectImages(t *testing.T) {
	dir := realTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	atts, problems := Expand("@shot.png what is this", dir, DefaultLimits())
	if len(problems) != 0 || len(atts) != 1 {
		t.Fatalf("atts=%v problems=%v", atts, problems)
	}
	a := atts[0]
	if a.Kind != "image" || a.MIME != "image/png" || len(a.Data) != len(tinyPNG) {
		t.Errorf("attachment = kind=%q mime=%q %d bytes", a.Kind, a.MIME, len(a.Data))
	}
}

// The screenshot-on-Desktop workflow: images — and only images — may be
// referenced from outside the project, because @ is operator-typed.
func TestExpandImageOutsideProjectAllowedTextRefused(t *testing.T) {
	project, outside := realTempDir(t), realTempDir(t)
	img := filepath.Join(outside, "screen.png")
	if err := os.WriteFile(img, tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "notes.txt"), []byte("text"), 0o644); err != nil {
		t.Fatal(err)
	}

	atts, problems := Expand("@"+img+" describe", project, DefaultLimits())
	if len(problems) != 0 || len(atts) != 1 || atts[0].Kind != "image" {
		t.Fatalf("out-of-project image refused: atts=%v problems=%v", atts, problems)
	}

	// A text file outside the project stays refused.
	_, problems = Expand("@"+filepath.Join(outside, "notes.txt")+" read", project, DefaultLimits())
	if len(problems) != 1 {
		t.Fatalf("out-of-project text was attached: %v", problems)
	}
}

// The extension gates the route; the bytes decide the claim. A text
// file renamed .png must not ride the image exception out of the tree.
func TestExpandRejectsFakeImages(t *testing.T) {
	project, outside := realTempDir(t), realTempDir(t)
	fake := filepath.Join(outside, "fake.png")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho secrets\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	atts, problems := Expand("@"+fake, project, DefaultLimits())
	if len(atts) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Reason, "not an image") {
		t.Fatalf("fake image accepted: atts=%v problems=%v", atts, problems)
	}
}

func TestExpandClipboardRoute(t *testing.T) {
	lim := DefaultLimits()
	lim.Clipboard = func() ([]byte, error) { return tinyPNG, nil }
	atts, problems := Expand("@clipboard ここがおかしい", t.TempDir(), lim)
	if len(problems) != 0 || len(atts) != 1 || atts[0].Kind != "image" || atts[0].Ref != "clipboard" {
		t.Fatalf("atts=%v problems=%v", atts, problems)
	}

	// No clipboard hook: a reported problem, never a silent no-op.
	_, problems = Expand("@clipboard x", t.TempDir(), DefaultLimits())
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "unavailable") {
		t.Fatalf("problems = %v", problems)
	}

	// Clipboard errors surface as-is ("no image on the clipboard").
	lim.Clipboard = func() ([]byte, error) { return nil, fmt.Errorf("no image on the clipboard") }
	_, problems = Expand("@clipboard x", t.TempDir(), lim)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "no image") {
		t.Fatalf("problems = %v", problems)
	}
}

func TestExpandImageLimits(t *testing.T) {
	dir := realTempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "a.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	lim := DefaultLimits()
	lim.ImageBytes = 8 // smaller than tinyPNG: refused whole, never truncated
	_, problems := Expand("@a.png", dir, lim)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "limit") {
		t.Fatalf("oversized image: %v", problems)
	}

	lim = DefaultLimits()
	lim.MaxImages = 1
	if err := os.WriteFile(filepath.Join(dir, "b.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	atts, problems := Expand("@a.png @b.png", dir, lim)
	if len(atts) != 1 || len(problems) != 1 {
		t.Fatalf("image count cap: atts=%d problems=%v", len(atts), problems)
	}
}

package mention

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	atts, problems := Expand(context.Background(), "@README.md と @src/ を見て", proj, "", DefaultLimits())
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
		atts, problems := Expand(context.Background(), "@"+ref, proj, "", DefaultLimits())
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
	atts, problems := Expand(context.Background(), "@nope.txt", proj, "", DefaultLimits())
	if len(atts) != 0 || len(problems) != 1 || problems[0].Reason != "not found" {
		t.Errorf("atts=%v problems=%v", atts, problems)
	}
}

func TestExpandLimits(t *testing.T) {
	big := strings.Repeat("x", 5000)
	proj := projectWith(t, map[string]string{"big.txt": big, "big2.txt": big})
	lim := Limits{PerFileBytes: 1000, TotalBytes: 1500, DirEntries: 10}

	atts, problems := Expand(context.Background(), "@big.txt @big2.txt", proj, "", lim)
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
	atts, problems := Expand(context.Background(), "@shot.png what is this", dir, "", DefaultLimits())
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

	atts, problems := Expand(context.Background(), "@"+img+" describe", project, "", DefaultLimits())
	if len(problems) != 0 || len(atts) != 1 || atts[0].Kind != "image" {
		t.Fatalf("out-of-project image refused: atts=%v problems=%v", atts, problems)
	}

	// A text file outside the project stays refused.
	_, problems = Expand(context.Background(), "@"+filepath.Join(outside, "notes.txt")+" read", project, "", DefaultLimits())
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
	atts, problems := Expand(context.Background(), "@"+fake, project, "", DefaultLimits())
	if len(atts) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Reason, "not an image") {
		t.Fatalf("fake image accepted: atts=%v problems=%v", atts, problems)
	}
}

func TestExpandClipboardRoute(t *testing.T) {
	lim := DefaultLimits()
	lim.Clipboard = func() ([]byte, error) { return tinyPNG, nil }
	atts, problems := Expand(context.Background(), "@clipboard ここがおかしい", t.TempDir(), "", lim)
	if len(problems) != 0 || len(atts) != 1 || atts[0].Kind != "image" || atts[0].Ref != "clipboard" {
		t.Fatalf("atts=%v problems=%v", atts, problems)
	}

	// No clipboard hook: a reported problem, never a silent no-op.
	_, problems = Expand(context.Background(), "@clipboard x", t.TempDir(), "", DefaultLimits())
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "unavailable") {
		t.Fatalf("problems = %v", problems)
	}

	// Clipboard errors surface as-is ("no image on the clipboard").
	lim.Clipboard = func() ([]byte, error) { return nil, fmt.Errorf("no image on the clipboard") }
	_, problems = Expand(context.Background(), "@clipboard x", t.TempDir(), "", lim)
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
	_, problems := Expand(context.Background(), "@a.png", dir, "", lim)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "limit") {
		t.Fatalf("oversized image: %v", problems)
	}

	lim = DefaultLimits()
	lim.MaxImages = 1
	if err := os.WriteFile(filepath.Join(dir, "b.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	atts, problems := Expand(context.Background(), "@a.png @b.png", dir, "", lim)
	if len(atts) != 1 || len(problems) != 1 {
		t.Fatalf("image count cap: atts=%d problems=%v", len(atts), problems)
	}
}

// The session work directory (ADR-0058) holds spilled MCP results and
// staged intermediates, and the operator can see those paths in the
// conversation — so an @-reference to one has to attach. Found by
// applying the v0.56.1 lesson (enumerate every consumer of the old
// one-root boundary): this resolver was the remaining consumer.
func TestWorkDirReferencesAttach(t *testing.T) {
	// Resolved roots, as production passes them (registry resolves both;
	// macOS t.TempDir() sits under a symlinked /var).
	proj := projectWith(t, nil)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	spilled := filepath.Join(work, "rdns-lookup-lookup_rdns-99d6ac00.json")
	if err := os.WriteFile(spilled, []byte(`{"kind":"rdns"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	atts, problems := Expand(context.Background(), "@"+spilled+" を見て", proj, work, DefaultLimits())
	if len(problems) != 0 {
		t.Fatalf("work-dir reference refused: %v", problems)
	}
	if len(atts) != 1 || !strings.Contains(atts[0].Content, `"kind":"rdns"`) {
		t.Fatalf("attachment = %+v", atts)
	}
}

// Adding the root must not widen anything else: outside both roots is
// still refused, a sibling-prefix directory is outside, and a symlink
// planted in the work directory is not a way out.
func TestWorkDirDoesNotWidenTheBoundary(t *testing.T) {
	proj := projectWith(t, nil)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, problems := Expand(context.Background(), "@"+filepath.Join(outside, "x.txt"), proj, work, DefaultLimits())
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "outside the project and work directories") {
		t.Fatalf("outside both roots should be refused with both roots named: %v", problems)
	}

	sibling := work + "-evil"
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sibling, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, problems = Expand(context.Background(), "@"+filepath.Join(sibling, "x.txt"), proj, work, DefaultLimits())
	if len(problems) != 1 {
		t.Fatalf("sibling-prefix path should be refused: %v", problems)
	}

	link := filepath.Join(work, "escape.txt")
	if err := os.Symlink(filepath.Join(outside, "x.txt"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, problems = Expand(context.Background(), "@"+link, proj, work, DefaultLimits())
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "symlink") {
		t.Fatalf("symlink out of the work dir should be refused: %v", problems)
	}
}

// A session with no work directory keeps the one-root wording — the
// refusal must not claim a root that does not exist.
func TestNoWorkDirKeepsTheOneRootWording(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, problems := Expand(context.Background(), "@"+filepath.Join(outside, "x.txt"), t.TempDir(), "", DefaultLimits())
	if len(problems) != 1 || problems[0].Reason != "outside the project directory" {
		t.Fatalf("got %v", problems)
	}
}

// ADR-0072 §4.5: attachments are size-gated on the open descriptor
// and read bounded; text is cut on a rune boundary with the real size
// in the note.
func TestAttachmentsAreBoundedAndRuneSafe(t *testing.T) {
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	huge := filepath.Join(project, "huge.png")
	f, err := os.Create(huge)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(64 << 20); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if err := os.WriteFile(filepath.Join(project, "ja.txt"), []byte(strings.Repeat("あ", 1000)), 0o644); err != nil {
		t.Fatal(err)
	}
	lim := DefaultLimits()
	lim.PerFileBytes = 100
	atts, problems := Expand(context.Background(), "@huge.png @ja.txt", project, "", lim)
	for _, p := range problems {
		if p.Ref == "huge.png" && !strings.Contains(p.Reason, "limit") {
			t.Errorf("oversize image refused for the wrong reason: %s", p.Reason)
		}
	}
	found := false
	for _, a := range atts {
		if a.Ref == "huge.png" {
			t.Fatal("the oversize image was attached")
		}
		if a.Ref == "ja.txt" {
			found = true
			head := strings.SplitN(a.Content, "\n", 2)[0]
			if !utf8.ValidString(head) || head != strings.Repeat("あ", 33) {
				t.Errorf("text cut through a rune: %q", head)
			}
			if !strings.Contains(a.Content, "[truncated: 99 of 3000 bytes shown]") {
				t.Errorf("note wrong: %q", a.Content)
			}
		}
	}
	if !found {
		t.Fatalf("ja.txt not attached: %+v %+v", atts, problems)
	}
}

// R05: a work-directory text reference opens through the work root — a
// link swapped in after the resolve is refused.
func TestWorkDirAttachmentOpensThroughItsRoot(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	work, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	p := filepath.Join(work, "note.txt")
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(p, []byte("ordinary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("OUTSIDE-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	abs, err := resolve(project, work, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p, p+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, p); err != nil {
		t.Fatal(err)
	}
	if att, err := attachFile(p, abs, project, work, 1024); err == nil {
		t.Fatalf("work-dir reference read outside its root: %q", att.Content)
	}
}

// R12: an omission the listing did not count is not reported as a
// count.
func TestDirectoryOmissionIsNotACount(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lim := DefaultLimits()
	lim.DirEntries = 2
	att, err := attachDir("dir", dir, "", "", lim)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(att.Content, "1 more entries") || !strings.Contains(att.Content, "more than 2 entries") {
		t.Errorf("omission wording wrong: %q", att.Content)
	}
}

// R05: the upload receives the file mention verified and opened — a
// swap after the check reaches neither the hash nor the bucket.
func TestUploadUsesTheVerifiedFile(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	outside, _ := filepath.EvalSymlinks(t.TempDir())
	p := filepath.Join(project, "clip.mp4")
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(p, []byte("MEDIA-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("OUTSIDE-SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}
	var captured string
	lim := DefaultLimits()
	lim.UploadMedia = func(_ context.Context, f *os.File, _, _ string) (string, error) {
		if err := os.Rename(p, p+".original"); err != nil {
			return "", err
		}
		if err := os.Symlink(secret, p); err != nil {
			return "", err
		}
		data, err := io.ReadAll(f)
		captured = string(data)
		return "gs://test-only/not-uploaded", err
	}
	if _, err := attachMedia(context.Background(), "clip.mp4", project, lim); err != nil {
		t.Fatal(err)
	}
	if captured != "MEDIA-SENTINEL" {
		t.Fatalf("upload read %q, want the verified file's bytes", captured)
	}
}

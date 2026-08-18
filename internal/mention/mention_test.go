package mention

import (
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

package mention

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func onePixelPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestUnquotePath(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "shots/a.png", "shots/a.png"},
		{"spaces are part of a path", "Screenshot 2026-09-21 at 10.00.00.png", "Screenshot 2026-09-21 at 10.00.00.png"},
		{"what dragging a file into the terminal types", `/Users/you/Desktop/Screenshot\ 2026-09-21\ at\ 10.00.00.png`, "/Users/you/Desktop/Screenshot 2026-09-21 at 10.00.00.png"},
		{"double quotes", `"my shots/a b.png"`, "my shots/a b.png"},
		{"single quotes", `'my shots/a b.png'`, "my shots/a b.png"},
		{"an escaped backslash is a backslash", `a\\b.png`, `a\b.png`},
		{"other escaped characters", `a\(1\).png`, "a(1).png"},
		{"surrounding blanks", "  a.png  ", "a.png"},
		{"a lone quote is a character", `it's.png`, "it's.png"},
		{"nothing is expanded", "$HOME/a.png", "$HOME/a.png"},
	} {
		if got := UnquotePath(tc.in); got != tc.want {
			t.Errorf("%s: UnquotePath(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}

// The defect: /show put an "@" in front of its path and ran it through the
// text grammar, which ends a reference at the first space. macOS names every
// screenshot with spaces.
func TestImageTakesAPathNotAReference(t *testing.T) {
	project := realTempDir(t)
	name := "Screenshot 2026-09-21 at 10.00.00.png"
	want := onePixelPNG(t)
	if err := os.WriteFile(filepath.Join(project, name), want, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		name,
		filepath.Join(project, name),
		`Screenshot\ 2026-09-21\ at\ 10.00.00.png`,
		`"` + name + `"`,
	} {
		att, err := Image(path, project, DefaultLimits())
		if err != nil {
			t.Errorf("Image(%q): %v", path, err)
			continue
		}
		if att.Kind != "image" || !bytes.Equal(att.Data, want) {
			t.Errorf("Image(%q) = kind %q, %d bytes; want the file", path, att.Kind, len(att.Data))
		}
	}

	// The grammar it used to go through, for the record of why it failed.
	if refs := Refs("@" + name); len(refs) != 1 || refs[0] != "Screenshot" {
		t.Errorf("Refs is expected to stop at the space (that is its job in running text); got %q", refs)
	}
}

func TestImageRefusesWhatIsNotAnImage(t *testing.T) {
	project := realTempDir(t)
	if err := os.WriteFile(filepath.Join(project, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Image("notes.txt", project, DefaultLimits()); err == nil {
		t.Error("a text file was accepted as an image")
	}
	if _, err := Image("missing one.png", project, DefaultLimits()); err == nil {
		t.Error("a missing file was accepted")
	}
}

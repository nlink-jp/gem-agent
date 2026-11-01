package pathid

import "testing"

// The two spellings of "ドライブ": precomposed (NFC, what an input
// method types) and decomposed (NFD, what some providers create).
const (
	nfc = "ドライブ"
	nfd = "ドライブ"
)

func TestSameIgnoresTheUnicodeForm(t *testing.T) {
	if !Same("/a/"+nfc+"/p", "/a/"+nfd+"/p") {
		t.Error("the NFC and NFD spellings of one path do not match")
	}
	if !Same("/a/b/../c", "/a/c") {
		t.Error("a path is not compared cleaned")
	}
	if Same("/a/b", "/a/c") || Same("", "/a") || !Same("", "") {
		t.Error("different paths matched, or empty matched a path")
	}
}

// Case is not folded: on a case-sensitive volume these are two
// directories, and one must not inherit the other's records.
func TestSameDoesNotFoldCase(t *testing.T) {
	if Same("/a/Proj", "/a/proj") {
		t.Error("paths differing in case matched")
	}
}

func TestWithinAcrossSpellings(t *testing.T) {
	base := "/a/" + nfd
	for _, p := range []string{"/a/" + nfc, "/a/" + nfc + "/x/y", base + "/x"} {
		if !Within(base, p) {
			t.Errorf("%q is not within %q", p, base)
		}
	}
	for _, p := range []string{"/a/" + nfc + "x", "/a", "/b/" + nfc} {
		if Within(base, p) {
			t.Errorf("%q is within %q", p, base)
		}
	}
}

func TestRelAcrossSpellings(t *testing.T) {
	rel, err := Rel("/a/"+nfd, "/a/"+nfc+"/x/y")
	if err != nil || rel != "x/y" {
		t.Errorf("Rel = %q, %v; want x/y", rel, err)
	}
}

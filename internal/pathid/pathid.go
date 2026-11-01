// Package pathid compares paths as macOS names them (ADR-0101). A path
// is a spelling: APFS and HFS+ treat the precomposed and decomposed
// Unicode forms of a name as one name, so a project recorded under one
// form must be found under the other, and a path the model or the
// operator types in one form is inside a project the kernel spells in
// the other. Every comparison of two paths — equality, containment, the
// path of one relative to another — goes through this package, never
// through == or strings.HasPrefix on the raw strings.
//
// The comparison is a pure function of the strings: it does not touch
// the disk, so it cannot stall on a volume that no longer answers and
// cannot follow a symlink somewhere else. Case is not folded: on a
// case-sensitive volume two names that differ in case are two
// directories, and folding them would hand one's trust to the other.
package pathid

import (
	"path/filepath"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// Key is the form paths are compared in: cleaned, and Unicode-normalised
// to NFC. It is a comparison key, not a path to show or open — the
// spelling on disk may be the other form.
func Key(p string) string {
	return norm.NFC.String(filepath.Clean(p))
}

// Same reports whether a and b name the same path. Empty matches only
// empty.
func Same(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	return Key(a) == Key(b)
}

// Within reports whether p is base or lies under it.
func Within(base, p string) bool {
	b, q := Key(base), Key(p)
	return q == b || strings.HasPrefix(q, b+string(filepath.Separator))
}

// Rel is filepath.Rel over the comparison keys: the path of p relative
// to base however either is spelled. The result is in NFC, which the
// volume resolves to the same name.
func Rel(base, p string) (string, error) {
	return filepath.Rel(Key(base), Key(p))
}

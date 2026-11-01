// Package pathid decides whether two paths name the same directory
// (ADR-0101). A path is a spelling: macOS treats the precomposed and
// decomposed Unicode forms of a name — and, on a case-folding volume,
// its case — as one name, so a project recorded under one spelling must
// still be found under another. Every comparison of a recorded project
// path with the current one goes through Same.
package pathid

import "os"

// Same reports whether a and b name the same directory: equal strings,
// or two paths that both exist and resolve to the same file. A path
// that no longer exists matches only its own string.
func Same(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(sa, sb)
}

//go:build !unix

package termimg

// CellAspect has nothing to ask on a platform this runtime does not target.
func CellAspect(int) (float64, bool) { return 0, false }

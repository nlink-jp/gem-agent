//go:build !unix

package termimg

// CellSize has nothing to ask on a platform this runtime does not target.
func CellSize(int) (Cell, bool) { return Cell{}, false }

package sandbox

import (
	"os"
	"path/filepath"

	"github.com/nlink-jp/gem-agent/internal/pathid"
)

// mountTrees are the trees where a path the model names may be a share:
// resolving one before approval would touch the share, or wait on a
// stale one (ADR-0108 §1).
var mountTrees = []string{"/Volumes", "/Network", "/net"}

// IntoRoot rewrites p — a path typed through a link that lies outside
// every root, such as ~/Dropbox → ~/Library/CloudStorage/Dropbox — into
// the real path it names, when that lies inside one of roots (ADR-0108
// §1). roots are real paths. Only the parent is resolved: the last
// element stays as typed, so a link there is judged on its real name and
// written by replacement like any link inside a root.
//
// p comes back unchanged, with false, when it is relative, already lies
// lexically inside a root, is credential material by its spelling (the
// floors that judge the spelling keep seeing it), lies under a mount
// tree, or does not lead into a root — the file tools then refuse it as
// they always did, saying nothing about where a link leads.
func IntoRoot(p string, roots []string) (string, bool) {
	if !filepath.IsAbs(p) {
		return p, false
	}
	c := filepath.Clean(p)
	if inAny(roots, c) || CredentialPath(c) {
		return p, false
	}
	for _, m := range mountTrees {
		if pathid.Within(m, c) {
			return p, false
		}
	}
	parent := filepath.Dir(c)
	if parent == c {
		return p, false
	}
	real, ok := resolveExistingParent(parent)
	if !ok {
		return p, false
	}
	cand := filepath.Join(real, filepath.Base(c))
	if !inAny(roots, cand) {
		return p, false
	}
	return cand, true
}

func inAny(roots []string, p string) bool {
	for _, r := range roots {
		if r != "" && pathid.Within(r, p) {
			return true
		}
	}
	return false
}

// resolveExistingParent resolves dir's links on its deepest existing
// ancestor and re-attaches the part that does not exist yet (which can
// hold no link). A directory that cannot be resolved is not one.
func resolveExistingParent(dir string) (string, bool) {
	var tail []string
	cur := dir
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		up := filepath.Dir(cur)
		if up == cur {
			return "", false
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = up
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", false
	}
	return filepath.Join(append([]string{real}, tail...)...), true
}

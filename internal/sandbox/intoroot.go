package sandbox

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/pathid"
)

// mountTrees are the trees where a path the model names may be a share
// — external and network volumes and the automounter's /net and /home:
// resolving one before approval would touch the share, or wait on a
// stale one (ADR-0108 §1). They are judged on the real path a walk has
// reached, before anything inside is read (underMount).
var mountTrees = []string{"/Volumes", "/Network", "/net", "/home"}

// dataVolume is the firmlink spelling of the system's data volume: the
// automounter's /home is /System/Volumes/Data/home there, and a path or a
// link may name it that way.
const dataVolume = "/System/Volumes/Data"

// maxLinks bounds the links one resolution follows, as the kernel bounds
// a lookup: a loop ends as a path that does not resolve.
const maxLinks = 40

// IntoRoot rewrites p — a path typed through a link that lies outside
// every root, such as ~/Dropbox → ~/Library/CloudStorage/Dropbox — into
// the real path it names, when that lies inside one of roots (ADR-0108
// §1). roots are real paths. Only the parent is resolved: the last
// element stays as typed, so a link there is judged on its real name and
// written by replacement like any link inside a root.
//
// p comes back unchanged, with false, when it is relative, already lies
// lexically inside a root, is credential material by its spelling (the
// floors that judge the spelling keep seeing it), lies in a mount tree or
// leads into one, or does not lead into a root — the file tools then
// refuse it as they always did, saying nothing about where a link leads.
func IntoRoot(p string, roots []string) (string, bool) {
	if !filepath.IsAbs(p) {
		return p, false
	}
	c := filepath.Clean(p)
	if inAny(roots, c) || CredentialPath(c) || underMount(c) {
		return p, false
	}
	parent := filepath.Dir(c)
	if parent == c {
		return p, false
	}
	real, ok := walkReal(parent)
	if !ok {
		return p, false
	}
	cand := filepath.Join(real, filepath.Base(c))
	if inAny(roots, cand) {
		return cand, true
	}
	// The root itself named by its link (`~/Dropbox` added as a root):
	// the last element resolved too is the root, and naming a directory
	// that is a root writes through nothing.
	if whole, ok := walkReal(c); ok {
		for _, r := range roots {
			if r != "" && pathid.Same(r, whole) {
				return r, true
			}
		}
	}
	return p, false
}

// walkReal resolves the absolute path p one element at a time and
// returns its real path. Each step joins an element to a prefix that is
// already real — no link in it — so a Lstat reads that one element and
// nothing through a link, and the prefix is judged against the mount
// trees before it is read: a chain of links, a relative link (taken from
// the real directory it sits in), the data volume's spelling and a case
// variant all reach underMount as the real path the walk is at. The part
// of p that does not exist yet is joined unread; it can hold no link.
// false means p leads into a mount tree or does not resolve.
func walkReal(p string) (string, bool) {
	pending := split(p)
	real := string(filepath.Separator)
	links := 0
	for len(pending) > 0 {
		elem := pending[0]
		pending = pending[1:]
		switch elem {
		case "", ".":
			continue
		case "..":
			real = filepath.Dir(real)
			continue
		}
		next := filepath.Join(real, elem)
		if underMount(next) {
			return "", false
		}
		fi, err := os.Lstat(next)
		if os.IsNotExist(err) {
			rest := filepath.Join(append([]string{next}, pending...)...)
			if underMount(rest) {
				return "", false
			}
			return rest, true
		}
		if err != nil {
			return "", false
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			real = next
			continue
		}
		if links++; links > maxLinks {
			return "", false
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", false
		}
		if filepath.IsAbs(target) {
			real = string(filepath.Separator)
		}
		pending = append(split(target), pending...)
	}
	return real, true
}

// split cuts a path into its elements as written. It does not clean:
// in `a/../b` the `a` may be a link, and the walk must take it first.
func split(p string) []string {
	return strings.Split(p, string(filepath.Separator))
}

// underMount reports whether p lies in a mount tree, by either spelling
// of the data volume and in any case — the default volume folds case,
// and the trees are ASCII.
func underMount(p string) bool {
	for _, m := range mountTrees {
		for _, tree := range []string{m, dataVolume + m} {
			if len(p) >= len(tree) && strings.EqualFold(p[:len(tree)], tree) &&
				(len(p) == len(tree) || p[len(tree)] == filepath.Separator) {
				return true
			}
		}
	}
	return false
}

func inAny(roots []string, p string) bool {
	for _, r := range roots {
		if r != "" && pathid.Within(r, p) {
			return true
		}
	}
	return false
}

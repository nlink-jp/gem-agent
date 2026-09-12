package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// TestRuntimeEnvNamespaceIsPartitioned: every GEMAGENT_ variable the
// tree names is either one the runtime reads for itself — removed from
// every child it spawns — or one it exports for children (ADR-0087 §3).
// A new variable fails this test until it is classified.
//
// This is the closure the environment scrub could not provide. The
// scrub guessed, from a name, whether a variable was a secret, over a
// namespace nobody enumerates; the partition below is a set the
// authors write, so a later `GEMAGENT_API_KEY` never reaches a child
// without anything having to recognise the word "key".
func TestRuntimeEnvNamespaceIsPartitioned(t *testing.T) {
	own := map[string]bool{}
	for _, n := range sandbox.OwnEnvNames() {
		own[n] = true
	}
	exported := map[string]bool{}
	for _, n := range sandbox.ChildExportNames() {
		exported[n] = true
	}
	for n := range own {
		if exported[n] {
			t.Errorf("%s is in both halves of the partition", n)
		}
	}

	name := regexp.MustCompile(`^GEMAGENT_[A-Z0-9_]+$`)
	root := repoRoot(t)
	fset := token.NewFileSet()
	seen := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" || d.Name() == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v := strings.Trim(lit.Value, "`\"")
			if !name.MatchString(v) {
				return true
			}
			pos := fset.Position(lit.Pos())
			if _, ok := seen[v]; !ok {
				seen[v] = pos.Filename + ":" + itoa(pos.Line)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) == 0 {
		t.Fatal("no GEMAGENT_ variables found — the scan is broken, not the tree")
	}
	for v, where := range seen {
		if !own[v] && !exported[v] {
			t.Errorf("%s (%s) is in neither half of the GEMAGENT_ partition: add it to "+
				"sandbox.runtimeOwnEnv if the runtime reads it for itself, or to "+
				"sandbox.childExportEnv if children are meant to see it", v, where)
		}
	}
	// The reverse direction: a name in a list that the tree no longer
	// uses is a stale entry, and a stale entry is how a list stops
	// describing the program.
	for _, n := range append(sandbox.OwnEnvNames(), sandbox.ChildExportNames()...) {
		if _, ok := seen[n]; !ok {
			t.Errorf("%s is listed in the partition but named nowhere in the tree", n)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

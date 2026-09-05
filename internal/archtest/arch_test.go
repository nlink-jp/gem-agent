// Package archtest pins the structural rules of ADR-0073 §4: the
// three classes of finding that kept returning (check-then-use on a
// lexical path, unbounded I/O, permission decided in several places)
// are closed by construction here, not by review. A violation is a
// failing test naming the file, line and enclosing function.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the module root, found from this file's location.
func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller information")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// call is one call site: the package directory, the enclosing
// function, the callee as written (`os.Open`, `.CombinedOutput`) and
// the position.
type call struct {
	pkg, fn, callee string
	pos             token.Position
}

func (c call) String() string {
	return fmt.Sprintf("%s:%d %s.%s calls %s", c.pos.Filename, c.pos.Line, c.pkg, c.fn, c.callee)
}

// collectCalls parses every non-test Go file under the given
// directories (relative to the repo root) and returns the calls whose
// callee matches one of the patterns: `pkg.Func` for a package-level
// selector, `.Method` for any method call of that name.
func collectCalls(t *testing.T, root string, dirs []string, patterns []string) []call {
	fset := token.NewFileSet()
	var calls []call
	for _, dir := range dirs {
		abs := filepath.Join(root, dir)
		err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != abs && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(path))
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					fn = recvName(fd.Recv.List[0].Type) + "." + fn
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					ce, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := ce.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					callee := "." + sel.Sel.Name
					if id, ok := sel.X.(*ast.Ident); ok {
						if id.Name == "bounded" {
							return true // the primitive itself
						}
						callee = id.Name + "." + sel.Sel.Name
					}
					for _, p := range patterns {
						if callee == p || (strings.HasPrefix(p, ".") && strings.HasSuffix(callee, p)) {
							calls = append(calls, call{pkg: filepath.ToSlash(rel), fn: fn, callee: callee, pos: fset.Position(ce.Pos())})
							break
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].String() < calls[j].String() })
	return calls
}

func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvName(x.X)
	}
	return "?"
}

// report fails the test for every call not covered by the allowlist,
// and for every allowlist entry that no longer matches anything (a
// stale exemption is a hole waiting for its next use).
func report(t *testing.T, calls []call, allow map[string]string) {
	used := map[string]bool{}
	for _, c := range calls {
		key := c.pkg + " " + c.fn
		if _, ok := allow[key]; ok {
			used[key] = true
			continue
		}
		t.Errorf("%s — route it through the confined/bounded primitive, or add %q to the allowlist with the reason", c, key)
	}
	for key := range allow {
		if !used[key] {
			t.Errorf("allowlist entry %q matches nothing any more — remove it", key)
		}
	}
}

// pathPackages take paths from the model or the project. Every open,
// stat and listing in them goes through an os.Root or a handle that
// was opened through one: the confinement check and the use are one
// operation (ADR-0072 §4, four re-finds in ADR-0072 §4.1–§4.5).
var pathPackages = []string{
	"internal/tools", "internal/mention", "internal/skills", "internal/instructions",
	"internal/ignore", "internal/mediastore", "internal/docext", "internal/hooks",
}

func TestPathPackagesOpenThroughRoots(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), pathPackages, []string{
		"os.Open", "os.OpenFile", "os.ReadFile", "os.ReadDir", "os.Stat", "os.Lstat",
		"os.Create", "os.WriteFile", "os.Readlink", "os.Remove", "os.RemoveAll", "os.Rename",
	})
	report(t, calls, map[string]string{
		// Check-side only: the deepest existing ancestor is found to
		// resolve links for the containment CHECK; the open that
		// follows goes through the root, so a swap between the two is
		// refused there, not here.
		"internal/tools resolveExisting": "containment check; the use is the root open",
		// The operator's own absolute reference outside both roots
		// (`@~/Desktop/shot.png`): operator input, no confinement to
		// enforce (ADR-0072 §4.4 recorded).
		"internal/mention resolveImagePath": "operator-typed absolute image path outside the roots",
		"internal/mention openConfined":     "operator-typed absolute path outside the roots",
		// The skills root is the configured directory itself, opened
		// to list it; each skill then holds its own os.Root.
		"internal/skills readDirBounded": "opens the configured skills root to list it",
		"internal/skills Discover":       "IsDir probe of a skills-root entry before the skill's own os.Root is opened",
		// The default FileReader for callers without a root (tests, the
		// git cross-check); the file tools inject a root-backed reader.
		"internal/ignore osReader": "default reader for root-less callers",
	})
}

// boundedPackages must not read, list or capture without a cap: the
// primitives live in internal/bounded and return the `more` fact.
var boundedPackages = []string{"internal", "cmd"}

func TestReadsAreBounded(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), boundedPackages, []string{
		"io.ReadAll", "os.ReadFile", "os.ReadDir", "bufio.NewScanner",
		".CombinedOutput", ".Output",
	})
	var kept []call
	for _, c := range calls {
		if c.pkg == "internal/bounded" {
			continue
		}
		kept = append(kept, c)
	}
	report(t, kept, map[string]string{})
}

// TestOneDecisionPoint: the rule tier is consulted in exactly one
// function of the agent, and every gate reads that function's result
// (ADR-0072 §1.1, §4.5, §4.9 — three re-implementations of the floors,
// each missing one).
func TestOneDecisionPoint(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), []string{"internal/agent", "cmd", "internal/tui", "internal/approve"}, []string{"risk.Classify"})
	report(t, calls, map[string]string{
		"internal/agent Agent.decide": "the one decision point",
	})
}

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEveryModelCallNamesItsSource: a retry record says which kind of
// call was retried (ADR-0116 §1), and the only thing that knows is the
// call site. Every ChatStream call outside internal/llm must therefore
// pass a context wrapped by llm.WithSource — a new model call added
// without it would write retries with an empty source, which reads as
// "the caller did not say" in a transcript and is exactly the gap the
// usage record's own source field was introduced to close (ADR-0057).
func TestEveryModelCallNamesItsSource(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	calls := 0
	for _, dir := range []string{"internal", "cmd"} {
		abs := filepath.Join(root, dir)
		err := filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// internal/llm implements ChatStream; it does not call it.
				if d.Name() == "testdata" || path == filepath.Join(root, "internal", "llm") ||
					(path != abs && strings.HasPrefix(d.Name(), ".")) {
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
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ChatStream" || len(call.Args) == 0 {
					return true
				}
				calls++
				if !isWithSource(call.Args[0]) {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d: ChatStream is called without llm.WithSource(ctx, …) as its context",
						rel, fset.Position(call.Pos()).Line)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The count guards the test itself: a rename of ChatStream would
	// leave it checking nothing.
	if calls < 8 {
		t.Fatalf("found %d ChatStream calls, want at least the 8 known ones — the search no longer sees them", calls)
	}
}

func isWithSource(arg ast.Expr) bool {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithSource" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "llm"
}

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

// walkGo parses every non-test .go file in the tree, skipping the
// directories the other architecture tests skip, and hands each file to fn.
func walkGo(t *testing.T, fn func(path string, fset *token.FileSet, f *ast.File)) {
	t.Helper()
	root := repoRoot(t)
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata" ||
				d.Name() == "dist") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		fn(p, fset, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestOnlyTheViewLayerEmitsAnImageEscape is the test ADR-0089 §6 promised
// "in the same commit" and did not carry — an independent pass found the
// sentence standing alone, which by the ADRs' own rule makes it true "as of
// today" rather than true.
//
// The rule it enforces: an image escape is built in exactly one place
// (termimg.Payload) and reaches the terminal through exactly one caller,
// the view layer. A tool, a backend or the intake that acquired the bytes
// must never be able to write one — it would bypass the row accounting the
// bottom pin rests on, and the counter would be told nothing at all.
func TestOnlyTheViewLayerEmitsAnImageEscape(t *testing.T) {
	// The only package that may call the payload builder. The intake
	// hands BYTES to the view layer (ADR-0090); it never builds an
	// escape.
	const allowed = "internal/tui"
	callers := 0
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "termimg" || sel.Sel.Name != "Payload" {
				return true
			}
			callers++
			if !strings.Contains(filepath.ToSlash(path), allowed+"/") {
				t.Errorf("%s: termimg.Payload is called outside %s — an image escape "+
					"written anywhere else bypasses the declared-row accounting "+
					"(ADR-0089 §6)", fset.Position(call.Pos()), allowed)
			}
			return true
		})
	})
	if callers == 0 {
		t.Fatal("no termimg.Payload call found — the scan is broken, not the tree")
	}
}

// TestTheImageSinkIsWiredInProduction pins the one thing this feature's
// risk is about: the screen getting connected at all. An independent pass
// once replaced the sink's arguments with nil and the entire suite stayed
// green — images silently stopped reaching the operator, and the only
// detector left was a human at a terminal that draws.
//
// The sink moved when ADR-0091 withdrew the MCP intake as the source, and
// this test moved with it: what carries it now is Registry.SetShowImage,
// called once by cmd for an interactive session. Without that call
// show_image still reads, still judges and still answers — "no screen to
// draw on" — for the rest of the runtime's life.
func TestTheImageSinkIsWiredInProduction(t *testing.T) {
	wired := 0
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SetShowImage" || len(call.Args) != 1 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.Ident); ok && lit.Name == "nil" {
				t.Errorf("%s: the screen is wired with nil in production code — "+
					"show_image would answer 'no screen' for ever and nothing "+
					"else would notice (ADR-0091 §2)", fset.Position(call.Pos()))
				return true
			}
			wired++
			return true
		})
	})
	if wired == 0 {
		t.Fatal("no production call to SetShowImage — the model's route to the " +
			"operator's screen is not connected (ADR-0091 §2)")
	}
}

package tools

// The file tools' opens do not wait (gem-agent ADR-0072 §4.8, kept by
// ADR-0123 §4): a FIFO at a name must not hold a tool call. A walk run
// by goroutines the call waits for (ADR-0125 §2) would otherwise hold
// past an interrupt, as the first version of ADR-0125 did with
// OpenRoot. This test is the list of entrances: every open-like call in
// this package's code is one of three shapes, or the test names it.
//
//   - inside a function literal passed to openRegular (O_NONBLOCK, a
//     regular file or a directory only);
//   - an exclusive create (O_EXCL never opens what is already there);
//   - in a function listed below, with the reason it cannot wait.
//
// A new open is not wrong because it is new; it is added here with its
// reason, where a reviewer reads it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var openCalls = map[string]bool{"Open": true, "OpenFile": true, "OpenRoot": true, "ReadFile": true}

// opensThatCannotWait lists functions whose opens are neither through
// openRegular nor exclusive creates, each with why it cannot wait.
var opensThatCannotWait = map[string]string{
	"New":         "a session root: os.OpenRoot of the resolved project directory, once (ADR-0123 §4)",
	"AddDirs":     "a session root: os.OpenRoot of a directory the operator added, once (ADR-0123 §4)",
	"UseWorkDir":  "a session root: os.OpenRoot of the session's work directory, once (ADR-0123 §4)",
	"readDirOf":   "opens \".\" of a directory already held as a root: the directory itself",
	"openDirRoot": "OpenRoot of name+\"/.\": resolved as a directory, anything else refused at once (ADR-0125 §1)",
}

func TestFileToolOpensDoNotWait(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return false
				}
				stack = append(stack, n)
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !openCalls[sel.Sel.Name] {
					return true
				}
				switch {
				case insideOpenRegular(stack):
				case mentions(call, "O_EXCL"):
				case opensThatCannotWait[fn.Name.Name] != "":
					seen[fn.Name.Name] = true
				default:
					t.Errorf("%s: %s in %s may wait on a FIFO: open it through openRegular or openDirRoot, or list %s in opensThatCannotWait with the reason it cannot wait",
						fset.Position(call.Pos()), sel.Sel.Name, fn.Name.Name, fn.Name.Name)
				}
				return true
			})
		}
	}
	for name := range opensThatCannotWait {
		if !seen[name] {
			t.Errorf("opensThatCannotWait lists %s, which opens nothing any more: take it out", name)
		}
	}
}

// insideOpenRegular reports whether the innermost node of stack lies in
// a function literal passed to openRegular.
func insideOpenRegular(stack []ast.Node) bool {
	for i := len(stack) - 1; i > 0; i-- {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		if call, ok := stack[i-1].(*ast.CallExpr); ok {
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "openRegular" {
				for _, a := range call.Args {
					if a == lit {
						return true
					}
				}
			}
		}
	}
	return false
}

func mentions(n ast.Node, ident string) bool {
	found := false
	ast.Inspect(n, func(m ast.Node) bool {
		if s, ok := m.(*ast.SelectorExpr); ok && s.Sel.Name == ident {
			found = true
		}
		if id, ok := m.(*ast.Ident); ok && id.Name == ident {
			found = true
		}
		return !found
	})
	return found
}

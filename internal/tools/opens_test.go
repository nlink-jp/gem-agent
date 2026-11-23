package tools

// The file tools' opens do not wait (gem-agent ADR-0072 §4.8, kept by
// ADR-0123 §4): a FIFO at a name must not hold a tool call. A walk run
// by goroutines the call waits for (ADR-0125 §2) would otherwise hold
// past an interrupt, as the first version of ADR-0125 did with
// OpenRoot. This test is the list of entrances, over this package and
// internal/ignore, which the walk calls: every reference to an
// open-like function or method — a call, or a method value taken to
// call later — is one of these shapes, or the test names it.
//
//   - a call inside a function literal passed to openRegular that
//     passes on the literal's flag parameter (O_NONBLOCK, a regular
//     file or a directory only);
//   - a call whose arguments say O_NONBLOCK;
//   - an exclusive create (O_EXCL never opens what is already there);
//   - in a function listed below by file and name, with the reason it
//     cannot wait.
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

// opensThatCannotWait lists, by file and function, the opens that are
// none of the shapes above, each with why it cannot wait.
var opensThatCannotWait = map[string]string{
	"tools.go:New":         "a session root: os.OpenRoot of the resolved project directory, once (ADR-0123 §4)",
	"tools.go:AddDirs":     "a session root: os.OpenRoot of a directory the operator added, once (ADR-0123 §4)",
	"tools.go:UseWorkDir":  "a session root: os.OpenRoot of the session's work directory, once (ADR-0123 §4)",
	"tools.go:readDirOf":   "opens \".\" of a directory already held as a root: the directory itself",
	"tools.go:openDirRoot": "OpenRoot of name+\"/.\": resolved as a directory, anything else refused at once (ADR-0125 §1)",
}

func TestFileToolOpensDoNotWait(t *testing.T) {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, dir := range []string{".", filepath.Join("..", "ignore")} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			src, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			f, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				t.Fatal(err)
			}
			checkOpens(t, fset, f, filepath.Base(path), seen)
		}
	}
	for key := range opensThatCannotWait {
		if !seen[key] {
			t.Errorf("opensThatCannotWait lists %s, which opens nothing any more: take it out", key)
		}
	}
}

// checkOpens walks the whole file — package-level declarations
// included — and judges every selector that names an open.
func checkOpens(t *testing.T, fset *token.FileSet, f *ast.File, file string, seen map[string]bool) {
	t.Helper()
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || !openCalls[sel.Sel.Name] {
			return true
		}
		fn := enclosingFunc(stack)
		key := file + ":" + fn
		// The call the selector is the function of; nil for a method
		// value, which is judged only by the list.
		var call *ast.CallExpr
		if len(stack) >= 2 {
			if c, ok := stack[len(stack)-2].(*ast.CallExpr); ok && c.Fun == sel {
				call = c
			}
		}
		switch {
		case call != nil && insideOpenRegular(stack, call):
		case call != nil && mentions(call, "O_NONBLOCK"):
		case call != nil && mentions(call, "O_EXCL"):
		case opensThatCannotWait[key] != "":
			seen[key] = true
		default:
			t.Errorf("%s: %s in %s may wait on a FIFO: open it through openRegular or openDirRoot, or list %q in opensThatCannotWait with the reason it cannot wait",
				fset.Position(sel.Pos()), sel.Sel.Name, fn, key)
		}
		return true
	})
}

func enclosingFunc(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fd.Name.Name
		}
	}
	return "(package level)"
}

// insideOpenRegular reports whether call lies in a function literal
// passed to openRegular, and passes on that literal's flag parameter: a
// literal that ignores the flag opens the way it likes.
func insideOpenRegular(stack []ast.Node, call *ast.CallExpr) bool {
	for i := len(stack) - 1; i > 0; i-- {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		outer, ok := stack[i-1].(*ast.CallExpr)
		if !ok {
			continue
		}
		id, ok := outer.Fun.(*ast.Ident)
		if !ok || id.Name != "openRegular" || len(outer.Args) != 1 || outer.Args[0] != lit {
			continue
		}
		params := lit.Type.Params.List
		if len(params) != 1 || len(params[0].Names) != 1 {
			return false
		}
		return mentions(call, params[0].Names[0].Name)
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

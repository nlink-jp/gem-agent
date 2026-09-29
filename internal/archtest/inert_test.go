package archtest

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyTheIngressesMakeTextInert pins ADR-0093 §2: text from outside the
// runtime is made inert once, where it enters — the TUI's messages and
// callbacks, and the plain entrances' terminal streams — and nowhere
// downstream. A renderer that also sanitized would be a second mechanism,
// and the knowledge base records what that costs: in the CLI where a
// mutation check first found it, the one hid the absence of the other.
//
// Adding a caller is a design change. Say in the ADR why the ingress
// does not already cover it, then add the file here.
func TestOnlyTheIngressesMakeTextInert(t *testing.T) {
	allowed := map[string]bool{
		"internal/tui/inert.go": true, // the TUI's messages and callbacks
		"cmd/inertstreams.go":   true, // the plain REPL's and -p's streams
	}
	seen := map[string]bool{}
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		rel := filepath.ToSlash(path)
		if strings.Contains(rel, "/internal/inert/") {
			return // the package itself
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "inert" {
				return true
			}
			for file := range allowed {
				if strings.HasSuffix(rel, "/"+file) {
					seen[file] = true
					return true
				}
			}
			t.Errorf("%s: inert.%s is used outside the ingresses — sanitize where "+
				"text enters, not where it is shown (ADR-0093 §2)", fset.Position(sel.Pos()), sel.Sel.Name)
			return true
		})
	})
	for file := range allowed {
		if !seen[file] {
			t.Errorf("%s no longer uses internal/inert: an ingress was removed, "+
				"or the scan is broken", file)
		}
	}
}

// TestThePlainStreamsAreMadeInertFirst pins where runREPL wires the plain
// entrances (ADR-0093 §3): before its first print. Wired after, the lines
// before it — the startup notes, which quote MCP server output — would
// still reach a terminal as they came.
func TestThePlainStreamsAreMadeInertFirst(t *testing.T) {
	found := false
	walkGo(t, func(path string, fset *token.FileSet, f *ast.File) {
		if !strings.HasSuffix(filepath.ToSlash(path), "/cmd/root.go") {
			return
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "runREPL" {
				continue
			}
			wired, firstUse := -1, -1
			for i, st := range fn.Body.List {
				ast.Inspect(st, func(n ast.Node) bool {
					switch n := n.(type) {
					case *ast.CallExpr:
						if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "inertStreams" && wired < 0 {
							wired = i
						}
					case *ast.SelectorExpr:
						if n.Sel.Name == "ErrOrStderr" || n.Sel.Name == "OutOrStdout" {
							if firstUse < 0 && !isInertStreamsCall(st) {
								firstUse = i
							}
						}
					}
					return true
				})
			}
			found = true
			if wired < 0 {
				t.Errorf("%s: runREPL never calls inertStreams — the plain REPL and -p "+
					"write outside text to a terminal as it came (ADR-0093 §3)", fset.Position(fn.Pos()))
			} else if firstUse >= 0 && firstUse < wired {
				t.Errorf("%s: runREPL reads its streams at statement %d, before "+
					"inertStreams at %d", fset.Position(fn.Pos()), firstUse, wired)
			}
		}
	})
	if !found {
		t.Fatal("runREPL not found in cmd/root.go — the scan is broken, not the tree")
	}
}

func isInertStreamsCall(st ast.Stmt) bool {
	es, ok := st.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "inertStreams"
}

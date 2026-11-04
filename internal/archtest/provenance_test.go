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

// TestProvenanceFieldsAreSetOnce: `llm.Message.Denial`,
// `llm.Message.RuntimeNote` and `llm.Message.Saved` are the tool-message
// fields the send-time wrap trusts (ADR-0060 §3, ADR-0075 §3, ADR-0105
// §1). Trust by provenance
// holds only while the provenance is assigned in one place — the
// executor's tool-message construction in Agent.Run — so every
// composite-literal key and every field assignment naming them, in any
// non-test file under internal/ and cmd/, must be in that function.
func TestProvenanceFieldsAreSetOnce(t *testing.T) {
	root := repoRoot(t)
	fields := map[string]bool{"Denial": true, "RuntimeNote": true, "Saved": true}
	const allowed = "internal/agent Agent.Run"
	seen := map[string]int{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
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
				key := filepath.ToSlash(rel) + " " + fn
				// `Saved` is also a field of other structs (bounded's
				// HeadTailView), so its literal keys count only inside an
				// llm.Message literal; Denial and RuntimeNote stay
				// name-based, as before.
				messageKeys := map[ast.Expr]bool{}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if cl, ok := n.(*ast.CompositeLit); ok && isMessageType(cl.Type) {
						for _, e := range cl.Elts {
							if kv, ok := e.(*ast.KeyValueExpr); ok {
								messageKeys[kv.Key] = true
							}
						}
					}
					return true
				})
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					name := ""
					switch x := n.(type) {
					case *ast.KeyValueExpr:
						if id, ok := x.Key.(*ast.Ident); ok && fields[id.Name] && (id.Name != "Saved" || messageKeys[x.Key]) {
							name = id.Name
						}
					case *ast.AssignStmt:
						for _, lhs := range x.Lhs {
							if sel, ok := lhs.(*ast.SelectorExpr); ok && fields[sel.Sel.Name] {
								name = sel.Sel.Name
							}
						}
					}
					if name == "" {
						return true
					}
					seen[key]++
					if key != allowed {
						t.Errorf("%s: %s sets %s — the provenance fields are set only in %s", fset.Position(n.Pos()), key, name, allowed)
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
	// Every field, in the one function: a rule that matched nothing
	// would be a hole waiting for its first use.
	if seen[allowed] < 3 {
		t.Errorf("%s sets the provenance fields %d time(s); expected Denial, RuntimeNote and Saved", allowed, seen[allowed])
	}
}

// isMessageType reports a composite literal of llm.Message (or Message
// inside package llm).
func isMessageType(t ast.Expr) bool {
	switch x := t.(type) {
	case *ast.SelectorExpr:
		id, ok := x.X.(*ast.Ident)
		return ok && id.Name == "llm" && x.Sel.Name == "Message"
	case *ast.Ident:
		return x.Name == "Message"
	}
	return false
}

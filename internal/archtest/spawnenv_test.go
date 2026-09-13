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

// TestEverySpawnSiteAppliesTheChildEnvRule: ADR-0087 §2 says one
// function is applied at EVERY spawn site, so a child cannot be added
// without inheriting the rule. That sentence was written and not
// enforced, and three sites did not have it: the unconfined shell (which
// handed a child the runtime's own configuration, which in a sibling runtime means an API key), the
// startup lane probes, the clipboard capture and the availability
// probe. The
// partition test next door pins the two NAME lists; it cannot see a
// call site at all, which is why a false sentence survived a green
// build.
//
// The rule here: a function that builds an exec.Cmd must, in the same
// function body, name the helper that applies the environment rule.
// That is blunt — it does not prove the Env was actually assigned —
// but it puts the question in front of whoever adds the next spawn,
// which is the failure this closes.
func TestEverySpawnSiteAppliesTheChildEnvRule(t *testing.T) {
	// ChildEnv is the rule; laneEnv is the one wrapper that calls it
	// (it adds the read lane's temporary directory and the toolchain
	// caches on top), and the assertion below keeps that wrapper honest.
	applies := map[string]bool{"ChildEnv": true, "laneEnv": true}

	root := repoRoot(t)
	fset := token.NewFileSet()
	spawns := 0
	wrapperNamesTheRule := false
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
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		var anc []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				anc = anc[:len(anc)-1]
				return true
			}
			anc = append(anc, n)
			if fd, ok := n.(*ast.FuncDecl); ok && fd.Name.Name == "laneEnv" && fd.Body != nil {
				if bodyNames(fd.Body, map[string]bool{"ChildEnv": true}) {
					wrapperNamesTheRule = true
				}
			}
			if !isSpawn(n) {
				return true
			}
			spawns++
			body := enclosingBody(anc)
			if body == nil {
				t.Errorf("%s: a process is spawned outside any function body", fset.Position(n.Pos()))
				return true
			}
			if !bodyNames(body, applies) {
				t.Errorf("%s: this spawn site does not apply the child-environment rule — "+
					"set cmd.Env from sandbox.ChildEnv (ADR-0087 §2). Every child this "+
					"runtime starts is covered, probes and one-shot helpers included",
					fset.Position(n.Pos()))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if spawns == 0 {
		t.Fatal("no exec.Command call found — the scan is broken, not the tree")
	}
	if !wrapperNamesTheRule {
		t.Errorf("laneEnv is accepted as applying the rule but no longer names ChildEnv")
	}
}

// isSpawn reports an exec.Command / exec.CommandContext call.
func isSpawn(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return false
	}
	return sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"
}

// enclosingBody is the body of the innermost function around a call:
// the closure that builds the command, not the function that returns
// it, because the environment is assigned where the command is built.
func enclosingBody(anc []ast.Node) *ast.BlockStmt {
	for i := len(anc) - 1; i >= 0; i-- {
		switch fn := anc[i].(type) {
		case *ast.FuncLit:
			return fn.Body
		case *ast.FuncDecl:
			return fn.Body
		}
	}
	return nil
}

// bodyNames reports whether the body names one of the identifiers,
// selector halves included (sandbox.ChildEnv counts).
func bodyNames(body *ast.BlockStmt, want map[string]bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && want[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

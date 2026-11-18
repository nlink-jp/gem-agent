package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestTUIOptionsWiring pins the runREPL → tui.New handoff at the AST
// level. ADR-0029 shipped with the catalog resolved in cmd but never
// passed to the TUI — the field existed, the tests passed it directly,
// and the one production call site omitted it, so the entire chrome
// silently fell back to English (review round 2). A behavioral test
// cannot reach this literal without running the whole REPL, so the
// wiring itself is the thing under test: every field listed here must
// be set in the tui.Options composite literal — in frontends.go since
// ADR-0124, found wherever it is in cmd.
func TestTUIOptionsWiring(t *testing.T) {
	names, src := cmdSources(t)
	required := map[string]bool{
		"Msgs":         false, // ADR-0029: the field this test exists for
		"Theme":        false,
		"Banner":       false,
		"CompletePath": false,
		"Settings":     false,
	}
	found := 0
	for _, name := range names {
		f, err := parser.ParseFile(token.NewFileSet(), name, src[name], 0)
		if err != nil {
			t.Fatal(err)
		}
		inspectTUIOptions(f, required, &found)
	}
	if found != 1 {
		t.Fatalf("%d tui.Options literals in cmd, want the one runTUI builds", found)
	}
	for field, set := range required {
		if !set {
			t.Errorf("tui.Options does not set %s — the TUI silently loses that wiring", field)
		}
	}
}

func inspectTUIOptions(f *ast.File, required map[string]bool, found *int) {
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Options" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "tui" {
			return true
		}
		*found++
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok {
				if _, tracked := required[key.Name]; tracked {
					required[key.Name] = true
				}
			}
		}
		return true
	})
}

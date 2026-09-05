// Command labels prints every operator-facing string of gem-agent in one
// document, for the read-through that precedes a release: the ja/en UI
// catalogs side by side with their format verbs filled in, and every
// string literal the cmd package hands to a stderr note, an error, a
// flag's help or a command's description. Text scattered over forty
// files cannot be read as the operator reads it — one screen at a time,
// with no ADR at hand; collected, it can.
//
// Usage: go run ./tools/labels > dist/labels.md
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func main() {
	fmt.Println("# gem-agent operator-facing text")
	fmt.Println()
	fmt.Println("Read as the operator reads it: a fact and the next command, no design references, no reasons. Format verbs are filled with sample values.")
	fmt.Println()
	catalogs()
	literals()
}

// catalogs prints the UI catalog field by field, English then Japanese.
func catalogs() {
	en := reflect.ValueOf(*uitext.For(uitext.EN))
	ja := reflect.ValueOf(*uitext.For(uitext.JA))
	t := en.Type()
	fmt.Println("## UI catalog (internal/uitext)")
	fmt.Println()
	for i := 0; i < t.NumField(); i++ {
		if t.Field(i).Type.Kind() != reflect.String {
			continue
		}
		e, j := en.Field(i).String(), ja.Field(i).String()
		if e == "" && j == "" {
			continue
		}
		fmt.Printf("### %s\n\n", t.Field(i).Name)
		fmt.Printf("- en: %s\n", render(e))
		fmt.Printf("- ja: %s\n\n", render(j))
	}
}

// verb is one fmt directive: flags, width, precision, verb letter.
var verb = regexp.MustCompile(`%[-+# 0]*\d*(?:\.\d*)?[a-zA-Z%]`)

// render fills format verbs with sample values so a message reads as it
// will on screen. Verb count and kinds are taken from the string itself.
func render(s string) string {
	s = strings.ReplaceAll(s, "%w", "%v") // Errorf-only verb; Sprintf renders it as %v
	var args []any
	for _, m := range verb.FindAllString(s, -1) {
		switch m[len(m)-1] {
		case '%':
		case 'd':
			args = append(args, 3)
		case 'f', 'g', 'e':
			args = append(args, 12.5)
		case 'c':
			args = append(args, 'y')
		case 't':
			args = append(args, true)
		default: // s v q x ...
			args = append(args, "AGENTS.md")
		}
	}
	out := fmt.Sprintf(s, args...)
	return strconv.Quote(strings.ReplaceAll(out, "\n", "⏎"))
}

// literals prints the string literals the cmd package shows the operator:
// arguments of the printing and error functions, cobra descriptions and
// flag help. Paths are repo-relative.
func literals() {
	fmt.Println("## cmd literals (notes, errors, help)")
	fmt.Println()
	fset := token.NewFileSet()
	var lines []string
	_ = filepath.WalkDir("cmd", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := calleeName(x.Fun)
				if !printing[name] {
					return true
				}
				for _, a := range x.Args {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						add(&lines, fset, lit)
					}
				}
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok && (k.Name == "Short" || k.Name == "Long" || k.Name == "Use") {
					if lit, ok := x.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						add(&lines, fset, lit)
					}
				}
			}
			return true
		})
		return nil
	})
	sort.Strings(lines)
	for _, l := range lines {
		fmt.Println(l)
	}
}

// printing names the functions whose string arguments reach the operator.
var printing = map[string]bool{
	"Fprintf": true, "Fprintln": true, "Fprint": true, "Errorf": true, "Println": true, "Printf": true,
	"notice": true, "note": true, "warn": true,
	"StringVar": true, "StringVarP": true, "BoolVar": true, "BoolVarP": true, "IntVar": true,
	"StringSliceVar": true, "DurationVar": true,
}

func calleeName(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

func add(lines *[]string, fset *token.FileSet, lit *ast.BasicLit) {
	s, err := strconv.Unquote(lit.Value)
	if err != nil || len(s) < 16 || !strings.Contains(s, " ") {
		return
	}
	pos := fset.Position(lit.Pos())
	*lines = append(*lines, fmt.Sprintf("- `%s:%d` %s", pos.Filename, pos.Line, render(s)))
}

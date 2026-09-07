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
	return "“" + strings.ReplaceAll(out, "\n", "⏎") + "”"
}

// literals prints the string literals the cmd package shows the operator:
// arguments of the printing and error functions, cobra descriptions and
// flag help. Paths are repo-relative.
func literals() {
	fmt.Println("## cmd literals (notes, errors, help)")
	fmt.Println()
	fset := token.NewFileSet()
	var lines []string
	// cmd is where most operator text lives; internal/agent is the other
	// place that writes to the operator's screen (notices about
	// compaction, the round ladder, a remote server's repeated fault).
	var modelLines []string
	for _, root := range []string{"cmd", "internal/agent"} {
		collect(root, fset, &lines, &modelLines)
	}
	sort.Strings(modelLines)
	sort.Strings(lines)
	// A literal reached through both its print call and the variable it
	// was assembled into is one line, not two.
	prev := ""
	for _, l := range lines {
		if l == prev {
			continue
		}
		prev = l
		fmt.Println(l)
	}

	// Kept apart, not dropped: this text is written FOR THE MODEL, and
	// judging it against "a session fact plus the next command" is a
	// category error that will send the next reader to rewrite prompts
	// into operator chrome. It is printed so the document stays the
	// whole of what these packages say, which is the point of the
	// document.
	fmt.Println()
	fmt.Println("## Model-facing text — NOT judged by the operator criteria")
	fmt.Println()
	prev = ""
	for _, l := range modelLines {
		if l == prev {
			continue
		}
		prev = l
		fmt.Println(l)
	}
}

// modelFacing names the functions whose strings are written for the
// model — tool descriptions, tool results, the notes the runtime puts
// inside a function response. The split is per function, not per file:
// cmd/info.go holds renderInfo (the model's) and versionLine (/version,
// the operator's). A function added here disappears from the operator
// read-through, so add one only after reading where its string goes.
var modelFacing = map[string]bool{
	"renderInfo": true, "registerMemoryTools": true, "registerSkillTool": true,
	"expandSkillInput": true, "registerSummarizeTool": true, "registerAgenticSearch": true,
	"registerWebTools": true, "registerAskTool": true, "registerMCPTools": true,
	"wrapToolMessages": true, "runWithFloor": true, "evaluateProgress": true,
	"remoteFaultNote": true, "newMCPIntake": true, "render": true,
}

// modelFacingFiles are files with nothing but model-facing text in them.
var modelFacingFiles = map[string]bool{"cmd/mcpresult.go": true}

func collect(root string, fset *token.FileSet, lines, modelLines *[]string) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil
		}
		for _, decl := range f.Decls {
			out := lines
			if modelFacingFiles[filepath.ToSlash(p)] {
				out = modelLines
			}
			if fn, ok := decl.(*ast.FuncDecl); ok && modelFacing[fn.Name.Name] {
				out = modelLines
			}
			inspect(decl, fset, out)
		}
		return nil
	})
}

func inspect(root ast.Node, fset *token.FileSet, lines *[]string) {
	{
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := calleeName(x.Fun)
				if !printing[name] {
					return true
				}
				for _, a := range x.Args {
					addLiterals(lines, fset, a)
				}
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok && fieldNames[k.Name] {
					addLiterals(lines, fset, x.Value)
				}
			case *ast.ReturnStmt:
				// A function whose whole job is to build the operator's
				// line returns it; the four failure lines the settings
				// panel returns were invisible for exactly this reason.
				for _, r := range x.Results {
					addLiterals(lines, fset, r)
				}
			case *ast.ValueSpec:
				for _, v := range x.Values {
					addLiterals(lines, fset, v)
				}
			case *ast.AssignStmt:
				// A line the operator reads is often built into a
				// variable first and printed later. Collecting only the
				// print call missed the whole startup banner, which is
				// how four releases shipped explanatory banners with the
				// instrument that exists to prevent them (pre-release
				// review, 2026-09-08).
				for i, lhs := range x.Lhs {
					if i >= len(x.Rhs) {
						break
					}
					if id, ok := lhs.(*ast.Ident); ok && operatorVar(id.Name) {
						addLiterals(lines, fset, x.Rhs[i])
					}
				}
			}
			return true
		})
	}
}

// What this still cannot see, stated so the next reader does not mistake
// the document for the whole: string labels shorter than 16 characters
// ("session log: ", "instructions: ") are filtered as noise, and text
// assembled with a strings.Builder rather than a format call has no
// literal to collect. Both are prefixes to a value rather than sentences
// the operator has to weigh; a sentence that goes missing here is a
// defect in this tool, not in the read-through.
//
// What it does NOT distinguish, and should: roughly a fifth of what it
// collects from cmd and internal/agent is addressed to the MODEL, not
// the operator (tool results, the runtime's notes inside a function
// response). Judging those against operator criteria is a category
// error, and the split is per-function, not per-file.

// printing names the functions whose string arguments reach the operator.
// Sprintf is here because most of what an operator reads is formatted
// into a variable and printed somewhere else entirely.
var printing = map[string]bool{
	"Fprintf": true, "Fprintln": true, "Fprint": true, "Errorf": true, "Println": true, "Printf": true,
	"Sprintf": true, "notice": true, "note": true, "warn": true, "notify": true,
	"StringVar": true, "StringVarP": true, "BoolVar": true, "BoolVarP": true, "IntVar": true,
	"StringSliceVar": true, "DurationVar": true,
}

// fieldNames are struct fields whose string value the operator reads:
// cobra's help text, and the settings panel's dim note.
var fieldNames = map[string]bool{"Short": true, "Long": true, "Use": true, "Detail": true}

// operatorVar reports whether a variable name marks operator text
// assembled before it is printed. Names, not types: the banner is a
// []string built line by line, and there is no type to key on.
func operatorVar(name string) bool {
	switch name {
	case "bannerLines", "summary", "notes", "sandboxLine", "detail", "line", "scope", "msg":
		return true
	}
	return strings.HasSuffix(name, "Line") || strings.HasSuffix(name, "Note") ||
		strings.HasSuffix(name, "Notice") || strings.HasSuffix(name, "Msg")
}

// addLiterals collects the string literals inside an expression —
// including both sides of a concatenation, since "project: " + dir is
// two nodes and only the first is text the operator reads.
func addLiterals(lines *[]string, fset *token.FileSet, e ast.Expr) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			add(lines, fset, v)
		}
	case *ast.BinaryExpr:
		addLiterals(lines, fset, v.X)
		addLiterals(lines, fset, v.Y)
	case *ast.CallExpr:
		for _, a := range v.Args {
			addLiterals(lines, fset, a)
		}
	case *ast.CompositeLit:
		// The banner is a []string literal: four of the first five lines
		// an operator ever sees live in one composite.
		for _, el := range v.Elts {
			addLiterals(lines, fset, el)
		}
	}
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

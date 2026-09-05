// Package archtest pins the structural rules of ADR-0073 §4: the
// three classes of finding that kept returning (check-then-use on a
// lexical path, unbounded I/O, permission decided in several places)
// are closed by construction here, not by review. A violation is a
// failing test naming the file, line and enclosing function.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// repoRoot is the module root, found from this file's location.
func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller information")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// call is one call site: the package directory, the enclosing
// function, the callee as written (`os.Open`, `.CombinedOutput`) and
// the position.
type call struct {
	pkg, fn, callee string
	pos             token.Position
}

func (c call) String() string {
	return fmt.Sprintf("%s:%d %s.%s calls %s", c.pos.Filename, c.pos.Line, c.pkg, c.fn, c.callee)
}

// collectCalls parses every non-test Go file under the given
// directories (relative to the repo root) and returns the calls whose
// callee matches one of the patterns: `pkg.Func` for a package-level
// selector, `.Method` for any method call of that name.
func collectCalls(t *testing.T, root string, dirs []string, patterns []string) []call {
	fset := token.NewFileSet()
	var calls []call
	for _, dir := range dirs {
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
			// Imports are resolved to their real package names, so an
			// alias (`stdos "os"`) or a dot-import cannot hide a call;
			// the `bounded` exemption applies only where the file
			// imports internal/bounded under that name (review F2).
			names := map[string]string{} // local name -> canonical name
			dot := map[string]bool{}     // canonical names imported with a dot
			importsBounded := false
			for _, im := range f.Imports {
				pathv := strings.Trim(im.Path.Value, `"`)
				canon := pathv[strings.LastIndex(pathv, "/")+1:]
				if pathv == "os/exec" {
					canon = "exec"
				}
				local := canon
				if im.Name != nil {
					local = im.Name.Name
				}
				if local == "." {
					dot[canon] = true
					continue
				}
				if pathv == "github.com/nlink-jp/gem-agent/internal/bounded" && local == "bounded" {
					importsBounded = true
				}
				names[local] = canon
			}
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					fn = recvName(fd.Recv.List[0].Type) + "." + fn
				}
				record := func(callee string, pos token.Pos) {
					for _, p := range patterns {
						// `pkg.Func` patterns match a package-level
						// reference exactly; `.Method` patterns match a
						// method call by name, never `pkg.Method`.
						if callee == p || (strings.HasPrefix(p, ".") && strings.HasPrefix(callee, ".") && callee == p) {
							calls = append(calls, call{pkg: filepath.ToSlash(rel), fn: fn, callee: callee, pos: fset.Position(pos)})
							return
						}
					}
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.SelectorExpr:
						// A package-level function referenced through an
						// import, called or taken as a value
						// (`f := risk.Classify`), resolved through the
						// import table so an alias cannot hide it.
						if id, ok := x.X.(*ast.Ident); ok {
							if canon, ok := names[id.Name]; ok {
								if canon == "bounded" && importsBounded {
									return true // the primitive itself
								}
								record(canon+"."+x.Sel.Name, x.Pos())
							}
						}
					case *ast.CallExpr:
						// A method call on anything else: only the method
						// name is known (`cmd.CombinedOutput()`); a field
						// named the same (`usage.Output`) is not a call
						// and is not recorded.
						if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
							if id, ok := sel.X.(*ast.Ident); ok {
								if _, isImport := names[id.Name]; isImport {
									return true // handled as a SelectorExpr
								}
							}
							record("."+sel.Sel.Name, x.Pos())
						}
						if id, ok := x.Fun.(*ast.Ident); ok {
							// A dot-imported function used bare (`ReadAll(r)`).
							for canon := range dot {
								record(canon+"."+id.Name, x.Pos())
							}
						}
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
	sort.Slice(calls, func(i, j int) bool { return calls[i].String() < calls[j].String() })
	return calls
}

func recvName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return recvName(x.X)
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		return recvName(x.X)
	}
	return "?"
}

// report fails the test for every call not covered by the allowlist,
// and for every allowlist entry that no longer matches anything (a
// stale exemption is a hole waiting for its next use).
func report(t *testing.T, calls []call, allow map[string]string) {
	used := map[string]bool{}
	for _, c := range calls {
		key := c.pkg + " " + c.fn
		if _, ok := allow[key]; ok {
			used[key] = true
			continue
		}
		t.Errorf("%s — route it through the confined/bounded primitive, or add %q to the allowlist with the reason", c, key)
	}
	for key := range allow {
		if !used[key] {
			t.Errorf("allowlist entry %q matches nothing any more — remove it", key)
		}
	}
}

// pathPackages take paths from the model or the project. Every open,
// stat and listing in them goes through an os.Root or a handle that
// was opened through one: the confinement check and the use are one
// operation (ADR-0072 §4, four re-finds in ADR-0072 §4.1–§4.5).
var pathPackages = []string{
	"internal/tools", "internal/mention", "internal/skills", "internal/instructions",
	"internal/ignore", "internal/mediastore", "internal/docext", "internal/hooks",
}

func TestPathPackagesOpenThroughRoots(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), pathPackages, []string{
		"os.Open", "os.OpenFile", "os.ReadFile", "os.ReadDir", "os.Stat", "os.Lstat",
		"os.Create", "os.WriteFile", "os.Readlink", "os.Remove", "os.RemoveAll", "os.Rename",
	})
	report(t, calls, map[string]string{
		// Check-side only: the deepest existing ancestor is found to
		// resolve links for the containment CHECK; the open that
		// follows goes through the root, so a swap between the two is
		// refused there, not here.
		"internal/tools resolveExisting": "containment check; the use is the root open",
		// The operator's own absolute reference outside both roots
		// (`@~/Desktop/shot.png`): operator input, no confinement to
		// enforce (ADR-0072 §4.4 recorded).
		"internal/mention resolveImagePath": "operator-typed absolute image path outside the roots",
		"internal/mention openConfined":     "operator-typed absolute path outside the roots",
		// The skills root is the configured directory itself, opened
		// to list it; each skill then holds its own os.Root.
		"internal/skills readDirBounded": "opens the configured skills root to list it",
		"internal/skills Discover":       "IsDir probe of a skills-root entry before the skill's own os.Root is opened",
		// The default FileReader for callers without a root (tests, the
		// git cross-check); the file tools inject a root-backed reader.
		"internal/ignore osReader": "default reader for root-less callers",
	})
}

// boundedPackages must not read, list or capture without a cap: the
// primitives live in internal/bounded and return the `more` fact.
var boundedPackages = []string{"internal", "cmd"}

func TestReadsAreBounded(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), boundedPackages, []string{
		"io.ReadAll", "os.ReadFile", "os.ReadDir", "bufio.NewScanner",
		".CombinedOutput", ".Output",
	})
	var kept []call
	for _, c := range calls {
		if c.pkg == "internal/bounded" {
			continue
		}
		kept = append(kept, c)
	}
	report(t, kept, map[string]string{})
}

// TestOneDecisionPoint: the rule tier is consulted in exactly one
// function of the agent, and every gate reads that function's result
// (ADR-0072 §1.1, §4.5, §4.9 — three re-implementations of the floors,
// each missing one).
func TestOneDecisionPoint(t *testing.T) {
	root := repoRoot(t)
	calls := collectCalls(t, root, []string{"internal", "cmd"}, []string{"risk.Classify"})
	var kept []call
	for _, c := range calls {
		if c.pkg == "internal/risk" {
			continue // the package itself
		}
		kept = append(kept, c)
	}
	report(t, kept, map[string]string{
		"internal/agent Agent.decide": "the one decision point",
	})
	// And no package but the agent's may import the rule tier at all.
	for _, imp := range importers(t, root, []string{"internal", "cmd"}, "github.com/nlink-jp/gem-agent/internal/risk") {
		if imp != "internal/agent" && imp != "internal/risk" {
			t.Errorf("%s imports internal/risk — the rule tier is read through Agent.decide only", imp)
		}
	}
}

// TestProjectContentLoadsThroughGrant: what a project provides —
// instruction files, skills, .mcp.json, .gem-agent.toml — is read by
// exactly the cmd functions that take the projectGrant (ADR-0023 trust
// and ADR-0074 pins together). A loader called from anywhere else would
// bypass both (review of ADR-0074, B-1).
func TestProjectContentLoadsThroughGrant(t *testing.T) {
	calls := collectCalls(t, repoRoot(t), []string{"internal", "cmd"}, []string{
		"instructions.Load", "skills.Discover", "mcp.LoadConfig", "config.LoadProject",
	})
	report(t, calls, map[string]string{
		"cmd loadInstructions":  "filters the project's files by the grant",
		"cmd discoverSkills":    "filters the project's skills by the grant",
		"cmd connectMCPServers": "reads the project's .mcp.json only when the grant allows",
		"cmd loadProjectConfig": "reports whether .gem-agent.toml may loosen by the grant",
		// The ADR-0023 trust prompt counts what the project offers
		// BEFORE trust exists; the parsed servers are counted, never
		// started or registered.
		"cmd probeProject": "counts .mcp.json servers for the trust prompt; nothing is loaded",
	})
}

// importers lists the packages (repo-relative directories) whose
// non-test files import path.
func importers(t *testing.T, root string, dirs []string, path string) []string {
	fset := token.NewFileSet()
	seen := map[string]bool{}
	var out []string
	for _, dir := range dirs {
		abs := filepath.Join(root, dir)
		err := filepath.WalkDir(abs, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, im := range f.Imports {
				if strings.Trim(im.Path.Value, `"`) == path {
					rel, _ := filepath.Rel(root, filepath.Dir(p))
					rel = filepath.ToSlash(rel)
					if !seen[rel] {
						seen[rel] = true
						out = append(out, rel)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(out)
	return out
}

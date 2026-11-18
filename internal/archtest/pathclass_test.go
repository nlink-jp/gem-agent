package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The classes a call that names a file by path can have. There are
// seven, and none of them is "makes or writes something by path, or
// removes something through a path, in a place a lane writes": that is
// done through the place the session took (internal/place) or a
// directory it holds (internal/bounded), and a call that would need
// such a class has no row to go in.
//
// What the table holds, and what it does not: its key is a function,
// so a new call in a function it names passes without a word — the
// table tells a reader where to look, and a function added to the code
// must be added here. It sees a call through an import, not a field
// (exec.Cmd.Dir is not a call), and not the tools/ directory.
const (
	// entrance: where a directory is taken, or a name is resolved to
	// judge it — once, at the moment the session takes the place.
	entrance = "entrance"
	// readOnly: reads what a project, a skill or a work directory holds.
	// What is read is compared before it is trusted, or only shown.
	readOnly = "read only"
	// operatorInput: a path the operator typed in a message.
	operatorInput = "operator input"
	// operatorNamed: a file the operator named in the configuration or
	// on the command line.
	operatorNamed = "operator-named"
	// system: a fixed system path, or a program that is started.
	system = "system"
	// ownState: the runtime's own state and configuration directories,
	// which no lane writes.
	ownState = "own state"
	// removesName: removes a session's directory by its name in the
	// state tree, from the directory above it, which no lane writes.
	// What a lane left under the name goes as that name — a link as a
	// link, and nothing it leads to (measured).
	removesName = "removes a name"
)

// pathCallClasses gives every function that names a file by path, or
// starts a process, its one class.
//
// The runtime kept names for the places a lane writes — the project,
// the work directory — and used them again later: it built the sandbox
// profiles from them at every rebuild, saved results under them and
// made its own directories in them. A lane can replace what it writes
// by a link, and a name used later was then another place: the write
// lane wrote wherever the link led, with the startup checks passing
// (measured, ADR-0122). Each such use was found by a review and fixed
// where it was found, three times in one day, until every call that
// takes a path was listed and classed — 159 of them, 18 in need of a
// change, one cause. This table is that list. A call that is not on it
// fails the test until somebody says what it is.
var pathCallClasses = map[string]string{
	"cmd buildLanes":                   entrance,
	"cmd dirGranter.grant":             entrance,
	"cmd fileProbeDir":                 entrance,
	"cmd readScratchDir":               entrance,
	"cmd skillGuard.check":             entrance,
	"cmd typedStillNames":              entrance,
	"internal/place Place.InPlace":     entrance,
	"internal/place Place.OpenRoot":    entrance,
	"internal/place Take":              entrance,
	"internal/sandbox CanonicalDir":    entrance,
	"internal/sandbox RefuseRoot":      entrance,
	"internal/sandbox ResolveWriteDir": entrance,
	"internal/sandbox ScratchDirs":     entrance,
	"internal/sandbox TempDirReplaced": entrance,
	"internal/sandbox openProbes":      entrance,
	"internal/sandbox walkReal":        entrance,
	"internal/tools Registry.AddDirs":  entrance,
	"internal/tools openPlace":         entrance,
	"internal/workdir takeOwnDir":      entrance,

	"cmd addedWatch.report":                 readOnly,
	"cmd installFileChildWith":              readOnly,
	"cmd probeProject":                      readOnly,
	"cmd readDirBounded":                    readOnly,
	"internal/config LoadProject":           readOnly,
	"internal/config readCapped":            readOnly,
	"internal/ignore osReader":              readOnly,
	"internal/instructions readInstruction": readOnly,
	"internal/mcp readCapped":               readOnly,
	"internal/skills Discover":              readOnly,
	"internal/skills Skill.openRoot":        readOnly,
	"internal/skills readDirBounded":        readOnly,
	"internal/skills readSkill":             readOnly,
	"internal/tools Registry.describeFile":  readOnly,
	"internal/tools resolveExisting":        readOnly,
	"internal/trustpin Compute":             readOnly,
	"internal/trustpin Size":                readOnly,
	"internal/trustpin Snapshot":            readOnly,
	"internal/trustpin consumedDigest":      readOnly,
	"internal/trustpin dirDigest":           readOnly,
	"internal/trustpin fileDigest":          readOnly,
	"internal/trustpin readDir":             readOnly,
	"internal/workdir Remove":               removesName,
	"internal/workdir RemoveIfEmpty":        removesName,
	"internal/workdir Stat":                 readOnly,
	"internal/workdir describe":             readOnly,

	"internal/mention openConfined":     operatorInput,
	"internal/mention resolve":          operatorInput,
	"internal/mention resolveImagePath": operatorInput,

	"internal/config LoadWithOverrides": operatorNamed,
	"internal/config canonicalPath":     operatorNamed,
	"internal/telemetry loadHeaders":    operatorNamed,

	"cmd clipboardImage":               system,
	"cmd fileChildRunner":              system,
	"cmd resolveImages":                system,
	"cmd selfPath":                     system,
	"internal/hooks Runner.exec":       system,
	"internal/mcp NewStdio":            system,
	"internal/sandbox Available":       system,
	"internal/sandbox RunProbe":        system,
	"internal/sandbox resolveDenyExec": system,

	"cmd clipboardTemp":                  ownState,
	"cmd loadAddedSnapshots":             ownState,
	"cmd loadPersistentSnapshot":         ownState,
	"cmd recordedPolicy":                 ownState,
	"cmd replaceStateFile":               ownState,
	"cmd riskbookRunner.Command":         ownState,
	"internal/config LoadPolicyFile":     ownState,
	"internal/config MutatePolicyFile":   ownState,
	"internal/config PolicyFile.Save":    ownState,
	"internal/config readPolicyFile":     ownState,
	"internal/memory Delete":             ownState,
	"internal/memory Load":               ownState,
	"internal/memory Save":               ownState,
	"internal/memory readMemoryFile":     ownState,
	"internal/riskbook DeleteProject":    ownState,
	"internal/riskbook SaveProject":      ownState,
	"internal/riskbook dirExists":        ownState,
	"internal/riskbook readRulebook":     ownState,
	"internal/session InUse":             ownState,
	"internal/session Load":              ownState,
	"internal/session Open":              ownState,
	"internal/session Reopen":            ownState,
	"internal/session Scan":              ownState,
	"internal/session describe":          ownState,
	"internal/session findSessionFile":   ownState,
	"internal/session readDirCapped":     ownState,
	"internal/statedir EnsureProjectDir": ownState,
	"internal/statedir readMarker":       ownState,
	"internal/workdir List":              ownState,
}

// watchedPathCalls are the functions that take a path or start a
// process, by the package they are in.
var watchedPathCalls = map[string][]string{
	"os": {"Open", "OpenFile", "Create", "CreateTemp", "MkdirTemp", "Mkdir", "MkdirAll", "Remove", "RemoveAll",
		"Rename", "Symlink", "Link", "Readlink", "Stat", "Lstat", "ReadFile", "WriteFile", "ReadDir", "Chmod",
		"Chown", "Lchown", "Chtimes", "Truncate", "Chdir", "OpenRoot", "OpenInRoot", "DirFS", "CopyFS", "StartProcess"},
	"path/filepath": {"Walk", "WalkDir", "EvalSymlinks", "Glob"},
	"os/exec":       {"Command", "CommandContext", "LookPath"},
	"io/ioutil":     {"ReadFile", "WriteFile", "ReadDir", "TempFile", "TempDir"},
	"syscall": {"Open", "Openat", "Access", "Stat", "Lstat", "Mkfifo", "Mkdir", "Unlink", "Rmdir", "Rename",
		"Chmod", "Exec", "ForkExec", "Readlink", "Symlink", "Link", "Truncate", "Chdir"},
	"golang.org/x/sys/unix": {"Open", "Openat", "Access", "Faccessat", "Stat", "Lstat", "Fstatat", "Mkfifo", "Mkdir",
		"Mkdirat", "Unlink", "Unlinkat", "Rmdir", "Rename", "Renameat", "Chmod", "Exec", "Readlink", "Readlinkat",
		"Symlink", "Link", "Truncate", "Chdir", "Clonefile"},
	"github.com/nlink-jp/toml": {"DecodeFile"},
	"github.com/nlink-jp/gem-agent/internal/bounded": {"OpenRegularPath", "OpenRegularPathNoFollow", "OpenDir",
		"OpenRoot", "DirAt", "WriteFile", "RemoveAll"},
	"github.com/nlink-jp/gem-agent/internal/place": {"Take"},
}

// TestEveryPathCallHasItsClass holds the table to the code: every call
// of a watched function outside internal/bounded is in a function the
// table names, and the table names no function that makes no such call.
func TestEveryPathCallHasItsClass(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			pkg := filepath.ToSlash(rel)
			if pkg == "internal/bounded" {
				return nil // the primitives themselves
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			watched := map[string]map[string]bool{} // local name -> functions
			for _, im := range f.Imports {
				path := strings.Trim(im.Path.Value, `"`)
				names, ok := watchedPathCalls[path]
				if !ok {
					continue
				}
				local := path[strings.LastIndex(path, "/")+1:]
				if im.Name != nil {
					local = im.Name.Name
				}
				if local == "." || local == "_" {
					t.Errorf("%s imports %s as %q: a call through it cannot be told from a local one", p, path, local)
					continue
				}
				watched[local] = map[string]bool{}
				for _, n := range names {
					watched[local][n] = true
				}
			}
			for _, decl := range f.Decls {
				fn := "(package level)"
				if fd, ok := decl.(*ast.FuncDecl); ok {
					fn = fd.Name.Name
					if fd.Recv != nil && len(fd.Recv.List) > 0 {
						fn = recvName(fd.Recv.List[0].Type) + "." + fn
					}
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					id, ok := sel.X.(*ast.Ident)
					if !ok || !watched[id.Name][sel.Sel.Name] {
						return true
					}
					key := pkg + " " + fn
					seen[key] = true
					if _, ok := pathCallClasses[key]; !ok {
						pos := fset.Position(sel.Pos())
						t.Errorf("%s:%d %s uses %s.%s and has no class: say which of the six it is in pathCallClasses — and if it makes, writes or removes something in a place a lane writes, do that through internal/place instead (ADR-0122)",
							pos.Filename, pos.Line, key, id.Name, sel.Sel.Name)
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
	var stale []string
	for key := range pathCallClasses {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("pathCallClasses names %q, which makes no such call any more — remove the row", key)
	}
}

// nameResolvers are the functions that turn a name into the directory
// it leads to now — filepath.EvalSymlinks and the helpers built on it —
// or read the temporary directory's name. Each is here with when it
// does so: where a place is taken, where a path somebody typed is
// judged, or on a name no lane can replace.
//
// A sandbox profile anchored on a name resolved at a later build was
// anchored on whatever a lane had put under the name. That was closed
// for the project and the work directory, and the review of that fix
// found the temporary directory resolved the same way one package
// down, in a function the first form of this test did not look at: it
// looked at one package and at calls through an import. This one looks
// at every package and at calls within a package as well.
var nameResolvers = map[string]string{
	"cmd currentProject":   "the project the operator started in, once at startup",
	"cmd dirGranter.grant": "a directory the operator typed, judged before it is opened",
	"cmd fileProbeDir":     "takes the temporary directory as a place",
	"cmd readScratchDir":   "takes the temporary directory as a place",
	"cmd realHome":         "the operator's home, which no lane writes",
	"cmd realOrSelf":       "the helper itself: the kernel's name of a directory somebody named",
	"cmd rootBaseContext":  "the configuration, home and state directories, which no lane writes",
	"cmd resolveAddedDirs": "an added directory the operator named, where it is resolved to be judged",
	"cmd selfPath":         "this program's own file",
	"cmd takenOrSelf":      "a directory nobody took: nothing to ask the places for",
	"cmd typedStillNames":  "a typed path compared with the root that was opened for it",

	"internal/config canonicalPath":        "the configuration file the operator named",
	"internal/mention resolve":             "a path the operator typed in a message",
	"internal/mention resolveImagePath":    "a path the operator typed in a message",
	"internal/place Take":                  "the one place a name is resolved to be taken",
	"internal/sandbox CanonicalDir":        "the helper itself",
	"internal/sandbox RefuseRoot":          "a directory the operator asks to add, judged before it is taken",
	"internal/sandbox ResolveWriteDir":     "the helper itself",
	"internal/sandbox ScratchDirs":         "/private/tmp and /dev/fd, which are the system's; the temporary directory is taken as a place",
	"internal/sandbox TempDirReplaced":     "asks the place taken for the temporary directory",
	"internal/sandbox resolveDenyExec":     "the programs the read lane may not start",
	"internal/skills readSkill":            "a skill's directory, where it is discovered; its content is compared when it is used",
	"internal/tools Registry.describeFile": "a path the model named, to judge and describe it",
	"internal/tools openPlace":             "a directory nobody took: the file-read child, a process of its own",
	"internal/tools resolveExisting":       "a path the model named, to judge it before a root opens it",
	"internal/trustpin Compute":            "the project, to record what its files hold",
}

// TestNamesAreResolvedOnlyWhereItIsSaidWhy holds nameResolvers to the
// code, in cmd and internal, for calls through an import and calls
// within a package alike.
func TestNamesAreResolvedOnlyWhereItIsSaidWhy(t *testing.T) {
	watched := map[string]bool{"EvalSymlinks": true, "ResolveWriteDir": true, "CanonicalDir": true, "TempDir": true, "realOrSelf": true}
	root := repoRoot(t)
	fset := token.NewFileSet()
	seen := map[string]bool{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, filepath.Dir(p))
			pkg := filepath.ToSlash(rel)
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				fn := fd.Name.Name
				if fd.Recv != nil && len(fd.Recv.List) > 0 {
					fn = recvName(fd.Recv.List[0].Type) + "." + fn
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					callee := ""
					switch x := call.Fun.(type) {
					case *ast.Ident:
						callee = x.Name
					case *ast.SelectorExpr:
						callee = x.Sel.Name
					}
					if !watched[callee] {
						return true
					}
					key := pkg + " " + fn
					seen[key] = true
					if _, ok := nameResolvers[key]; !ok {
						pos := fset.Position(call.Pos())
						t.Errorf("%s:%d %s resolves a name (%s) and is not in nameResolvers: a place a lane writes is asked for from internal/place, not resolved again (ADR-0122)",
							pos.Filename, pos.Line, key, callee)
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
	var stale []string
	for key := range nameResolvers {
		if !seen[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("nameResolvers names %q, which resolves no name any more — remove the row", key)
	}
}

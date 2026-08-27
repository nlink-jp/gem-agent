package ignore

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestPatternSemantics covers each gitignore(5) construct at the
// single-file level: (lines, path, isDir) → ignored.
func TestPatternSemantics(t *testing.T) {
	cases := []struct {
		name  string
		lines string
		path  string
		isDir bool
		want  bool
	}{
		{"floating matches file at root", "frotz", "frotz", false, true},
		{"floating matches dir at depth", "frotz", "a/b/frotz", true, true},
		{"floating basename glob", "*.log", "doc/x.log", false, true},
		{"floating glob non-match", "*.log", "doc/x.txt", false, false},
		{"dir-only matches dir", "frotz/", "frotz", true, true},
		{"dir-only skips file", "frotz/", "frotz", false, false},
		{"anchored leading slash root only", "/frotz", "frotz", false, true},
		{"anchored leading slash not deep", "/frotz", "a/frotz", false, false},
		{"middle slash anchors", "doc/frotz", "doc/frotz", true, true},
		{"middle slash anchored not deep", "doc/frotz", "a/doc/frotz", true, false},
		{"anchored glob", "build/*.log", "build/x.log", false, true},
		{"star does not cross slash", "a*b", "a/b", false, false},
		{"question mark", "f?otz", "frotz", false, true},
		{"question mark one char", "f?tz", "frotz", false, false},
		{"class range", "[a-c].go", "b.go", false, true},
		{"class range non-match", "[a-c].go", "d.go", false, false},
		{"negated class", "[!a-c].go", "d.go", false, true},
		{"leading doublestar any depth", "**/foo", "a/b/foo", false, true},
		{"leading doublestar at root", "**/foo", "foo", false, true},
		{"trailing doublestar contents", "abc/**", "abc/x", false, true},
		{"trailing doublestar deep", "abc/**", "abc/x/y", false, true},
		{"trailing doublestar matches the dir itself", "abc/**", "abc", true, true},
		{"trailing doublestar not a file of that name", "abc/**", "abc", false, false},
		{"middle doublestar zero dirs", "a/**/b", "a/b", false, true},
		{"middle doublestar many dirs", "a/**/b", "a/x/y/b", false, true},
		{"inner doublestar is plain stars", "a**b", "axxb", false, true},
		{"comment is skipped", "# frotz", "# frotz", false, false},
		{"escaped hash is literal", `\#frotz`, "#frotz", false, true},
		{"escaped bang is literal", `\!frotz`, "!frotz", false, true},
		{"trailing space stripped", "frotz   ", "frotz", false, true},
		{"escaped trailing space kept", `frotz\ `, "frotz ", false, true},
		{"negation last match wins", "*.log\n!important.log", "important.log", false, false},
		{"negation order matters", "!important.log\n*.log", "important.log", false, true},
		{"multibyte in glob", "資料*.md", "資料メモ.md", false, true},
		{"unclosed class is literal", "a[b", "a[b", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &gitignoreFile{pats: parseLines(c.lines)}
			got, _ := f.match(c.path, c.isDir)
			if got != c.want {
				t.Errorf("lines %q path %q isDir=%v: got %v want %v", c.lines, c.path, c.isDir, got, c.want)
			}
		})
	}
}

// gitCrossCheck compares our verdicts against `git check-ignore` on
// the same tree — the ground truth the matcher claims to implement.
func TestGitCrossCheck(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	for _, cmd := range [][]string{
		{"git", "init", "-q"},
		{"git", "config", "core.ignorecase", "false"},
	} {
		c := exec.Command(cmd[0], cmd[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", cmd, err, out)
		}
	}
	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", strings.Join([]string{
		"*.log", "!keep.log", "/rootonly", "doc/frotz/", "bld/*.tmp",
		"**/deep", "gen/**", "a/**/b", `\#lit`, "cls[0-9]",
	}, "\n"))
	write("sub/.gitignore", "!special.log\nlocal.txt")

	paths := []struct {
		rel   string
		isDir bool
	}{
		{"x.log", false}, {"keep.log", false}, {"rootonly", false},
		{"sub/rootonly", false}, {"doc/frotz", true}, {"doc/frotz-file", false},
		{"bld/x.tmp", false}, {"bld/x.txt", false},
		{"nest/deep", false}, {"deep", false},
		{"gen", true}, {"gen/out.txt", false}, {"gen/n/out.txt", false},
		{"a/b", false}, {"a/x/b", false}, {"a/x/c", false},
		{"#lit", false}, {"cls5", false}, {"clsx", false},
		{"sub/x.log", false}, {"sub/special.log", false}, {"sub/local.txt", false},
		{"plain.txt", false},
	}

	for _, pc := range paths {
		// Ours: walk the chain the way the nav walk would.
		parts := strings.Split(pc.rel, "/")
		r := Root(dir, dir, false)
		ours := false
		for i, name := range parts {
			isDir := i < len(parts)-1 || pc.isDir
			if r.Ignored(name, isDir) {
				ours = true
				break
			}
			if isDir {
				r = r.Descend(name)
			}
		}
		// Git's verdict. check-ignore wants the path to be plausible;
		// no need for it to exist.
		arg := pc.rel
		if pc.isDir {
			arg += "/"
		}
		c := exec.Command("git", "check-ignore", "-q", arg)
		c.Dir = dir
		err := c.Run()
		theirs := err == nil
		if ours != theirs {
			t.Errorf("%s (dir=%v): ours=%v git=%v", pc.rel, pc.isDir, ours, theirs)
		}
	}
}

func TestNestedGitignorePrecedence(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(dir, "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "sub", ".gitignore"), []byte("!keep.log\n"), 0o644))

	root := Root(dir, dir, false)
	if !root.Ignored("x.log", false) {
		t.Error("root x.log should be ignored")
	}
	sub := root.Descend("sub")
	if sub.Ignored("keep.log", false) {
		t.Error("deeper !keep.log must override the shallower *.log")
	}
	if !sub.Ignored("other.log", false) {
		t.Error("sub other.log should still be ignored by the root file")
	}
}

func TestBuiltinLayerIndependent(t *testing.T) {
	dir := t.TempDir()
	root := Root(dir, dir, false)
	if !root.Ignored("node_modules", true) {
		t.Error("builtin layer should ignore node_modules without any .gitignore")
	}
	if root.Ignored("node_modules", false) {
		t.Error("builtin layer is directory-only")
	}
	// A .gitignore negation cannot re-include the builtin layer.
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("!node_modules\n"), 0o644))
	root = Root(dir, dir, false)
	if !root.Ignored("node_modules", true) {
		t.Error("a .gitignore negation must not re-include the builtin layer")
	}
}

func TestRootInsideIgnoredAreaDisablesLayers(t *testing.T) {
	dir := t.TempDir()
	inside := filepath.Join(dir, "node_modules", "pkg")
	must(t, os.MkdirAll(inside, 0o755))
	r := Root(dir, inside, false)
	if r.Note() == "" {
		t.Error("walking inside an ignored area must be noted")
	}
	if r.Ignored("dist", true) {
		t.Error("layers must be off when the walk root is inside an ignored area")
	}
	// A normal subdirectory root keeps the layers on and no note.
	sub := filepath.Join(dir, "src")
	must(t, os.MkdirAll(sub, 0o755))
	r = Root(dir, sub, false)
	if r.Note() != "" {
		t.Errorf("unexpected note: %q", r.Note())
	}
	if !r.Ignored("node_modules", true) {
		t.Error("layers should be on for a normal subdirectory root")
	}
}

func TestAncestorGitignoreAppliesToSubdirWalk(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644))
	sub := filepath.Join(dir, "src")
	must(t, os.MkdirAll(sub, 0o755))
	r := Root(dir, sub, false)
	if !r.Ignored("x.log", false) {
		t.Error("the project root .gitignore must apply to a walk rooted in a subdirectory")
	}
}

func TestOffDisablesEverything(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644))
	r := Root(dir, dir, true)
	if r.Ignored("node_modules", true) || r.Ignored("x.log", false) {
		t.Error("off must disable both layers")
	}
}

func TestSymlinkedGitignoreNotRead(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "gitignore")
	must(t, os.WriteFile(outside, []byte("*.log\n"), 0o644))
	must(t, os.Symlink(outside, filepath.Join(dir, ".gitignore")))
	r := Root(dir, dir, false)
	if r.Ignored("x.log", false) {
		t.Error("a symlinked .gitignore must not be read")
	}
}

func TestCompileInclude(t *testing.T) {
	inc, err := CompileInclude("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if !inc.MatchFile("a/b/c.go") || inc.MatchFile("a/b/c.md") {
		t.Error("*.go should select .go files at any depth")
	}
	inc, err = CompileInclude("src/**")
	if err != nil {
		t.Fatal(err)
	}
	if !inc.MatchFile("src/x/y.txt") || inc.MatchFile("lib/y.txt") {
		t.Error("src/** should select only files under src")
	}
	for _, bad := range []string{"!x", "x/", ""} {
		if _, err := CompileInclude(bad); err == nil {
			t.Errorf("CompileInclude(%q) should error", bad)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

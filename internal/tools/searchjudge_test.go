package tools

// The judge of ADR-0125: search_files is changed for speed only, so its
// output over a tree that does not change during the call must be
// byte-for-byte what v0.108.2 returned. The references in
// testdata/searchjudge were written once, at the commit before the
// change, from output that came out the same on every run. Rewrite them
// (-update) only when behaviour is meant to change.
//
// What this covers: ignore rules (built-in, nested .gitignore with
// negation, include_ignored), include and mode="files", the size,
// binary and image skips, an unreadable file, an unlistable directory,
// symlinks to files and directories, a FIFO in the tree and a FIFO or a
// regular file named as path, a directory over DirEntryCap, the
// 200-line cap reached in the middle of a directory, line anchors,
// (?i), patterns with and without a literal prefix, matches on both
// sides of line 1024, a file without a trailing newline, CRLF lines and
// non-ASCII names.
//
// What it leaves out, and what covers it: the child under the real
// profile (cmd.TestFileChildUnderTheRealProfile); cancellation
// (nav_cancel_test.go); added roots nested in the project
// (added_root_test.go); a tree that changes during the call (output is
// defined only for a tree that does not).

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

var updateSearchJudge = flag.Bool("update", false, "rewrite the search_files judge references")

// judgeRuns is how many times each scenario runs; every run must agree
// with the first before the result is compared with (or stored as) the
// reference.
const judgeRuns = 5

type judgeScenario struct {
	name string
	tree string // which generated tree
	args map[string]any
}

var judgeScenarios = []judgeScenario{
	{"literal", "main", map[string]any{"pattern": "needle", "literal": true}},
	{"literal-files", "main", map[string]any{"pattern": "needle", "literal": true, "mode": "files"}},
	{"no-match", "main", map[string]any{"pattern": "zzqqxxnotfound", "literal": true}},
	{"regex-prefix", "main", map[string]any{"pattern": `func New\w*`}},
	{"regex-no-prefix", "main", map[string]any{"pattern": `\w+Registry\(`}},
	{"case-folded", "main", map[string]any{"pattern": `(?i)NEEDLE`, "mode": "files"}},
	{"anchor-begin", "main", map[string]any{"pattern": `^func`}},
	{"anchor-end", "main", map[string]any{"pattern": `needle$`}},
	{"anchor-text-begin", "main", map[string]any{"pattern": `\Aheader`}},
	{"anchor-text-end", "main", map[string]any{"pattern": `trailer\z`}},
	{"anchor-multiline", "main", map[string]any{"pattern": `(?m)^needle`}},
	{"crlf", "main", map[string]any{"pattern": "crlf-line\r$"}},
	{"boundary-lines", "main", map[string]any{"pattern": "boundary-mark", "literal": true}},
	{"include-go", "main", map[string]any{"pattern": "needle", "include": "*.go"}},
	{"include-dir", "main", map[string]any{"pattern": "needle", "include": "src/**"}},
	{"include-ignored", "main", map[string]any{"pattern": "needle", "include_ignored": true, "mode": "files"}},
	{"path-sub", "main", map[string]any{"pattern": "needle", "path": "sub"}},
	{"path-ignored", "main", map[string]any{"pattern": "needle", "path": "node_modules"}},
	{"path-unicode", "main", map[string]any{"pattern": "needle", "path": "日本語"}},
	{"path-fifo", "main", map[string]any{"pattern": "needle", "path": "pipe"}},
	{"path-file", "main", map[string]any{"pattern": "needle", "path": "src/a.go"}},
	{"cap-content", "cap", map[string]any{"pattern": "hit", "literal": true}},
	{"cap-files", "cap", map[string]any{"pattern": "hit", "literal": true, "mode": "files"}},
	{"cap-regex", "cap", map[string]any{"pattern": `h[i]t \d+`}},
	{"wide", "wide", map[string]any{"pattern": "needle", "literal": true}},
}

func TestSearchFilesJudge(t *testing.T) {
	trees := map[string]*Registry{
		"main": judgeMainTree(t),
		"cap":  judgeCapTree(t),
		"wide": judgeWideTree(t),
	}
	// The same references hold whether one worker reads or eight
	// (ADR-0125 §2).
	defer func(n int) { searchWorkerLimit = n }(searchWorkerLimit)
	for _, workers := range []int{1, 8} {
		searchWorkerLimit = workers
		if *updateSearchJudge && workers != 1 {
			break
		}
		for _, sc := range judgeScenarios {
			judgeScenarioRun(t, trees, workers, sc)
		}
	}
}

func judgeScenarioRun(t *testing.T, trees map[string]*Registry, workers int, sc judgeScenario) {
	t.Run(fmt.Sprintf("w%d/%s", workers, sc.name), func(t *testing.T) {
		r := trees[sc.tree]
		got := judgeRun(t, r, sc.args)
		for i := 1; i < judgeRuns; i++ {
			if again := judgeRun(t, r, sc.args); again != got {
				t.Fatalf("run %d differs from run 0:\n--- run 0\n%s\n--- run %d\n%s", i, got, i, again)
			}
		}
		ref := filepath.Join("testdata", "searchjudge", sc.name+".txt")
		if *updateSearchJudge {
			if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(ref, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		want, err := os.ReadFile(ref)
		if err != nil {
			t.Fatalf("no reference (run with -update at the commit before a change): %v", err)
		}
		if got != string(want) {
			t.Errorf("output differs from the reference %s:\n--- want\n%s\n--- got\n%s", ref, want, got)
		}
	})
}

// judgeRun runs search_files and replaces the project's absolute path,
// which differs per run, with a fixed token.
func judgeRun(t *testing.T, r *Registry, args map[string]any) string {
	t.Helper()
	out, err := run(t, r, "search_files", args)
	if err != nil {
		out = "error: " + err.Error()
	}
	return strings.ReplaceAll(out, r.ProjectDir(), "<project>")
}

func judgeWrite(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func judgeMainTree(t *testing.T) *Registry {
	t.Helper()
	r := newRegistry(t)
	dir := r.ProjectDir()
	files := map[string]string{
		".gitignore":        "*.log\n!keep.log\nbuild-out/\n",
		"a.log":             "needle in an ignored log\n",
		"keep.log":          "needle in a kept log\n",
		"build-out/x.txt":   "needle in ignored output\n",
		"node_modules/m.js": "const needle = 1\n",
		"sub/.gitignore":    "secret*.txt\n!secret-ok.txt\n",
		"sub/secret-a.txt":  "needle hidden by the nested ignore\n",
		"sub/secret-ok.txt": "needle re-included by negation\n",
		"sub/deep/d.go":     "package deep\n\nfunc NewDeep() {} // needle\nneedle\n",
		"src/a.go":          "header line\npackage src\n\nfunc New() *Registry { return newRegistry() } // needle\nfunc NewOther() {}\nneedle\n",
		"src/b.go":          "package src\n\n// NEEDLE upper\n// Needle mixed\nfunc helper() {}\n",
		"src/c.txt":         "no trailing newline, ends with trailer",
		"src/crlf.txt":      "first\r\ncrlf-line\r\nneedle\r\n",
		"日本語/ファイル.txt":      "needle 日本語\n",
		"img/pic.png":       string(tinyPNG),
		"img/needle.jpg":    "needle in an image by extension\n",
		"bin/data.bin":      "needle\x00binary\n",
	}
	for i := 0; i < 8; i++ {
		files[fmt.Sprintf("many/f%02d.go", i)] = strings.Repeat("needle line\n", i+1)
	}
	for rel, content := range files {
		judgeWrite(t, dir, rel, content)
	}
	// Lines 1020–1030 carry a mark: matches on both sides of line 1024.
	var long strings.Builder
	for i := 1; i <= 1100; i++ {
		if i >= 1020 && i <= 1030 {
			fmt.Fprintf(&long, "boundary-mark %d\n", i)
		} else {
			fmt.Fprintf(&long, "filler %d\n", i)
		}
	}
	judgeWrite(t, dir, "src/long.txt", long.String())
	judgeWrite(t, dir, "big/huge.txt", "needle\n"+strings.Repeat("x", searchFileCap))
	judgeWrite(t, dir, "locked/inside.txt", "needle\n")
	judgeWrite(t, dir, "closed.txt", "needle\n")
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o755) })
	if err := os.Chmod(filepath.Join(dir, "closed.txt"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "src", "a.go"), filepath.Join(dir, "linkfile.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "src"), filepath.Join(dir, "linkdir")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "src", "stall.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

// judgeCapTree reaches the 200-line cap in the middle of a directory
// and has more matching files after it.
func judgeCapTree(t *testing.T) *Registry {
	t.Helper()
	r := newRegistry(t)
	dir := r.ProjectDir()
	for i := 0; i < 60; i++ {
		var b strings.Builder
		for j := 0; j < 4+i%4; j++ {
			fmt.Fprintf(&b, "hit %d.%d\n", i, j)
		}
		judgeWrite(t, dir, fmt.Sprintf("a/f%02d.txt", i), b.String())
	}
	for i := 0; i < 10; i++ {
		judgeWrite(t, dir, fmt.Sprintf("b/g%02d.txt", i), "hit after the cap\n")
	}
	judgeWrite(t, dir, "a/.gitignore", "*.skip\n")
	judgeWrite(t, dir, "a/zz.skip", "hit ignored\n")
	return r
}

// judgeWideTree has a directory over DirEntryCap.
func judgeWideTree(t *testing.T) *Registry {
	t.Helper()
	r := newRegistry(t)
	dir := r.ProjectDir()
	wide := filepath.Join(dir, "wide")
	if err := os.MkdirAll(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < DirEntryCap+5; i++ {
		content := "filler\n"
		if i%997 == 0 {
			content = "needle\n"
		}
		if err := os.WriteFile(filepath.Join(wide, fmt.Sprintf("w%05d.txt", i)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	judgeWrite(t, dir, "z/after.txt", "needle after the wide directory\n")
	return r
}

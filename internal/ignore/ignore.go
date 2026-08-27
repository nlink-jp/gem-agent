// Package ignore decides what the enumeration walks skip (ADR-0052).
//
// Two independent layers: a curated list of well-known dependency and
// build-output directory names (works in projects that are not git
// repositories), and full gitignore(5) semantics parsed from the
// project's own .gitignore files — nested files, negation, anchoring,
// '**', dir-only patterns, character classes, last-match-wins,
// deeper-file-precedence. The matcher is implemented here rather than
// imported (operator constraint: no new dependencies) and is
// cross-checked against `git check-ignore` in the tests.
//
// The layers filter enumeration only. Tools that take an explicit
// path never consult this package, and a walk whose root sits inside
// an ignored area runs with the layers off (Root reports it via
// Note): asking to look there is the escape.
package ignore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// builtinDirs are directory basenames skipped at any depth: dependency
// stores and build output that swamp enumeration in grown projects.
// The .gitignore layer cannot re-include these; include_ignored=true
// on the calling tool is the escape hatch, not a pattern.
var builtinDirs = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true,
	".venv": true, "venv": true, "__pycache__": true,
	".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true, ".tox": true,
	"dist": true, "build": true, "target": true, "coverage": true,
	".next": true, ".nuxt": true, ".svelte-kit": true,
	".cache": true, ".parcel-cache": true, ".turbo": true,
	".gradle": true, ".terraform": true, ".bundle": true, ".direnv": true,
	"Pods": true, "DerivedData": true,
}

// gitignoreCap bounds one .gitignore read — a sanity floor, not a
// tuning knob.
const gitignoreCap = 1 << 20

// Rules is one directory level of the ignore decision: the chain of
// parsed .gitignore files from the walk root down to this directory,
// plus the built-in layer. Values are immutable; Descend returns a
// child level.
type Rules struct {
	parent *Rules
	dir    string // absolute directory this level represents
	f      *gitignoreFile
	off    bool
	note   string
}

// Root builds the rules for a walk rooted at walkRoot inside
// projectDir (both absolute, walkRoot at or below projectDir).
// .gitignore files between the two apply, exactly as git applies
// them. If off is true the layers are disabled outright. If walkRoot
// itself is ignored by the layers, they are disabled for this walk
// and Note explains why — an explicit path into an ignored area means
// the caller wants to see it.
func Root(projectDir, walkRoot string, off bool) *Rules {
	r := &Rules{dir: projectDir, off: off}
	if !off {
		r.f = loadGitignore(projectDir)
	}
	rel, err := filepath.Rel(projectDir, walkRoot)
	if err != nil || rel == "." {
		return r
	}
	for _, name := range strings.Split(filepath.ToSlash(rel), "/") {
		if r.Ignored(name, true) {
			return &Rules{dir: walkRoot, off: true, note: fmt.Sprintf(
				"note: %s is inside an ignored area — ignore rules are off for this walk", rel)}
		}
		r = r.Descend(name)
	}
	return r
}

// Note reports the inside-an-ignored-area condition, or "".
func (r *Rules) Note() string { return r.note }

// Descend enters the named subdirectory, picking up its .gitignore.
func (r *Rules) Descend(name string) *Rules {
	child := &Rules{parent: r, dir: filepath.Join(r.dir, name), off: r.off}
	if !r.off {
		child.f = loadGitignore(child.dir)
	}
	return child
}

// Ignored decides for one entry of this level's directory. Either
// layer can ignore; a .gitignore negation cannot re-include what the
// built-in layer ignores.
func (r *Rules) Ignored(name string, isDir bool) bool {
	if r.off {
		return false
	}
	if isDir && builtinDirs[name] {
		return true
	}
	full := filepath.Join(r.dir, name)
	// Deeper .gitignore files take precedence; within one file the
	// last matching line wins (gitignore(5)).
	for lvl := r; lvl != nil; lvl = lvl.parent {
		if lvl.f == nil {
			continue
		}
		rel, err := filepath.Rel(lvl.dir, full)
		if err != nil {
			continue
		}
		if verdict, matched := lvl.f.match(filepath.ToSlash(rel), isDir); matched {
			return verdict
		}
	}
	return false
}

// gitignoreFile is one parsed .gitignore.
type gitignoreFile struct {
	pats []pattern
}

func loadGitignore(dir string) *gitignoreFile {
	path := filepath.Join(dir, ".gitignore")
	// Lstat first: a symlinked .gitignore could pull content from
	// outside the project into the decision; the walks never follow
	// links, and neither does this.
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > gitignoreCap {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	pats := parseLines(string(data))
	if len(pats) == 0 {
		return nil
	}
	return &gitignoreFile{pats: pats}
}

// match returns (ignored, matched-at-all) for a slash-separated path
// relative to this file's directory.
func (f *gitignoreFile) match(rel string, isDir bool) (bool, bool) {
	for i := len(f.pats) - 1; i >= 0; i-- {
		p := f.pats[i]
		if p.dirOnly && !isDir {
			continue
		}
		if p.match(rel, isDir) {
			return !p.negate, true
		}
	}
	return false, false
}

// pattern is one parsed .gitignore line.
type pattern struct {
	negate   bool
	dirOnly  bool
	floating bool // no slash: matches the basename at any depth
	segs     []string
}

func parseLines(data string) []pattern {
	var pats []pattern
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Trailing spaces are ignored unless backslash-escaped.
		for strings.HasSuffix(line, " ") && !strings.HasSuffix(line, `\ `) {
			line = line[:len(line)-1]
		}
		if line == "" {
			continue
		}
		var p pattern
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		p.floating = !anchored && !strings.Contains(line, "/")
		p.segs = strings.Split(line, "/")
		pats = append(pats, p)
	}
	return pats
}

func (p pattern) match(rel string, isDir bool) bool {
	if p.floating {
		base := rel
		if i := strings.LastIndexByte(rel, '/'); i >= 0 {
			base = rel[i+1:]
		}
		return matchSegment(p.segs[0], base)
	}
	return matchSegs(p.segs, strings.Split(rel, "/"), isDir)
}

// matchSegs matches pattern segments against path segments. A "**"
// segment matches zero or more directories. A trailing "/**" matches
// everything inside a directory and — measured against git, which
// appends "/" to directory paths before matching — the directory
// itself, but not a plain file of that name.
func matchSegs(pats, parts []string, isDir bool) bool {
	if len(pats) == 0 {
		return len(parts) == 0
	}
	if pats[0] == "**" {
		if len(pats) == 1 {
			return len(parts) > 0 || isDir
		}
		for k := 0; k <= len(parts); k++ {
			if matchSegs(pats[1:], parts[k:], isDir) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	return matchSegment(pats[0], parts[0]) && matchSegs(pats[1:], parts[1:], isDir)
}

// matchSegment is fnmatch over one path segment: '*' (not crossing
// '/', which segments never contain), '?', '[...]' classes with '!'
// or '^' negation and ranges, and backslash escapes. Runs on runes so
// '?' and ranges treat multibyte characters as one character.
func matchSegment(pat, s string) bool {
	p, t := []rune(pat), []rune(s)
	starP, starT := -1, 0
	i, j := 0, 0
	for j < len(t) {
		if i < len(p) {
			switch p[i] {
			case '*':
				starP, starT = i, j
				i++
				continue
			case '?':
				i++
				j++
				continue
			case '\\':
				if i+1 < len(p) && p[i+1] == t[j] {
					i += 2
					j++
					continue
				}
			case '[':
				if ok, matched, next := matchClass(p, i, t[j]); ok {
					if matched {
						i = next
						j++
						continue
					}
				} else if p[i] == t[j] { // unclosed class: literal '['
					i++
					j++
					continue
				}
			default:
				if p[i] == t[j] {
					i++
					j++
					continue
				}
			}
		}
		if starP >= 0 {
			starT++
			i, j = starP+1, starT
			continue
		}
		return false
	}
	for i < len(p) && p[i] == '*' {
		i++
	}
	return i == len(p)
}

// matchClass evaluates the character class starting at p[i]=='['
// against rune c. valid is false when the class never closes (the
// caller then treats '[' literally).
func matchClass(p []rune, i int, c rune) (valid, matched bool, next int) {
	k := i + 1
	negate := false
	if k < len(p) && (p[k] == '!' || p[k] == '^') {
		negate = true
		k++
	}
	first := true
	for k < len(p) {
		if p[k] == ']' && !first {
			return true, matched != negate, k + 1
		}
		first = false
		lo := p[k]
		if lo == '\\' && k+1 < len(p) {
			k++
			lo = p[k]
		}
		if k+2 < len(p) && p[k+1] == '-' && p[k+2] != ']' {
			hi := p[k+2]
			if lo <= c && c <= hi {
				matched = true
			}
			k += 3
			continue
		}
		if c == lo {
			matched = true
		}
		k++
	}
	return false, false, i
}

// IncludePattern is a single positive gitignore-syntax pattern used
// by search_files' include filter — same compiler, same semantics,
// anchored at the walk root.
type IncludePattern struct {
	p pattern
}

// CompileInclude parses one pattern. Negation and dir-only forms are
// errors, not silent never-matchers: include selects files.
func CompileInclude(spec string) (*IncludePattern, error) {
	if strings.HasPrefix(spec, "!") {
		return nil, fmt.Errorf("include is a single pattern, not a list — negation is not supported")
	}
	if strings.HasSuffix(spec, "/") {
		return nil, fmt.Errorf("include matches files — a trailing / would match nothing")
	}
	pats := parseLines(spec)
	if len(pats) != 1 {
		return nil, fmt.Errorf("include must be one gitignore-style pattern (e.g. \"*.go\" or \"src/**\")")
	}
	return &IncludePattern{p: pats[0]}, nil
}

// MatchFile reports whether a slash-separated walk-root-relative file
// path is selected.
func (ip *IncludePattern) MatchFile(rel string) bool {
	return ip.p.match(rel, false)
}

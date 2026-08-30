package tools

// Project navigation tools (ADR-0013, reshaped by ADR-0052): a tree
// listing and a fast grep. Both are read-only, project-confined, and
// dependency-free — a backup tool must not acquire a ripgrep
// prerequisite on the day it is needed. Neither follows symlinks: a
// walk that follows links can leave the project through a link the
// per-path checks never see.
//
// Both walks are ignore-aware (internal/ignore): well-known
// dependency/build directories and .gitignore'd entries are skipped
// during enumeration — measured 99.3% of a real Tauri project — and
// every skip is reported, never silent. Explicit paths are untouched:
// ignoring filters discovery, not access.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/ignore"
)

const (
	// treeEntryCap bounds one list_tree result. Hitting it is reported,
	// with the way to see more (narrow to a subdirectory).
	treeEntryCap = 800
	// treePerDirCap bounds entries shown per directory, so one huge
	// directory cannot starve every sibling that sorts after it
	// (ADR-0052). The remainder is reported, never silent.
	treePerDirCap = 50
	// treeDepthDefault/Max bound recursion. Depth 0 means "default".
	treeDepthDefault = 12
	treeDepthMax     = 32

	// searchMatchCap bounds shown result lines; hitting it is
	// reported, never silent.
	searchMatchCap = 200
	// searchPerFileCap bounds match lines shown per file; the rest of
	// the file is still counted and the count reported (ADR-0052), so
	// a capped result carries distribution, not the alphabetical head.
	searchPerFileCap = 5
	// searchFileCap skips files larger than this — grep on a huge
	// artifact is noise at context prices.
	searchFileCap = 2 * 1024 * 1024
	// searchLineClip bounds one matched line in the output.
	searchLineClip = 240
	// binarySniff is how many leading bytes decide text vs binary.
	binarySniff = 8192
)

// vcsDirs are skipped entirely by both tools — plumbing, not project
// content. This is stated in the tool descriptions rather than done
// silently.
var vcsDirs = map[string]bool{".git": true, ".hg": true, ".svn": true}

// ignoreTally aggregates what a walk skipped, for the honesty footer
// (ADR-0052: every skip is reported).
type ignoreTally struct {
	dirs, files int
	names       []string
}

func (t *ignoreTally) dir(name string) {
	t.dirs++
	for _, n := range t.names {
		if n == name {
			return
		}
	}
	if len(t.names) < 5 {
		t.names = append(t.names, name)
	}
}

func (t *ignoreTally) summary() string {
	if t.dirs == 0 && t.files == 0 {
		return ""
	}
	var parts []string
	if t.dirs > 0 {
		parts = append(parts, fmt.Sprintf("%d dirs (%s)", t.dirs, strings.Join(t.names, ", ")))
	}
	if t.files > 0 {
		parts = append(parts, fmt.Sprintf("%d files", t.files))
	}
	return fmt.Sprintf("[ignored: %s — pass include_ignored=true to include them]", strings.Join(parts, ", "))
}

func (r *Registry) listTree() *Tool {
	return &Tool{
		Name: "list_tree",
		Description: "List the project as an indented tree (directories end with /), recursively from " +
			"an optional subdirectory. Dependency and build directories (node_modules, vendor, dist, " +
			"target, …) and .gitignore'd entries are skipped — ignored directories still appear, " +
			"marked [ignored], and every skip is reported; pass include_ignored=true to include them. " +
			"VCS internals (.git and friends) are skipped; symlinks are shown but not followed. Big " +
			"directories are elided at a reported per-directory cap. To orient in a large project, " +
			"start with dirs_only=true, then descend. Prefer this over repeated list_files calls.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":            map[string]any{"type": "string", "description": "subdirectory to start from (default: the project root)"},
				"depth":           map[string]any{"type": "integer", "description": fmt.Sprintf("maximum depth (default %d, max %d)", treeDepthDefault, treeDepthMax)},
				"dirs_only":       map[string]any{"type": "boolean", "description": "directories only, each annotated with its file count — a large project in one screenful"},
				"include_ignored": map[string]any{"type": "boolean", "description": "also descend into ignored directories and list .gitignore'd entries"},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			if p == "" {
				p = "."
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			depth := treeDepthDefault
			if d, ok := args["depth"].(float64); ok && d > 0 {
				depth = min(int(d), treeDepthMax)
			}
			dirsOnly, _ := args["dirs_only"].(bool)
			includeIgnored, _ := args["include_ignored"].(bool)
			rules := ignore.Root(r.projectDir, abs, includeIgnored)
			tally := &ignoreTally{}

			var b strings.Builder
			entries := 0
			truncated := ""
			var walk func(dir string, level int, rules *ignore.Rules)
			walk = func(dir string, level int, rules *ignore.Rules) {
				if truncated != "" {
					return
				}
				items, err := os.ReadDir(dir)
				if err != nil {
					fmt.Fprintf(&b, "%s[unreadable: %s]\n", strings.Repeat("  ", level), filepath.Base(dir))
					return
				}
				sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })

				// Classify first, so the per-directory elision line can
				// state how many printable entries it hides.
				type row struct {
					e       os.DirEntry
					ignored bool
				}
				var rows []row
				for _, e := range items {
					// A submodule's .git is a file, not a directory —
					// VCS plumbing is skipped by name either way.
					if vcsDirs[e.Name()] {
						continue
					}
					ignored := rules.Ignored(e.Name(), e.IsDir())
					if ignored && !e.IsDir() {
						tally.files++
						continue
					}
					if ignored {
						tally.dir(e.Name())
					}
					if dirsOnly && !e.IsDir() {
						continue
					}
					rows = append(rows, row{e, ignored})
				}

				indent := strings.Repeat("  ", level)
				for i, row := range rows {
					if entries >= treeEntryCap {
						truncated = fmt.Sprintf("[stopped at %d entries — pass a subdirectory path to see more]", treeEntryCap)
						return
					}
					if i >= treePerDirCap {
						fmt.Fprintf(&b, "%s[+%d more entries]\n", indent, len(rows)-i)
						entries++
						break
					}
					entries++
					e := row.e
					switch {
					case row.ignored:
						fmt.Fprintf(&b, "%s%s/ [ignored]\n", indent, e.Name())
					case e.Type()&os.ModeSymlink != 0:
						// Shown, never followed (ADR-0013 §3).
						fmt.Fprintf(&b, "%s%s@\n", indent, e.Name())
					case e.IsDir():
						if dirsOnly {
							fmt.Fprintf(&b, "%s%s/ (%d files)\n", indent, e.Name(), countFiles(filepath.Join(dir, e.Name())))
						} else {
							fmt.Fprintf(&b, "%s%s/\n", indent, e.Name())
						}
						if level+1 >= depth {
							fmt.Fprintf(&b, "%s  [depth limit — pass path=%q to descend]\n",
								indent, relOrDot(r.projectDir, filepath.Join(dir, e.Name())))
							continue
						}
						walk(filepath.Join(dir, e.Name()), level+1, rules.Descend(e.Name()))
					default:
						fmt.Fprintf(&b, "%s%s\n", indent, e.Name())
					}
				}
			}
			walk(abs, 0, rules)
			out := b.String()
			if out == "" {
				out = "(empty directory)"
			}
			if truncated != "" {
				out += truncated + "\n"
			}
			if s := tally.summary(); s != "" {
				out += s + "\n"
			}
			if n := rules.Note(); n != "" {
				out = n + "\n" + out
			}
			return truncate(strings.TrimRight(out, "\n"), OutputCap), nil
		},
	}
}

// countFiles counts the non-directory entries of one directory, for
// the dirs_only annotation.
func countFiles(dir string) int {
	items, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range items {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

func (r *Registry) searchFiles() *Tool {
	return &Tool{
		Name: "search_files",
		Description: "Fast text search across project files (pure grep, no index). pattern is a Go " +
			"regular expression — set literal=true to search for the exact string instead. Results " +
			"are path:line: text, at most 5 shown per file with the remainder counted. Dependency " +
			"and build directories (node_modules, vendor, dist, …) and .gitignore'd files are " +
			"skipped and reported — pass include_ignored=true to search them too. For a broad " +
			"\"where does this live\" question, start with mode=\"files\" (per-file counts only) " +
			"and narrow with include (gitignore-style file pattern, e.g. \"*.go\" or \"src/**\") " +
			"or path. Binary files, VCS internals, symlinks, and files over 2MB are skipped; caps " +
			"are reported when hit. Prefer this over reading files wholesale to locate something.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern":         map[string]any{"type": "string", "description": "Go regexp (or exact string with literal=true)"},
				"path":            map[string]any{"type": "string", "description": "subdirectory to search (default: the project root)"},
				"literal":         map[string]any{"type": "boolean", "description": "treat pattern as an exact string, not a regexp"},
				"include":         map[string]any{"type": "string", "description": "only scan files matching this gitignore-style pattern (e.g. \"*.go\", \"src/**\")"},
				"mode":            map[string]any{"type": "string", "description": "\"content\" (default: matching lines) or \"files\" (per-file match counts only)"},
				"include_ignored": map[string]any{"type": "boolean", "description": "also search ignored directories and .gitignore'd files"},
			},
			"required": []string{"pattern"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}
			if lit, _ := args["literal"].(bool); lit {
				pattern = regexp.QuoteMeta(pattern)
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("invalid pattern: %w (set literal=true to search for the exact string)", err)
			}
			mode, _ := args["mode"].(string)
			if mode != "" && mode != "content" && mode != "files" {
				return "", fmt.Errorf("unknown mode %q — valid: \"content\", \"files\"", mode)
			}
			var inc *ignore.IncludePattern
			if spec, _ := args["include"].(string); spec != "" {
				if inc, err = ignore.CompileInclude(spec); err != nil {
					return "", fmt.Errorf("invalid include: %w", err)
				}
			}
			p, _ := args["path"].(string)
			if p == "" {
				p = "."
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			includeIgnored, _ := args["include_ignored"].(bool)
			rules := ignore.Root(r.projectDir, abs, includeIgnored)
			tally := &ignoreTally{}

			var b strings.Builder
			totalMatches, filesHit, filesScanned, filteredOut, shownLines := 0, 0, 0, 0, 0
			capped := false
			var walk func(dir string, rules *ignore.Rules)
			walk = func(dir string, rules *ignore.Rules) {
				if capped {
					return
				}
				items, err := os.ReadDir(dir)
				if err != nil {
					return
				}
				sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
				for _, e := range items {
					if capped {
						return
					}
					if e.Type()&os.ModeSymlink != 0 {
						continue // never follow links out of the walk (ADR-0013 §3)
					}
					if vcsDirs[e.Name()] {
						continue // a submodule's .git is a file — skip by name either way
					}
					full := filepath.Join(dir, e.Name())
					if e.IsDir() {
						if rules.Ignored(e.Name(), true) {
							tally.dir(e.Name())
							continue
						}
						walk(full, rules.Descend(e.Name()))
						continue
					}
					if rules.Ignored(e.Name(), false) {
						tally.files++
						continue
					}
					rel := relOrDot(abs, full)
					if inc != nil && !inc.MatchFile(filepath.ToSlash(rel)) {
						filteredOut++
						continue
					}
					info, err := e.Info()
					if err != nil || info.Size() > searchFileCap || isImageExt(e.Name()) {
						continue
					}
					data, err := os.ReadFile(full)
					if err != nil {
						continue
					}
					if bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0 {
						continue // binary
					}
					filesScanned++
					display := relOrDot(r.projectDir, full)
					var shown []string
					count := 0
					for i, line := range strings.Split(string(data), "\n") {
						if !re.MatchString(line) {
							continue
						}
						count++
						if len(shown) < searchPerFileCap {
							shown = append(shown, fmt.Sprintf("%s:%d: %s",
								display, i+1, clipRunes(strings.TrimSpace(line), searchLineClip)))
						}
					}
					if count == 0 {
						continue
					}
					filesHit++
					totalMatches += count
					if mode == "files" {
						fmt.Fprintf(&b, "%s (%d matches)\n", display, count)
						shownLines++
					} else {
						for _, l := range shown {
							b.WriteString(l + "\n")
						}
						shownLines += len(shown)
						if count > len(shown) {
							fmt.Fprintf(&b, "  … +%d more in this file\n", count-len(shown))
						}
					}
					if shownLines >= searchMatchCap {
						capped = true
					}
				}
			}
			walk(abs, rules)

			extra := ""
			if filteredOut > 0 {
				extra = fmt.Sprintf(", %d filtered by include", filteredOut)
			}
			var out string
			if totalMatches == 0 {
				out = fmt.Sprintf("no matches (%d files scanned%s)", filesScanned, extra)
			} else {
				out = b.String()
				out += fmt.Sprintf("\n%d matches in %d files (%d scanned%s)", totalMatches, filesHit, filesScanned, extra)
				if capped {
					out += fmt.Sprintf(" — stopped at the %d-line cap; narrow with path or include, or use mode=\"files\"", searchMatchCap)
				}
			}
			if s := tally.summary(); s != "" {
				out += "\n" + s
			}
			if n := rules.Note(); n != "" {
				out = n + "\n" + out
			}
			return truncate(out, OutputCap), nil
		},
	}
}

// relOrDot renders a path relative to the project for display. A path
// outside the project — the session work directory, listed by its
// absolute path — is shown absolute rather than as a climb through
// "../..", which reads as an escape and is not one.
func relOrDot(projectDir, p string) string {
	rel, err := filepath.Rel(projectDir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		return p
	}
	return rel
}

func clipRunes(s string, limit int) string {
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

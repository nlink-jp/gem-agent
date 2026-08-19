package tools

// Project navigation tools (ADR-0013): a tree listing and a fast grep.
// Both are read-only, project-confined, and dependency-free — a backup
// tool must not acquire a ripgrep prerequisite on the day it is needed.
// Neither follows symlinks: a walk that follows links can leave the
// project through a link the per-path checks never see.

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	// treeEntryCap bounds one list_tree result. Hitting it is reported,
	// with the way to see more (narrow to a subdirectory).
	treeEntryCap = 800
	// treeDepthDefault/Max bound recursion. Depth 0 means "default".
	treeDepthDefault = 12
	treeDepthMax     = 32

	// searchMatchCap bounds one search_files result; hitting it is
	// reported, never silent.
	searchMatchCap = 200
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

func (r *Registry) listTree() *Tool {
	return &Tool{
		Name: "list_tree",
		Description: "List the project as an indented tree (directories end with /), recursively from " +
			"an optional subdirectory. VCS internals (.git and friends) are skipped; symlinks are " +
			"shown but not followed. Large trees are cut at a reported cap — pass a subdirectory " +
			"path to see more. Prefer this over repeated list_files calls to orient in a project.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string", "description": "subdirectory to start from (default: the project root)"},
				"depth": map[string]any{"type": "integer", "description": fmt.Sprintf("maximum depth (default %d, max %d)", treeDepthDefault, treeDepthMax)},
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

			var b strings.Builder
			entries := 0
			truncated := ""
			var walk func(dir string, level int)
			walk = func(dir string, level int) {
				if truncated != "" {
					return
				}
				items, err := os.ReadDir(dir)
				if err != nil {
					fmt.Fprintf(&b, "%s[unreadable: %s]\n", strings.Repeat("  ", level), filepath.Base(dir))
					return
				}
				sort.Slice(items, func(i, j int) bool { return items[i].Name() < items[j].Name() })
				for _, e := range items {
					if e.IsDir() && vcsDirs[e.Name()] {
						continue
					}
					if entries >= treeEntryCap {
						truncated = fmt.Sprintf("[stopped at %d entries — pass a subdirectory path to see more]", treeEntryCap)
						return
					}
					entries++
					indent := strings.Repeat("  ", level)
					switch {
					case e.Type()&os.ModeSymlink != 0:
						// Shown, never followed (ADR-0013 §3).
						fmt.Fprintf(&b, "%s%s@\n", indent, e.Name())
					case e.IsDir():
						fmt.Fprintf(&b, "%s%s/\n", indent, e.Name())
						if level+1 >= depth {
							fmt.Fprintf(&b, "%s  [depth limit — pass path=%q to descend]\n",
								indent, relOrDot(r.projectDir, filepath.Join(dir, e.Name())))
							continue
						}
						walk(filepath.Join(dir, e.Name()), level+1)
					default:
						fmt.Fprintf(&b, "%s%s\n", indent, e.Name())
					}
				}
			}
			walk(abs, 0)
			out := b.String()
			if out == "" {
				out = "(empty directory)"
			}
			if truncated != "" {
				out += truncated + "\n"
			}
			return truncate(strings.TrimRight(out, "\n"), outputCap), nil
		},
	}
}

func (r *Registry) searchFiles() *Tool {
	return &Tool{
		Name: "search_files",
		Description: "Fast text search across project files (pure grep, no index). pattern is a Go " +
			"regular expression — set literal=true to search for the exact string instead. Results " +
			"are path:line: text. Binary files, VCS internals, symlinks, and files over 2MB are " +
			"skipped; the match cap is reported when hit. Prefer this over reading files wholesale " +
			"to locate something.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{"type": "string", "description": "Go regexp (or exact string with literal=true)"},
				"path":    map[string]any{"type": "string", "description": "subdirectory to search (default: the project root)"},
				"literal": map[string]any{"type": "boolean", "description": "treat pattern as an exact string, not a regexp"},
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
			p, _ := args["path"].(string)
			if p == "" {
				p = "."
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}

			var b strings.Builder
			matches, filesHit, filesScanned := 0, 0, 0
			capped := false
			var walk func(dir string)
			walk = func(dir string) {
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
					full := filepath.Join(dir, e.Name())
					if e.IsDir() {
						if !vcsDirs[e.Name()] {
							walk(full)
						}
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
					fileMatched := false
					for i, line := range strings.Split(string(data), "\n") {
						if !re.MatchString(line) {
							continue
						}
						fileMatched = true
						matches++
						fmt.Fprintf(&b, "%s:%d: %s\n",
							relOrDot(r.projectDir, full), i+1, clipRunes(strings.TrimSpace(line), searchLineClip))
						if matches >= searchMatchCap {
							capped = true
							break
						}
					}
					if fileMatched {
						filesHit++
					}
				}
			}
			walk(abs)

			if matches == 0 {
				return fmt.Sprintf("no matches (%d files scanned)", filesScanned), nil
			}
			summary := fmt.Sprintf("\n%d matches in %d files (%d scanned)", matches, filesHit, filesScanned)
			if capped {
				summary += fmt.Sprintf(" — stopped at the %d-match cap; narrow the pattern or the path", searchMatchCap)
			}
			return truncate(b.String()+summary, outputCap), nil
		},
	}
}

// relOrDot renders a path relative to the project for display.
func relOrDot(projectDir, p string) string {
	rel, err := filepath.Rel(projectDir, p)
	if err != nil {
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

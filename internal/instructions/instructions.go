// Package instructions collects the project's agent-instruction files —
// the convention every coding agent follows, so a repository set up for
// one works with gem-agent unchanged.
//
// Two conventions are honoured: the file names (AGENTS.md and the
// vendor variants) and the ancestor walk (instructions in a parent
// directory apply to everything beneath it, which is how workspace-wide
// rules are shared across sibling repositories).
package instructions

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Names are the instruction files read from each directory, in the
// order they are injected. AGENTS.md is the cross-vendor standard;
// AGENT.md is its singular variant; CLAUDE.md and GEMINI.md are the
// Claude Code and Gemini CLI conventions.
var Names = []string{"AGENTS.md", "AGENT.md", "CLAUDE.md", "GEMINI.md"}

// File is one loaded instruction file.
type File struct {
	Path    string // absolute
	Label   string // what the prompt and the banner show
	Content string
}

// Limits bound the injection so a deep tree of instruction files cannot
// crowd out the conversation.
type Limits struct {
	PerFileBytes int
	TotalBytes   int
}

// DefaultLimits are generous per file but capped in total.
func DefaultLimits() Limits {
	return Limits{PerFileBytes: 32 * 1024, TotalBytes: 128 * 1024}
}

// Load collects instruction files for projectDir.
//
// Order is least specific first: the user-global file, then ancestor
// directories from the outermost inward, then the project itself — so
// the nearest rules are the last thing the model reads.
//
// The ancestor walk stops at home: an instruction file is trusted as
// instructions rather than data, so gem-agent will not pick one up from
// a shared location like /tmp that the operator does not own.
func Load(projectDir, home, globalDir string, lim Limits) ([]File, []string) {
	var files []File
	var notes []string
	total := 0
	seenPath := map[string]bool{}
	seenContent := map[string]bool{}

	add := func(path, label string) {
		if seenPath[path] {
			return
		}
		seenPath[path] = true
		// Read through an os.Root at the file's directory: an
		// instruction file enters the system prompt unwrapped, so a
		// link planted where AGENTS.md should be must not pull content
		// from outside that directory (a CLAUDE.md → AGENTS.md link
		// beside it still resolves). Capped on the stream (review
		// after v0.68.2, the load_skill class).
		data, size, err := readInstruction(path, lim.PerFileBytes)
		if err != nil {
			if !os.IsNotExist(err) {
				notes = append(notes, label+": skipped ("+err.Error()+")")
			}
			return
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			return
		}
		// The same rules reached by two names (a symlinked AGENT.md, a
		// CLAUDE.md that just includes AGENTS.md) should be injected
		// once, not twice.
		if seenContent[content] {
			return
		}
		seenContent[content] = true

		if total >= lim.TotalBytes {
			notes = append(notes, label+": skipped (instruction budget exhausted)")
			return
		}
		cap := lim.PerFileBytes
		if remaining := lim.TotalBytes - total; remaining < cap {
			cap = remaining
		}
		if len(content) > cap || size > int64(len(data)) {
			cut := cutRunes(content, cap)
			content = cut + fmt.Sprintf("\n[truncated: %d of %d bytes shown]", len(cut), size)
			notes = append(notes, label+": truncated")
		}
		total += len(content)
		files = append(files, File{Path: path, Label: label, Content: content})
	}

	// User-global instructions for gem-agent itself.
	if globalDir != "" {
		for _, name := range Names {
			add(filepath.Join(globalDir, name), "~/.config/gem-agent/"+name)
		}
	}

	for _, dir := range ancestors(projectDir, home) {
		for _, name := range Names {
			add(filepath.Join(dir, name), label(dir, projectDir, name))
		}
	}
	return files, notes
}

// ancestors returns the directories to search, outermost first, ending
// with projectDir. The walk stops when it leaves home (or immediately,
// if the project is not under home).
func ancestors(projectDir, home string) []string {
	projectDir = filepath.Clean(projectDir)
	if home == "" {
		return []string{projectDir}
	}
	home = filepath.Clean(home)

	var dirs []string
	for dir := projectDir; ; {
		dirs = append(dirs, dir)
		if dir == home || !within(home, dir) {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Collected inward-out; inject outermost first.
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// label renders a path for display: bare name in the project itself,
// otherwise relative to the project so the operator can see how far up
// the rules came from.
func label(dir, projectDir, name string) string {
	if dir == filepath.Clean(projectDir) {
		return name
	}
	if rel, err := filepath.Rel(projectDir, dir); err == nil {
		return filepath.Join(rel, name)
	}
	return filepath.Join(dir, name)
}

// Render turns loaded files into the project-context section of the
// system prompt. Returns "" when nothing was found.
func Render(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nProject instructions (the project's own agent-instruction files — follow them as project-specific guidance; the later ones are closer to this project and take precedence):\n")
	for _, f := range files {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n", f.Label, f.Content)
	}
	return b.String()
}

// Labels lists what was loaded, for the startup banner.
func Labels(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Label)
	}
	return out
}

// cutRunes truncates s to at most n bytes without splitting a UTF-8
// sequence (review round 4).
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// readInstruction reads path through an os.Root at its directory, at
// most cap bytes, and reports the file's full size. A link that leads
// out of the directory is refused by the root at the open.
func readInstruction(path string, cap int) ([]byte, int64, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		if strings.Contains(err.Error(), "escapes") {
			return nil, 0, fmt.Errorf("a link to outside its directory — not read")
		}
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(cap)))
	if err != nil {
		return nil, 0, err
	}
	return data, size, nil
}

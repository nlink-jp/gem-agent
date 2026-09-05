// Package trustpin keys project trust on content, not on a path
// (ADR-0074). A trusted directory stays a name; what gem-agent consumes
// from it — the instruction files, .mcp.json, .gem-agent.toml, the
// project skills — is content that a `git pull`, a link, or a renamed
// parent directory can change without the name changing. Pins are
// SHA-256 digests of exactly those files, recorded when the operator
// trusts the project and compared on every load; a changed file is
// loaded only after the operator has seen that it changed.
//
// The same package takes the wider snapshot of every persistent file
// under the project (the write lane's protected names, nested
// repositories' hooks included) so a session can report what it added
// or changed — the residue ADR-0073 §6 recorded, made visible where it
// cannot be enforced.
package trustpin

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/instructions"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// Pins maps a consumed file's project-relative name to "sha256:<hex>".
// Skills are pinned as a directory: the key is `.claude/skills/<name>`
// and the digest covers every file under it.
type Pins map[string]string

// Caps: one consumed file, the files of one skill, the entries of one
// directory, and the whole persistent-file walk.
const (
	FileCap     = 1 << 20
	SkillFiles  = 2000
	DirEntries  = 2000
	WalkEntries = 20000
)

// SkillsDir is the project skills directory (Claude Code's layout,
// ADR-0011).
const SkillsDir = ".claude/skills"

// ConfigNames are the runtime's own project configuration files.
var ConfigNames = []string{".mcp.json", ".gem-agent.toml"}

// Compute digests what gem-agent would consume from projectDir. Files
// that do not exist are absent from the result. notes report caps that
// were reached — a pin over a cut listing is still a pin, and says so.
func Compute(projectDir string) (Pins, []string) {
	pins := Pins{}
	var notes []string
	for _, name := range append(append([]string{}, instructions.Names...), ConfigNames...) {
		if d, ok := consumedDigest(projectDir, name); ok {
			pins[name] = d
		}
	}
	skillsRoot := filepath.Join(projectDir, SkillsDir)
	entries, more, err := readDir(skillsRoot, DirEntries)
	if err == nil {
		if more {
			notes = append(notes, fmt.Sprintf("%s: more than %d entries; only the first were pinned", SkillsDir, DirEntries))
		}
		for _, e := range entries {
			dir := filepath.Join(skillsRoot, e.Name())
			// os.Stat, as skills.Discover does: a symlinked skill
			// directory reports IsDir()=false on the DirEntry.
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				continue
			}
			// A shared skill is a symlinked directory (ADR-0011 §3):
			// the walk starts from the resolved target, as Discover
			// reads it. Links inside are recorded by target, not followed.
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			d, cut := dirDigest(dir, SkillFiles)
			if cut {
				notes = append(notes, fmt.Sprintf("%s/%s: more than %d files; the pin covers the first", SkillsDir, e.Name(), SkillFiles))
			}
			pins[SkillsDir+"/"+e.Name()] = d
		}
	}
	return pins, notes
}

// Change is one difference between recorded and current pins.
type Change struct {
	Name string
	// Kind is "added" (no pin recorded), "changed" or "removed".
	Kind string
}

// Diff compares recorded pins with current ones. Names are sorted.
func Diff(recorded, current Pins) []Change {
	var out []Change
	for name, cur := range current {
		switch rec, ok := recorded[name]; {
		case !ok:
			out = append(out, Change{name, "added"})
		case rec != cur:
			out = append(out, Change{name, "changed"})
		}
	}
	for name := range recorded {
		if _, ok := current[name]; !ok {
			out = append(out, Change{name, "removed"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Snapshot digests every persistent file under projectDir — the names
// sandbox.PersistentFile protects, at any depth — without following
// symlinks. cut reports that the walk stopped at WalkEntries. Keys are
// project-relative, slash-separated.
func Snapshot(projectDir string) (map[string]string, bool) {
	snap := map[string]string{}
	seen := 0
	cut := false
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable: skip, keep walking
		}
		if cut {
			return fs.SkipAll
		}
		seen++
		if seen > WalkEntries {
			cut = true
			return fs.SkipAll
		}
		if path == projectDir {
			return nil
		}
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.Type()&fs.ModeSymlink != 0 {
			// A link is recorded by its target string: swapping where it
			// points is a change, and it is never followed.
			if sandbox.PersistentFile(rel) {
				if target, err := os.Readlink(path); err == nil {
					snap[rel] = "link:" + target
				}
			}
			return nil
		}
		if d.IsDir() {
			// Whole trees that are persistent (.claude, .git/hooks) are
			// walked file by file; other directories are entered.
			return nil
		}
		if sandbox.PersistentFile(rel) {
			if dg, ok := fileDigest(path); ok {
				snap[rel] = dg
			}
		}
		return nil
	})
	return snap, cut
}

// SnapshotDiff lists the keys added, changed and removed between two
// snapshots, each sorted.
func SnapshotDiff(before, after map[string]string) (added, changed, removed []string) {
	for k, v := range after {
		if b, ok := before[k]; !ok {
			added = append(added, k)
		} else if b != v {
			changed = append(changed, k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)
	return added, changed, removed
}

// Parents returns the distinct absolute parent directories of the
// snapshot's files, deepest first, excluding projectDir itself — the
// directories whose rename would replace a persistent file's content
// under an unchanged name (ADR-0074 §2). Every ancestor between the
// file and the project root is included.
func Parents(projectDir string, snap map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for rel := range snap {
		dir := filepath.Dir(filepath.FromSlash(rel))
		for dir != "." && dir != "" && dir != string(filepath.Separator) {
			abs := filepath.Join(projectDir, dir)
			if !seen[abs] {
				seen[abs] = true
				out = append(out, abs)
			}
			dir = filepath.Dir(dir)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// consumedDigest hashes a consumed file the way its loader reads it:
// through an os.Root at the project directory, so a same-directory
// link resolves and a link leaving the directory is refused (absent —
// and not loaded either). A link's target string is hashed with the
// content, so retargeting it is a change even when the bytes match
// (review F1: a link was "absent" to the pin and present to the loader).
func consumedDigest(projectDir, name string) (string, bool) {
	root, err := os.OpenRoot(projectDir)
	if err != nil {
		return "", false
	}
	defer func() { _ = root.Close() }()
	st, err := root.Lstat(name)
	if err != nil {
		return "", false
	}
	prefix := ""
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(name)
		if err != nil {
			return "", false
		}
		prefix = "link:" + target + "\x00"
	} else if !st.Mode().IsRegular() {
		return "", false
	}
	f, err := root.Open(name)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	if fst, err := f.Stat(); err != nil || !fst.Mode().IsRegular() {
		return "", false
	}
	data, more, err := bounded.ReadAll(f, FileCap)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	h.Write([]byte(prefix))
	h.Write(data)
	if more {
		fmt.Fprintf(h, "\x00cut")
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), true
}

// fileDigest hashes one regular file, bounded at FileCap; a file past
// the cap is hashed as its first FileCap bytes plus a marker, so it is
// still pinned and a change past the cap still changes the size part.
func fileDigest(path string) (string, bool) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	data, more, err := bounded.ReadAll(f, FileCap)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	h.Write(data)
	if more {
		fmt.Fprintf(h, "\x00cut:%d", st.Size())
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), true
}

// dirDigest hashes a skill directory: for every regular file (sorted by
// relative path, links included by their target string) the path, the
// size and the content digest. cut reports the file cap.
func dirDigest(dir string, maxFiles int) (string, bool) {
	type entry struct{ rel, line string }
	var entries []entry
	cut := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(entries) >= maxFiles {
			cut = true
			return fs.SkipAll
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, _ := os.Readlink(path)
			entries = append(entries, entry{rel, "link:" + target})
		case d.Type().IsRegular():
			if dg, ok := fileDigest(path); ok {
				entries = append(entries, entry{rel, dg})
			}
		}
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\n", e.rel, e.line)
	}
	if cut {
		h.Write([]byte("\x00cut"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), cut
}

func readDir(dir string, n int) ([]os.DirEntry, bool, error) {
	d, err := os.Open(dir)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = d.Close() }()
	return bounded.ReadDir(d, n)
}

// Size renders the current size of a pinned name for the operator:
// "(1234 bytes)", "(directory)" for a skill, or "" when it is gone.
func Size(projectDir, name string) string {
	st, err := os.Stat(filepath.Join(projectDir, filepath.FromSlash(name)))
	if err != nil {
		return ""
	}
	if st.IsDir() {
		return "(directory)"
	}
	return fmt.Sprintf("(%d bytes)", st.Size())
}

// PinName maps a project-relative path a tool wrote to the pin it
// belongs to: a root instruction or configuration file is its own pin,
// a file inside a project skill is that skill's pin, anything else has
// none (ADR-0074 §1: re-pin only what the operator saw). Names are
// compared regardless of case — the default volume folds it — and the
// pin returned is the canonical spelling: the loader's name for a root
// file, the directory entry as listed for a skill (projectDir may be
// empty, in which case the segment is returned as written).
func PinName(projectDir, rel string) string {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, name := range append(append([]string{}, instructions.Names...), ConfigNames...) {
		if strings.EqualFold(rel, name) {
			return name
		}
	}
	if len(rel) > len(SkillsDir)+1 && strings.EqualFold(rel[:len(SkillsDir)+1], SkillsDir+"/") {
		rest := rel[len(SkillsDir)+1:]
		entry := rest
		if i := strings.IndexByte(rest, '/'); i > 0 {
			entry = rest[:i]
		}
		if entry == "" {
			return ""
		}
		if projectDir != "" {
			if entries, _, err := readDir(filepath.Join(projectDir, SkillsDir), DirEntries); err == nil {
				for _, e := range entries {
					if strings.EqualFold(e.Name(), entry) {
						entry = e.Name()
						break
					}
				}
			}
		}
		return SkillsDir + "/" + entry
	}
	return ""
}

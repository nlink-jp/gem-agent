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
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/instructions"
	"github.com/nlink-jp/gem-agent/internal/pathid"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// Pins maps a consumed file's project-relative name to "sha256:<hex>".
// Skills are pinned as a directory: the key is `.claude/skills/<name>`
// and the digest covers every file under it.
type Pins map[string]string

// Caps: one consumed file, the files of one skill, the entries of every
// kind a skill's walk looks at, how deep its directories nest, the
// entries of one directory, and the whole persistent-file walk.
// SkillWalk is twice SkillFiles: a skill that keeps every file in a
// directory of its own still fits. SkillDepth is how many directory
// handles one walk holds open at once.
const (
	FileCap     = 1 << 20
	SkillFiles  = 2000
	SkillWalk   = 2 * SkillFiles
	SkillDepth  = 32
	DirEntries  = 2000
	WalkEntries = 20000
)

// SkillsDir is the project skills directory (Claude Code's layout,
// ADR-0011).
const SkillsDir = ".claude/skills"

// ConfigNames are the runtime's own project configuration files — what
// gem-agent consumes, so what a pin must cover. The sibling runtime's
// .lagent.toml is a persistent file (sandbox.PersistentFile: the write
// lane denies it and the file tools ask the operator) but not a pin:
// gem-agent never reads it, so a change there alters nothing this
// runtime loads, and a pin over it would ask the operator to re-approve
// a file this runtime does not use. lagent pins only its own pair the
// same way. Snapshot still covers it, so a session that changes it is
// reported.
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
			// A skill directory may be a symlink (Discover follows it):
			// the walk starts from the resolved target, as Discover
			// reads it. Links inside are recorded by target, not followed.
			if real, err := filepath.EvalSymlinks(dir); err == nil {
				dir = real
			}
			d, cut := dirDigest(dir, SkillFiles)
			if cut {
				notes = append(notes, fmt.Sprintf("%s/%s: past what a trust record covers (up to 2,000 files, 4,000 entries in all, directories 32 deep) — the skill is not loaded when it is used", SkillsDir, e.Name()))
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
	root, err := bounded.OpenRoot(projectDir)
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
	// Only a regular file, looked at before the open and again on what
	// was opened.
	f, err := bounded.OpenRegular(root, name)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
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

// fileDigest hashes one regular file by path, bounded at FileCap; a file
// past the cap is hashed as its first FileCap bytes plus a marker. The
// persistent-file snapshot uses it; a skill is digested through its
// root (rootFileDigest), with the same bytes for the same file.
func fileDigest(path string) (string, bool) {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	f, err := bounded.OpenRegularPath(path)
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

// SkillPinName is the pin a project skill is recorded under: its
// directory entry, never the name its frontmatter declares.
func SkillPinName(entry string) string { return SkillsDir + "/" + entry }

// SkillDigest digests a skill through its own open root, with the cap a
// pin uses. It is the digest Compute records for that skill: the pin and
// the comparison made when the skill is used (ADR-0119) are one
// implementation, so an unchanged skill cannot read as changed.
func SkillDigest(root *os.Root) (string, bool) { return DigestRoot(root, SkillFiles) }

// Manifest is what a skill's digest was computed from: each covered
// file's relative path (slash-separated) and its line — the digest of
// its content, or "link:<target>" for a link, which is recorded and not
// followed.
type Manifest map[string]string

// SkillManifest is SkillDigest together with the manifest the digest
// was computed from. The comparison at use (ADR-0119 §1) needs both:
// the digest says the tree is the one that was trusted, and the
// manifest lets the bytes actually returned be checked against that
// tree — a compare before a read does not, by itself, say what the read
// returned.
func SkillManifest(root *os.Root) (digest string, files Manifest, cut bool) {
	entries, cut := manifestRoot(root, SkillFiles, SkillWalk, SkillDepth)
	files = make(Manifest, len(entries))
	for _, e := range entries {
		files[e.rel] = e.line
	}
	return digestEntries(entries, cut), files, cut
}

// ContentLine is the manifest line of a file whose whole content is
// data. It holds for a file of at most FileCap bytes; a larger one is
// covered only by its first FileCap bytes and its size, which is why
// nothing larger is ever served from a skill.
func ContentLine(data []byte) string {
	h := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(h[:])
}

// follow rewrites rel through the links the manifest records, until no
// component of it is a link: a link entry holds its target, and a read
// through it reaches what the target leads to. ok is false for a path
// that leaves the tree, an absolute target, and a chain longer than a
// few hops.
func (m Manifest) follow(rel string) (string, bool) {
	rel = path.Clean(rel)
	for hop := 0; hop < 8; hop++ {
		if rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
			return "", false
		}
		// In the manifest's own spelling, at every hop: a link's target
		// may be spelled differently from the entry it leads to as well.
		rel = m.spell(rel)
		// The shortest prefix that is a link, the whole path included.
		at, target := -1, ""
		for i := 0; i <= len(rel); i++ {
			if i < len(rel) && rel[i] != '/' {
				continue
			}
			if t, isLink := strings.CutPrefix(m[rel[:i]], "link:"); isLink {
				at, target = i, t
				break
			}
		}
		if at < 0 {
			return rel, true
		}
		if path.IsAbs(target) {
			return "", false
		}
		rel = path.Clean(path.Join(path.Dir(rel[:at]), target, rel[at:]))
	}
	return "", false
}

// spell rewrites rel, component by component, into the spelling the
// manifest has. A volume that does not tell case or Unicode form apart
// opens "Refs/A.md" for a tree that holds "refs/a.md"; the manifest is
// keyed by the names as stored. A component is rewritten only when no
// name at that place is spelled exactly as asked and exactly one is the
// same once case and form are set aside — an exact name is never
// passed over for a similar one, and two similar names are never
// chosen between. A component that matches nothing is left as it is.
func (m Manifest) spell(rel string) string {
	if rel == "." || rel == "" {
		return rel
	}
	parts := strings.Split(rel, "/")
	cur := ""
	for i, c := range parts {
		exact := c
		if cur != "" {
			exact = cur + "/" + c
		}
		if c == ".." || c == "." || m.has(exact) {
			cur = exact
			continue
		}
		match, n := "", 0
		want := foldName(c)
		for _, name := range m.namesIn(cur) {
			if foldName(name) == want {
				match, n = name, n+1
			}
		}
		if n != 1 {
			// Nothing to rewrite it to: the rest is left as asked.
			return strings.Join(append(strings.Split(exact, "/"), parts[i+1:]...), "/")
		}
		if cur == "" {
			cur = match
		} else {
			cur = cur + "/" + match
		}
	}
	return cur
}

// has reports whether p is an entry of the manifest or a directory on
// the way to one.
func (m Manifest) has(p string) bool {
	if _, ok := m[p]; ok {
		return true
	}
	for k := range m {
		if strings.HasPrefix(k, p+"/") {
			return true
		}
	}
	return false
}

// namesIn lists the names directly under dir ("" for the top): entries
// and the directories on the way to entries, each once.
func (m Manifest) namesIn(dir string) []string {
	prefix := ""
	if dir != "" {
		prefix = dir + "/"
	}
	seen := map[string]bool{}
	var names []string
	for k := range m {
		rest, ok := strings.CutPrefix(k, prefix)
		if !ok || rest == "" {
			continue
		}
		name, _, _ := strings.Cut(rest, "/")
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names
}

// Line returns the content line of the file a read of rel through the
// skill's root returns, following the links the manifest records. A
// path the manifest does not cover has no line.
func (m Manifest) Line(rel string) (string, bool) {
	r, ok := m.follow(rel)
	if !ok {
		return "", false
	}
	line, ok := m[r]
	return line, ok
}

// Holds reports whether data is this tree's content for the file a read
// of rel returns. The entry under that exact name decides when there is
// one. When there is none, the volume may still have found the file
// under another spelling of its name — a different case, or another
// Unicode form: the loader asks for "SKILL.md" and for whatever the
// model wrote, and a case-insensitive volume answers for "skill.md" and
// "Notes.md" alike. The bytes are then accepted if they are the content
// of an entry whose name is the same once case and Unicode form are set
// aside: they are still bytes of the trusted tree, under the name that
// was asked for. (The first version compared exact names only and
// refused such a skill as "still changing", for ever.)
//
// Folding here hands nothing to another file. On a volume that tells
// "Notes.md" from "notes.md" a read of a name with no entry fails at the
// open and never gets this far; a name that has an entry is decided by
// that entry alone.
func (m Manifest) Holds(rel string, data []byte) bool {
	want := ContentLine(data)
	if line, ok := m.Line(rel); ok {
		return line == want
	}
	key := foldName(rel)
	for k := range m {
		if foldName(k) != key {
			continue
		}
		if line, ok := m.Line(k); ok && line == want {
			return true
		}
	}
	return false
}

// foldName is a name with case and Unicode form set aside.
func foldName(p string) string {
	return strings.ToLower(pathid.Key(path.Clean(p)))
}

// Covers reports whether p — a file, a link, or a directory on the way
// to one — is part of the tree the digest covers. An empty directory, a
// FIFO and anything created since the digest was taken are not: the
// digest does not see them, so a listing must not show them.
func (m Manifest) Covers(p string) bool {
	p = m.spell(path.Clean(p))
	if _, ok := m[p]; ok {
		return true // a file, or a link under its own name
	}
	r, ok := m.follow(p)
	if !ok {
		return false
	}
	if _, ok := m[r]; ok {
		return true
	}
	for k := range m {
		if strings.HasPrefix(k, r+"/") {
			return true
		}
	}
	return false
}

// rootFileDigest hashes one regular file under root, bounded at FileCap;
// a file past the cap is hashed as its first FileCap bytes plus a
// marker, so it is still pinned and a change past the cap still changes
// the size part.
func rootFileDigest(root *os.Root, rel string) (string, bool) {
	st, err := root.Lstat(rel)
	if err != nil || !st.Mode().IsRegular() {
		return "", false
	}
	f, err := bounded.OpenRegular(root, rel)
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

// DigestRoot hashes a skill directory through root: for every regular
// file (sorted by relative path, links included by their target string)
// the path, the size and the content digest. cut reports that the tree
// is past a bound: more files than maxFiles, more entries of every kind
// than SkillWalk, or directories nested deeper than SkillDepth.
//
// It reads through the root — the object the skill loader holds open
// and reads from — not through a path resolved again at each call: a
// directory swapped under the same name is then compared as the tree
// the loader will read, not as another one (ADR-0119 §1).
func DigestRoot(root *os.Root, maxFiles int) (string, bool) {
	entries, cut := manifestRoot(root, maxFiles, SkillWalk, SkillDepth)
	return digestEntries(entries, cut), cut
}

type manifestEntry struct{ rel, line string }

// manifestRoot walks root and returns the covered entries, sorted. cut
// reports that the tree is past a bound: a file or link past maxFiles,
// more than maxWalk entries of every kind, or a directory nested deeper
// than maxDepth.
//
// The walk is bounded in the work it does, not only in what it
// collects. A directory is not an entry — it is neither counted as a
// file nor part of the digest — so directories added to a trusted skill
// change no digest and ask nothing, and a walk that stopped only at the
// file cap paid for all of them at every use (ADR-0119: measured). Three
// things kept that cost open, and each has its bound here:
//
//   - how much is listed: a directory is read only as far as the budget
//     goes, and what is read comes off the budget before any of it is
//     visited, so the walk lists no more than maxWalk names and one;
//   - how far each open travels: a directory is opened from its parent's
//     handle, one component, not by a path resolved again from the root
//     — that made a chain of d directories cost d*d/2 opens;
//   - how many handles are held: one per level, so maxDepth of them.
//
// The bounds are properties of the tree, not of the order it is read
// in: a tree is cut when it holds more than maxFiles files and links
// that can be digested, more than maxWalk entries in all, or a
// directory below level maxDepth. Within all three the result is what
// fs.WalkDir gave — a directory's names visited sorted, a link recorded
// and not followed, what cannot be read skipped — so a pin taken before
// is still equal to its tree. The one tree that reads differently is
// the one the old walk refused at exactly the file cap, for whatever
// sorted after its last file.
func manifestRoot(root *os.Root, maxFiles, maxWalk, maxDepth int) ([]manifestEntry, bool) {
	w := &manifestWalk{maxFiles: maxFiles, left: maxWalk, maxDepth: maxDepth}
	w.dir(root, "", 0)
	sort.Slice(w.entries, func(i, j int) bool { return w.entries[i].rel < w.entries[j].rel })
	return w.entries, w.cut
}

// manifestWalk is one walk of a skill's tree.
type manifestWalk struct {
	maxFiles, maxDepth int
	// left is how many more entries the walk may look at.
	left    int
	entries []manifestEntry
	cut     bool
	// listed counts the names read from directories, for the test that
	// holds the walk to its bound.
	listed int
}

// dir reads the directory held open as dir and visits what it holds,
// by name. rel is its path from the skill's root ("" for the root
// itself), level how many directories lie above it.
func (w *manifestWalk) dir(dir *os.Root, rel string, level int) {
	d, err := dir.Open(".")
	if err != nil {
		return // unreadable: skipped, as the walk always did
	}
	// One more than the budget: enough to know the directory does not
	// fit, and no more. A read that fails part-way still gives what it
	// read, and that much is visited, as before.
	list, _ := d.ReadDir(w.left + 1)
	_ = d.Close()
	w.listed += len(list)
	if len(list) > w.left {
		w.cut = true
		return
	}
	w.left -= len(list)
	sort.Slice(list, func(i, j int) bool { return list[i].Name() < list[j].Name() })
	for _, e := range list {
		base := e.Name()
		name := base
		if rel != "" {
			name = rel + "/" + base
		}
		switch t := e.Type(); {
		case t&fs.ModeSymlink != 0:
			if len(w.entries) >= w.maxFiles {
				w.cut = true
				return
			}
			target, _ := dir.Readlink(base)
			w.entries = append(w.entries, manifestEntry{name, "link:" + target})
		case t.IsRegular():
			// A file that cannot be digested is not an entry and is not
			// counted, wherever it sorts: counted at the cap and not
			// below it, it made the answer depend on its name.
			dg, ok := rootFileDigest(dir, base)
			if !ok {
				continue
			}
			// The cap is looked at where a file is counted. Looked at
			// on every entry, it refused a skill of exactly maxFiles
			// files for whatever happened to follow them.
			if len(w.entries) >= w.maxFiles {
				w.cut = true
				return
			}
			w.entries = append(w.entries, manifestEntry{name, dg})
		case t.IsDir():
			if !descends(base) {
				continue
			}
			if level == w.maxDepth {
				w.cut = true
				return
			}
			sub, err := dir.OpenRoot(base)
			if err != nil {
				continue // unreadable, or no longer a directory: skipped
			}
			w.dir(sub, name, level+1)
			_ = sub.Close()
			if w.cut {
				return
			}
		}
	}
}

// descends reports whether the walk goes into a directory of this
// name. One whose name is not valid UTF-8 was never entered: the walk
// used to read through the root's fs.FS, which refuses such a path, and
// what is under it is in no pin taken so far. It stays out, so those
// pins still compare equal; nothing under it is served either, since
// only what the manifest holds is.
func descends(base string) bool { return utf8.ValidString(base) }

// digestEntries is the digest of a manifest: what a pin holds.
func digestEntries(entries []manifestEntry, cut bool) string {
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\n", e.rel, e.line)
	}
	if cut {
		h.Write([]byte("\x00cut"))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// dirDigest is DigestRoot for a directory named by path. A directory
// that cannot be opened digests as an empty one, as a walk that found
// nothing always did.
func dirDigest(dir string, maxFiles int) (string, bool) {
	root, err := bounded.OpenRoot(dir)
	if err != nil {
		h := sha256.New()
		return "sha256:" + hex.EncodeToString(h.Sum(nil)), false
	}
	defer func() { _ = root.Close() }()
	return DigestRoot(root, maxFiles)
}

func readDir(dir string, n int) ([]os.DirEntry, bool, error) {
	d, err := bounded.OpenDir(dir)
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

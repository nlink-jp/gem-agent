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
	"testing"
)

// pathDirDigest is the digest as it was taken before ADR-0119: a walk of
// the path. It is kept here, and only here, as the reference the
// root-based digest must equal byte for byte. Every project that was
// trusted before the change has pins taken this way; a digest that
// differed would make each of their skills read as changed, and under
// ADR-0119 that is a question to the operator for every one of them.
func pathDirDigest(dir string, maxFiles int) (string, bool) {
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

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// The digest taken through a root equals the digest the pins already on
// disk were taken with, over every case a pin distinguishes.
func TestDigestRootEqualsThePinsAlreadyRecorded(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"a plain skill": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\nbody\n"))
			mustWrite(t, filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"))
			mustWrite(t, filepath.Join(dir, "references", "deep", "note.md"), []byte("note\n"))
		},
		"an empty directory": func(t *testing.T, dir string) {},
		"links, inside and leading out": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte("s\n"))
			if err := os.Symlink("SKILL.md", filepath.Join(dir, "alias.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc/hosts", filepath.Join(dir, "out")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("references", filepath.Join(dir, "refs")); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, "references", "a.md"), []byte("a\n"))
			if err := os.Symlink("nowhere", filepath.Join(dir, "dangling")); err != nil {
				t.Fatal(err)
			}
		},
		"a file past the cap": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte("s\n"))
			mustWrite(t, filepath.Join(dir, "big.bin"), []byte(strings.Repeat("x", FileCap+4096)))
		},
		"names that sort across directories": func(t *testing.T, dir string) {
			for _, rel := range []string{"a/z", "a.b", "a-b/c", "b", "ab/c", "a/b/c", "a0"} {
				mustWrite(t, filepath.Join(dir, filepath.FromSlash(rel)), []byte(rel))
			}
		},
		// The walk goes on past a directory it cannot open: what sorts
		// after it is in the pin.
		"a directory that cannot be opened, and what comes after it": func(t *testing.T, dir string) {
			for _, rel := range []string{"SKILL.md", "closed/inside", "later/file", "zz"} {
				mustWrite(t, filepath.Join(dir, filepath.FromSlash(rel)), []byte(rel))
			}
			closed := filepath.Join(dir, "closed")
			if err := os.Chmod(closed, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
		},
		"an unreadable file": func(t *testing.T, dir string) {
			mustWrite(t, filepath.Join(dir, "SKILL.md"), []byte("s\n"))
			mustWrite(t, filepath.Join(dir, "secret"), []byte("x\n"))
			if err := os.Chmod(filepath.Join(dir, "secret"), 0); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			build(t, dir)
			for _, max := range []int{SkillFiles, 3, 1} {
				want, wantCut := pathDirDigest(dir, max)
				root, err := os.OpenRoot(dir)
				if err != nil {
					t.Fatal(err)
				}
				got, gotCut := DigestRoot(root, max)
				entries, _ := manifestRoot(root, max, SkillWalk, SkillDepth)
				_ = root.Close()
				// The one tree the two walks read differently: exactly
				// the cap, then something that is not a file that can
				// be digested. The old walk looked at the cap on every
				// entry and refused it; it is covered whole now.
				if wantCut && !gotCut && len(entries) == max {
					want, wantCut = pathDirDigest(dir, 1<<30)
				}
				if got != want || gotCut != wantCut {
					t.Errorf("cap %d: through the root %s (cut %v), by path %s (cut %v)", max, got, gotCut, want, wantCut)
				}
				if viaDir, c := dirDigest(dir, max); viaDir != want || c != wantCut {
					t.Errorf("cap %d: dirDigest %s (cut %v), by path %s (cut %v)", max, viaDir, c, want, wantCut)
				}
			}
		})
	}
}

// More files than the cap: both digests stop at the same files, so a
// skill that was pinned cut is still equal to its pin — while it is
// within the walk's other bounds (walkbound_test.go); past those the
// walk stops sooner than the old one did, and startup asks once.
func TestDigestRootCutsWhereThePathWalkDid(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 40; i++ {
		mustWrite(t, filepath.Join(dir, fmt.Sprintf("d%02d", i%7), fmt.Sprintf("f%03d", i)), []byte{byte(i)})
	}
	for _, max := range []int{1, 5, 13, 39, 40, 41} {
		want, wantCut := pathDirDigest(dir, max)
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, gotCut := DigestRoot(root, max)
		_ = root.Close()
		if got != want || gotCut != wantCut {
			t.Errorf("cap %d: through the root %s (cut %v), by path %s (cut %v)", max, got, gotCut, want, wantCut)
		}
	}
}

// What Compute records for a project skill is what SkillDigest returns
// for that skill's root: the pin and the comparison at use are one
// digest (ADR-0119 §1). A skill reached through a link is included.
func TestSkillDigestIsThePinComputeRecords(t *testing.T) {
	proj := t.TempDir()
	plain := filepath.Join(proj, SkillsDir, "plain")
	mustWrite(t, filepath.Join(plain, "SKILL.md"), []byte("plain\n"))
	mustWrite(t, filepath.Join(plain, "scripts", "x.sh"), []byte("x\n"))
	elsewhere := filepath.Join(proj, "shared", "linked")
	mustWrite(t, filepath.Join(elsewhere, "SKILL.md"), []byte("linked\n"))
	if err := os.Symlink(elsewhere, filepath.Join(proj, SkillsDir, "linked")); err != nil {
		t.Fatal(err)
	}
	pins, notes := Compute(proj)
	if len(notes) != 0 {
		t.Fatalf("notes = %v", notes)
	}
	for entry, dir := range map[string]string{"plain": plain, "linked": elsewhere} {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(real)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := SkillDigest(root)
		_ = root.Close()
		if pin := pins[SkillPinName(entry)]; pin == "" || got != pin {
			t.Errorf("%s: at use %s, pinned %q", entry, got, pin)
		}
	}
}

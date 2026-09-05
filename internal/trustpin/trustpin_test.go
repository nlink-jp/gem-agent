package trustpin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Pins cover the consumed files and change when — and only when —
// their content does; a skill is pinned as a directory.
func TestComputeAndDiff(t *testing.T) {
	proj := t.TempDir()
	write(t, filepath.Join(proj, "AGENTS.md"), "rules\n")
	write(t, filepath.Join(proj, ".mcp.json"), "{}\n")
	write(t, filepath.Join(proj, ".claude/skills/a/SKILL.md"), "---\nname: a\n---\nbody\n")
	write(t, filepath.Join(proj, ".claude/skills/a/references/x.md"), "ref\n")
	write(t, filepath.Join(proj, "README.md"), "not pinned\n")
	pins, notes := Compute(proj)
	if len(notes) != 0 {
		t.Errorf("notes: %v", notes)
	}
	for _, want := range []string{"AGENTS.md", ".mcp.json", ".claude/skills/a"} {
		if !strings.HasPrefix(pins[want], "sha256:") {
			t.Errorf("%s not pinned: %v", want, pins)
		}
	}
	if _, ok := pins["README.md"]; ok {
		t.Error("README.md pinned")
	}
	again, _ := Compute(proj)
	if len(Diff(pins, again)) != 0 {
		t.Errorf("identical content differs: %v", Diff(pins, again))
	}
	write(t, filepath.Join(proj, ".claude/skills/a/references/x.md"), "changed\n")
	write(t, filepath.Join(proj, "CLAUDE.md"), "new\n")
	if err := os.Remove(filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	after, _ := Compute(proj)
	got := map[string]string{}
	for _, c := range Diff(pins, after) {
		got[c.Name] = c.Kind
	}
	want := map[string]string{".claude/skills/a": "changed", "CLAUDE.md": "added", ".mcp.json": "removed"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("unexpected changes: %v", got)
	}
}

// A symlinked skill directory is pinned through its target (that is how
// skills are shared), and a link inside a skill is pinned by its target
// string, not followed.
func TestSkillLinksArePinnedNotFollowed(t *testing.T) {
	proj := t.TempDir()
	real := filepath.Join(t.TempDir(), "shared")
	write(t, filepath.Join(real, "SKILL.md"), "---\nname: s\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(proj, ".claude/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(proj, ".claude/skills/s")); err != nil {
		t.Fatal(err)
	}
	pins, _ := Compute(proj)
	if _, ok := pins[".claude/skills/s"]; !ok {
		t.Fatalf("symlinked skill not pinned: %v", pins)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	write(t, secret, "s1\n")
	if err := os.Symlink(secret, filepath.Join(real, "link")); err != nil {
		t.Fatal(err)
	}
	withLink, _ := Compute(proj)
	if withLink[".claude/skills/s"] == pins[".claude/skills/s"] {
		t.Error("adding a link inside the skill did not change its pin")
	}
	write(t, secret, "s2\n")
	retarget, _ := Compute(proj)
	if retarget[".claude/skills/s"] != withLink[".claude/skills/s"] {
		t.Error("the link's target content was followed into the pin")
	}
}

// The snapshot lists every persistent file at any depth, records links
// by target, and the parent list covers each ancestor below the root.
func TestSnapshotAndParents(t *testing.T) {
	proj := t.TempDir()
	write(t, filepath.Join(proj, "AGENTS.md"), "a\n")
	write(t, filepath.Join(proj, "sub/deep/CLAUDE.md"), "c\n")
	write(t, filepath.Join(proj, "vendor/x/.git/hooks/pre-commit"), "#!/bin/sh\n")
	write(t, filepath.Join(proj, "vendor/x/.git/config"), "[core]\n")
	write(t, filepath.Join(proj, "src/main.go"), "package main\n")
	if err := os.Symlink("AGENTS.md", filepath.Join(proj, "sub/GEMINI.md")); err != nil {
		t.Fatal(err)
	}
	snap, cut := Snapshot(proj)
	if cut {
		t.Error("cut on a tiny tree")
	}
	for _, want := range []string{"AGENTS.md", "sub/deep/CLAUDE.md", "vendor/x/.git/hooks/pre-commit", "vendor/x/.git/config"} {
		if !strings.HasPrefix(snap[want], "sha256:") {
			t.Errorf("%s missing: %v", want, snap)
		}
	}
	if snap["sub/GEMINI.md"] != "link:AGENTS.md" {
		t.Errorf("link not recorded by target: %q", snap["sub/GEMINI.md"])
	}
	if _, ok := snap["src/main.go"]; ok {
		t.Error("an ordinary file was snapshotted")
	}
	parents := Parents(proj, snap)
	want := map[string]bool{
		filepath.Join(proj, "sub"): true, filepath.Join(proj, "sub/deep"): true,
		filepath.Join(proj, "vendor"): true, filepath.Join(proj, "vendor/x"): true,
		filepath.Join(proj, "vendor/x/.git"): true, filepath.Join(proj, "vendor/x/.git/hooks"): true,
	}
	got := map[string]bool{}
	for _, p := range parents {
		got[p] = true
	}
	for p := range want {
		if !got[p] {
			t.Errorf("parent %s missing: %v", p, parents)
		}
	}
	if got[proj] {
		t.Error("the project root is not a parent to deny")
	}
	if len(parents) > 0 && len(parents[0]) < len(parents[len(parents)-1]) {
		t.Error("parents are not deepest first")
	}
	write(t, filepath.Join(proj, "sub/deep/CLAUDE.md"), "c2\n")
	after, _ := Snapshot(proj)
	added, changed, removed := SnapshotDiff(snap, after)
	if len(added) != 0 || len(removed) != 0 || strings.Join(changed, ",") != "sub/deep/CLAUDE.md" {
		t.Errorf("diff = +%v ~%v -%v", added, changed, removed)
	}
}

// The walk stops at its cap and says so.
func TestSnapshotCap(t *testing.T) {
	proj := t.TempDir()
	for i := 0; i < WalkEntries+50; i++ {
		write(t, filepath.Join(proj, "many", strings.Repeat("f", 1)+string(rune('a'+i%26))+"-"+itoa(i)), "x")
	}
	_, cut := Snapshot(proj)
	if !cut {
		t.Error("walk past the cap was not reported as cut")
	}
}

func itoa(i int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(i))
}

func fmtInt(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

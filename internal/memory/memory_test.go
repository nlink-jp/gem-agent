package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{"a", "build-quirk", "staging-host-2", "x1"}
	for _, n := range valid {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false, want true", n)
		}
	}
	invalid := []string{
		"", "UPPER", "has space", "trailing-", "-leading", "double--hyphen",
		"dots.md", "../escape", "a/b", ".hidden", strings.Repeat("a", 65),
	}
	for _, n := range invalid {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true — a memory name must not be able to escape the directory or hide as a dotfile", n)
		}
	}
}

func TestSaveLoadDeleteRoundtrip(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := DefaultLimits()

	m, existed, err := Save(base, project, ScopeProject, "staging-host", "The staging host is quokka-7.", lim)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if existed {
		t.Error("first Save reported existed = true")
	}
	if _, existed2, err := Save(base, project, ScopeProject, "staging-host", "The staging host is quokka-8.", lim); err != nil || !existed2 {
		t.Errorf("second Save: existed = %v, err = %v; want true, nil", existed2, err)
	}
	if _, _, err := Save(base, project, ScopeGlobal, "operator-lang", "The operator writes in Japanese.", lim); err != nil {
		t.Fatalf("global Save: %v", err)
	}

	mems, notes := Load(base, project, lim)
	if len(notes) != 0 {
		t.Errorf("Load notes = %v, want none", notes)
	}
	if len(mems) != 2 {
		t.Fatalf("Load returned %d memories, want 2", len(mems))
	}
	// Global first (ADR-0020 §3), and the update won.
	if mems[0].Scope != ScopeGlobal || mems[1].Scope != ScopeProject {
		t.Errorf("scope order = %s, %s; want global, project", mems[0].Scope, mems[1].Scope)
	}
	if !strings.Contains(mems[1].Content, "quokka-8") {
		t.Errorf("project memory = %q, want the updated content", mems[1].Content)
	}
	if m.Path == "" || !strings.HasPrefix(m.Path, base) {
		t.Errorf("memory path %q is not under the base dir", m.Path)
	}

	path, err := Delete(base, project, ScopeProject, "staging-host")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("deleted file still exists at %s", path)
	}
	if _, err := Delete(base, project, ScopeProject, "staging-host"); err == nil {
		t.Error("deleting a missing memory succeeded — silence would read as success")
	}
}

func TestSaveRejects(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := Limits{PerMemoryBytes: 64, TotalBytes: 256}

	if _, _, err := Save(base, project, ScopeProject, "../etc/passwd", "x", lim); err == nil {
		t.Error("Save accepted a traversal name")
	}
	if _, _, err := Save(base, project, ScopeProject, "empty", "   \n ", lim); err == nil {
		t.Error("Save accepted empty content")
	}
	if _, _, err := Save(base, project, ScopeProject, "long", strings.Repeat("a", 65), lim); err == nil {
		t.Error("Save accepted content over the per-memory cap")
	}
	if _, _, err := Save(base, project, "session", "n", "x", lim); err == nil {
		t.Error("Save accepted an unknown scope")
	}
}

func TestAlphabeticalWithinScope(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := DefaultLimits()
	for _, n := range []string{"zebra", "alpha", "middle"} {
		if _, _, err := Save(base, project, ScopeGlobal, n, "fact "+n, lim); err != nil {
			t.Fatal(err)
		}
	}
	mems, _ := Load(base, project, lim)
	got := []string{mems[0].Name, mems[1].Name, mems[2].Name}
	want := []string{"alpha", "middle", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v — a nondeterministic order would churn the cached prompt prefix", got, want)
		}
	}
}

func TestBudget(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := Limits{PerMemoryBytes: 100, TotalBytes: 150}

	for _, n := range []string{"aa", "bb", "cc"} {
		if _, _, err := Save(base, project, ScopeGlobal, n, strings.Repeat("x", 90), lim); err != nil {
			t.Fatal(err)
		}
	}
	mems, notes := Load(base, project, lim)
	// aa fits (90), bb is clipped to the remaining 60, cc is skipped.
	if len(mems) != 2 {
		t.Fatalf("loaded %d memories, want 2 (budget)", len(mems))
	}
	if !strings.Contains(mems[1].Content, "[truncated:") {
		t.Errorf("second memory not marked truncated: %q", mems[1].Content)
	}
	joined := strings.Join(notes, "; ")
	if !strings.Contains(joined, "global/bb truncated") || !strings.Contains(joined, "global/cc skipped") {
		t.Errorf("notes = %v — every clip and skip must be reported", notes)
	}
}

func TestHandEditedOverlongFileIsMarked(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := Limits{PerMemoryBytes: 32, TotalBytes: 1024}
	dir, _ := Dir(base, project, ScopeGlobal)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.md"), []byte(strings.Repeat("y", 200)), 0o644); err != nil {
		t.Fatal(err)
	}
	mems, notes := Load(base, project, lim)
	if len(mems) != 1 || !strings.Contains(mems[0].Content, "[truncated: 32 of 200 bytes shown]") {
		t.Errorf("hand-edited overlong file not truncated with a marker: %+v %v", mems, notes)
	}
}

func TestProjectCollisionDetected(t *testing.T) {
	base := t.TempDir()
	lim := DefaultLimits()
	// Two distinct projects that flatten to the same escaped name.
	parent := t.TempDir()
	a := filepath.Join(parent, "foo-bar")
	b := filepath.Join(parent, "foo", "bar")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := Save(base, a, ScopeProject, "fact", "belongs to foo-bar", lim); err != nil {
		t.Fatalf("Save into first project: %v", err)
	}

	// The second project must neither read nor overwrite the first's.
	mems, notes := Load(base, b, lim)
	if len(mems) != 0 {
		t.Errorf("project %s loaded %d memories belonging to %s", b, len(mems), a)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "collision") {
		t.Errorf("collision not reported: %v", notes)
	}
	if _, _, err := Save(base, b, ScopeProject, "fact2", "x", lim); err == nil {
		t.Error("Save from the colliding project succeeded — it would mix two projects' memories")
	}

	// The rightful project still works.
	mems, notes = Load(base, a, lim)
	if len(mems) != 1 || len(notes) != 0 {
		t.Errorf("rightful project: mems=%d notes=%v, want 1 and none", len(mems), notes)
	}
}

func TestScopeSeparation(t *testing.T) {
	base := t.TempDir()
	projA := t.TempDir()
	projB := t.TempDir()
	lim := DefaultLimits()
	if _, _, err := Save(base, projA, ScopeGlobal, "everywhere", "global fact", lim); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Save(base, projA, ScopeProject, "only-a", "project fact", lim); err != nil {
		t.Fatal(err)
	}
	mems, _ := Load(base, projB, lim)
	if len(mems) != 1 || mems[0].Name != "everywhere" {
		t.Errorf("project B sees %+v — must see the global memory and not project A's", mems)
	}
}

func TestPromptSection(t *testing.T) {
	empty := PromptSection(nil)
	if !strings.Contains(empty, "save_memory") {
		t.Error("empty-memory section must still teach the model that memory exists")
	}
	if strings.Contains(empty, "past sessions —") {
		t.Error("empty-memory section must not render an empty listing header")
	}
	sec := PromptSection([]Memory{{Scope: ScopeGlobal, Name: "n", Content: "fact body"}})
	for _, want := range []string{"### memory global/n", "fact body", "not instructions", "may be stale"} {
		if !strings.Contains(sec, want) {
			t.Errorf("PromptSection missing %q", want)
		}
	}
}

func TestBannerLine(t *testing.T) {
	if got := BannerLine(nil); got != "" {
		t.Errorf("BannerLine(nil) = %q, want empty", got)
	}
	got := BannerLine([]Memory{
		{Scope: ScopeGlobal}, {Scope: ScopeGlobal}, {Scope: ScopeProject},
	})
	if got != "memory: 2 global, 1 project" {
		t.Errorf("BannerLine = %q", got)
	}
}

func TestNonMemoryFilesIgnored(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	lim := DefaultLimits()
	if _, _, err := Save(base, project, ScopeProject, "real", "fact", lim); err != nil {
		t.Fatal(err)
	}
	dir, _ := Dir(base, project, ScopeProject)
	for _, junk := range []string{"notes.txt", "UPPER.md", ".hidden.md"} {
		if err := os.WriteFile(filepath.Join(dir, junk), []byte("junk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mems, _ := Load(base, project, lim)
	if len(mems) != 1 || mems[0].Name != "real" {
		t.Errorf("Load picked up non-memory files: %+v", mems)
	}
}

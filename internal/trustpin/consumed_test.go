package trustpin

import (
	"os"
	"path/filepath"
	"testing"
)

// A consumed file that is a link is pinned the way its loader reads it
// (review F1: a link was absent to the pin and present to the loader):
// a link inside the project is pinned by target and content, so
// retargeting it to identical bytes is still a change; a link leaving
// the project is absent — the loader refuses it too.
func TestConsumedLinksArePinnedAsTheLoaderReadsThem(t *testing.T) {
	proj := t.TempDir()
	write(t, filepath.Join(proj, "docs/agents-a.md"), "rules\n")
	write(t, filepath.Join(proj, "docs/agents-b.md"), "rules\n")
	if err := os.Symlink("docs/agents-a.md", filepath.Join(proj, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	pins, _ := Compute(proj)
	a, ok := pins["AGENTS.md"]
	if !ok {
		t.Fatal("linked AGENTS.md not pinned")
	}
	if err := os.Remove(filepath.Join(proj, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("docs/agents-b.md", filepath.Join(proj, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if again, _ := Compute(proj); again["AGENTS.md"] == a {
		t.Error("retargeting the link to identical bytes did not change the pin")
	}
	outside := t.TempDir()
	write(t, filepath.Join(outside, "mcp.json"), "{}\n")
	if err := os.Symlink(filepath.Join(outside, "mcp.json"), filepath.Join(proj, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	if p, _ := Compute(proj); p[".mcp.json"] != "" {
		t.Error("a link leaving the project was pinned; the loader refuses it")
	}
}

// PinName maps a written path to the pin it belongs to.
func TestPinName(t *testing.T) {
	cases := map[string]string{
		"AGENTS.md": "AGENTS.md", ".gem-agent.toml": ".gem-agent.toml",
		".claude/skills/x/SKILL.md": ".claude/skills/x", ".claude/skills/x/refs/a.md": ".claude/skills/x",
		".claude/skills/x": ".claude/skills/x", ".claude/skills": "", "sub/AGENTS.md": "", "README.md": "",
	}
	for in, want := range cases {
		if got := PinName("", in); got != want {
			t.Errorf("PinName(%q) = %q, want %q", in, got, want)
		}
	}
}

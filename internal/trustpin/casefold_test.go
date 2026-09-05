package trustpin

import "testing"

// A write to a case variant of a pinned name is a write to that pin on
// the default volume.
func TestPinNameFoldsCase(t *testing.T) {
	cases := map[string]string{
		"agents.md": "AGENTS.md", "Claude.MD": "CLAUDE.md", ".Mcp.json": ".mcp.json",
		".GEM-AGENT.toml": ".gem-agent.toml", ".Claude/skills/deploy/SKILL.md": ".claude/skills/deploy",
		".CLAUDE/Skills/deploy": ".claude/skills/deploy", "readme.md": "",
	}
	for in, want := range cases {
		if got := PinName("", in); got != want {
			t.Errorf("PinName(%q) = %q, want %q", in, got, want)
		}
	}
}

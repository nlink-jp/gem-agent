package cmd

import "testing"

// ADR-0021: two long remote tool names that truncate identically must
// not collide — the hash suffix keeps them distinct and deterministic.
func TestMCPToolNameTruncationNoCollision(t *testing.T) {
	server := "very-long-server-name-for-testing"
	a := mcpToolName(server, "get_screenshot_full_page_render_v2")
	b := mcpToolName(server, "get_screenshot_full_page_render_v3")
	if len(a) > 64 || len(b) > 64 {
		t.Fatalf("names exceed the Gemini limit: %d, %d", len(a), len(b))
	}
	if a == b {
		t.Errorf("truncated names collide: %q", a)
	}
	if a != mcpToolName(server, "get_screenshot_full_page_render_v2") {
		t.Error("truncated name not deterministic — policies could not target it")
	}
	// Short names stay untouched.
	if got := mcpToolName("srv", "tool"); got != "mcp__srv__tool" {
		t.Errorf("short name mangled: %q", got)
	}
}

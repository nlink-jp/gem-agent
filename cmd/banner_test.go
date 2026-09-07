package cmd

import (
	"strings"
	"testing"
)

// A toggle in the settings panel reconnects the servers, and the reload
// report is a line per server — twenty-five on a full machine. The
// panel's one-line message keeps the warnings and drops the inventory.
func TestReloadWarningsKeepsOnlyWhatWentWrong(t *testing.T) {
	report := "mcp reloaded: 3 server(s), 12 tool(s)\n" +
		"  obsidian [global] (51 tools)\n" +
		"  github [global] (43 tools)\n" +
		"warning: MCP server chrome-pilot unavailable: exec failed\n" +
		"note: project .mcp.json overrides global MCP server \"github\"\n"

	got := reloadWarnings(report)
	if !strings.Contains(got, "chrome-pilot unavailable") || !strings.Contains(got, "overrides global") {
		t.Errorf("reloadWarnings dropped something the operator needs: %q", got)
	}
	for _, gone := range []string{"mcp reloaded", "51 tools", "43 tools"} {
		if strings.Contains(got, gone) {
			t.Errorf("reloadWarnings kept the inventory (%q): %q", gone, got)
		}
	}
	if reloadWarnings("mcp reloaded: 0 server(s), 0 tool(s)\n") != "" {
		t.Error("a clean reload should say nothing")
	}
}

package cmd

import (
	"context"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"
)

// withMCP gives a store an inventory and a filter built from the same
// three scopes the runtime uses, plus a reload stub that re-derives the
// filter the way the real one does.
func withMCP(t *testing.T, s *settingsStore, servers []string, offered map[string][]string) {
	t.Helper()
	s.inv = mcpInventory{Servers: servers, Offered: offered, Scopes: map[string]string{}}
	rebuild := func() mcpfilter.Filter {
		f, err := mcpfilter.Build(s.cfg.MCP.Exclude, s.policyFile.MCP.Exclude, s.projectCfg.MCP.Exclude)
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	s.filter = rebuild()
	s.reloadMCP = func() (mcpfilter.Filter, mcpInventory, string) { return rebuild(), s.inv, "" }
}

// registerFakeMCPTool puts a tool in the registry under the name the
// connect loop would give it, so the approval rows have something to
// group.
func registerFakeMCPTool(s *settingsStore, server, fn string) error {
	return s.registry.Register(&tools.Tool{
		Name:        mcpToolName(server, fn),
		Description: "[MCP:" + server + "] test",
		Parameters:  map[string]any{"type": "object"},
		Mutating:    true,
		Run: func(context.Context, map[string]any) (string, error) {
			return "", nil
		},
	})
}

func excludeRow(d tui.SettingsData, entry string) (tui.SettingRow, bool) {
	for _, r := range d.Rows {
		if r.Exclude == entry {
			return r, true
		}
	}
	return tui.SettingRow{}, false
}

// ADR-0077 §3: two levels — the servers, and the functions of the ones
// that listed. A server's row exists whether or not it is running.
func TestPanelDrawsServersAndTheirFunctions(t *testing.T) {
	s := newStore(t)
	withMCP(t, s, []string{"obsidian"}, map[string][]string{
		"obsidian": {"get_vault_file", "patch_vault_file"},
	})
	d := s.data()

	parent, ok := excludeRow(d, "obsidian")
	if !ok {
		t.Fatal("no server row")
	}
	if !parent.Collapsible || parent.Child {
		t.Errorf("server row is not a group parent: %+v", parent)
	}
	child, ok := excludeRow(d, "obsidian/patch_vault_file")
	if !ok {
		t.Fatal("no function row")
	}
	if !child.Child || child.Group != parent.Group {
		t.Errorf("function row is not a child of its server: %+v", child)
	}
	if parent.Value != "on" || child.Value != "on" {
		t.Errorf("nothing is excluded, so every row should read on: %q / %q", parent.Value, child.Value)
	}
}

// An excluded server was never started, so it has no functions to show —
// and its row has to be there anyway, or there is no way back.
func TestPanelShowsAnExcludedServerWithNoChildren(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"chrome-pilot"}
	withMCP(t, s, []string{"chrome-pilot"}, map[string][]string{})
	d := s.data()

	row, ok := excludeRow(d, "chrome-pilot")
	if !ok {
		t.Fatal("an excluded server lost its row — it could never be turned back on")
	}
	if row.Value != "off" {
		t.Errorf("excluded server reads %q, want off", row.Value)
	}
	if row.Source != config.FromFile {
		t.Errorf("provenance = %q, want the file that decided it", row.Source)
	}
	for _, r := range d.Rows {
		if r.Child && r.Group == row.Group {
			t.Errorf("a server that never listed has a function row: %+v", r)
		}
	}
}

// Per server the nearest scope decides whole, so the panel writes the
// server's whole state — an operator who turns one function off must not
// silently lose the ones their own config excluded.
func TestPanelWriteCarriesTheServersOtherExclusions(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"obsidian/search_and_replace", "github"}
	withMCP(t, s, []string{"obsidian", "github"}, map[string][]string{
		"obsidian": {"get_vault_file", "patch_vault_file", "search_and_replace"},
	})

	_, line := s.Apply(tui.SettingChange{Exclude: "obsidian/patch_vault_file", Value: "off"})
	if line == "" {
		t.Fatal("the edit reported nothing")
	}
	got := map[string]bool{}
	for _, e := range s.policyFile.MCP.Exclude {
		got[e] = true
	}
	if !got["obsidian/patch_vault_file"] {
		t.Error("the toggled function was not written")
	}
	if !got["obsidian/search_and_replace"] {
		t.Error("config.toml's other exclusion for this server was dropped by the write")
	}
	if got["github"] {
		t.Error("the write reached a server the operator did not touch")
	}
}

// Turning a function back on removes just that entry.
func TestPanelTurningAFunctionOnRemovesOnlyIt(t *testing.T) {
	s := newStore(t)
	s.policyFile.MCP.Exclude = []string{"obsidian/patch_vault_file", "obsidian/search_and_replace"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{
		"obsidian": {"get_vault_file", "patch_vault_file", "search_and_replace"},
	})

	s.Apply(tui.SettingChange{Exclude: "obsidian/patch_vault_file", Value: "on"})
	if len(s.policyFile.MCP.Exclude) != 1 || s.policyFile.MCP.Exclude[0] != "obsidian/search_and_replace" {
		t.Errorf("exclusions = %v, want only the untouched one", s.policyFile.MCP.Exclude)
	}
}

// Turning a whole server off replaces whatever was said about it.
func TestPanelTurningAServerOffReplacesItsEntries(t *testing.T) {
	s := newStore(t)
	s.policyFile.MCP.Exclude = []string{"obsidian/patch_vault_file"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{"obsidian": {"get_vault_file"}})

	s.Apply(tui.SettingChange{Exclude: "obsidian", Value: "off"})
	if len(s.policyFile.MCP.Exclude) != 1 || s.policyFile.MCP.Exclude[0] != "obsidian" {
		t.Errorf("exclusions = %v, want the whole server", s.policyFile.MCP.Exclude)
	}
}

// policy.toml having spoken about a server is the answer for every row
// under it — including the ones it left on. That shadowing is what the
// provenance column exists to show.
func TestPanelProvenanceShowsTheShadowingFile(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"obsidian/search_and_replace"}
	s.policyFile.MCP.Exclude = []string{"obsidian/patch_vault_file"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{
		"obsidian": {"get_vault_file", "patch_vault_file", "search_and_replace"},
	})
	d := s.data()

	for _, entry := range []string{"obsidian", "obsidian/get_vault_file", "obsidian/search_and_replace"} {
		r, ok := excludeRow(d, entry)
		if !ok {
			t.Fatalf("no row for %s", entry)
		}
		if r.Source != config.PolicyFileName {
			t.Errorf("%s provenance = %q, want %s — policy.toml decided this server whole",
				entry, r.Source, config.PolicyFileName)
		}
	}
	// And the shadowed config entry is genuinely not in force.
	r, _ := excludeRow(d, "obsidian/search_and_replace")
	if r.Value != "on" {
		t.Errorf("a config exclusion shadowed by policy.toml is still applied: %q", r.Value)
	}
}

// The approval section adopts the same two levels (ADR-0009 decision 1,
// amended): built-ins stay flat, a server's tools sit under it.
func TestApprovalRowsAreGroupedByServer(t *testing.T) {
	s := newStore(t)
	withMCP(t, s, []string{"obsidian"}, map[string][]string{"obsidian": {"get_vault_file"}})
	if err := registerFakeMCPTool(s, "obsidian", "get_vault_file"); err != nil {
		t.Fatal(err)
	}
	d := s.data()

	var parent, child *tui.SettingRow
	for i := range d.Rows {
		r := &d.Rows[i]
		if r.Section != "approval policy" {
			continue
		}
		if r.Collapsible && r.Label == "obsidian" {
			parent = r
		}
		if r.Tool == mcpToolName("obsidian", "get_vault_file") {
			child = r
		}
	}
	if parent == nil {
		t.Fatal("no approval group for the server")
	}
	if child == nil || !child.Child || child.Group != parent.Group {
		t.Fatalf("the server's tool is not under its group: %+v", child)
	}
	if len(parent.Values) != 0 {
		t.Error("the group heading offers a value it cannot set")
	}
	// Built-ins have no server to sit under.
	for _, r := range d.Rows {
		if r.Tool == "list_files" && r.Child {
			t.Error("a built-in was filed under a server")
		}
	}
}

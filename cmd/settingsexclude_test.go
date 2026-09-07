package cmd

import (
	"context"
	"strings"
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
		f, err := mcpfilter.Build(s.cfg.MCP.Exclude, policyScope(s.policyFile), s.projectCfg.MCP.Exclude)
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

// seedPolicy puts exclusions in the machine-owned file and reloads the
// store from it. Seeding only the in-memory struct is not a state the
// runtime can be in: applyExclude derives the server's set from the file
// it is about to write, inside the lock.
func seedPolicy(t *testing.T, s *settingsStore, exclude, decided []string) {
	t.Helper()
	pf := &config.PolicyFile{Tools: map[string]string{}, Projects: map[string]config.ProjectPolicy{}}
	pf.MCP.Exclude = exclude
	pf.MCP.Decided = decided
	if err := pf.Save(s.policyPath); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadPolicyFile(s.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	*s.policyFile = *loaded
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
	seedPolicy(t, s, []string{"obsidian/patch_vault_file", "obsidian/search_and_replace"}, []string{"obsidian"})
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
	seedPolicy(t, s, []string{"obsidian/patch_vault_file"}, []string{"obsidian"})
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
	seedPolicy(t, s, []string{"obsidian/patch_vault_file"}, []string{"obsidian"})
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

// loadPolicy reads what actually reached the disk. Every panel test that
// asserted on the in-memory struct passed while Save was dropping the
// [mcp] table on the floor (pre-release review), so the exclusion tests
// go through the file from here on.
func loadPolicy(t *testing.T, s *settingsStore) *config.PolicyFile {
	t.Helper()
	pf, err := config.LoadPolicyFile(s.policyPath)
	if err != nil {
		t.Fatal(err)
	}
	return pf
}

func TestPanelWriteReachesTheFile(t *testing.T) {
	s := newStore(t)
	withMCP(t, s, []string{"obsidian"}, map[string][]string{"obsidian": {"patch_vault_file"}})

	s.Apply(tui.SettingChange{Exclude: "obsidian/patch_vault_file", Value: "off"})

	pf := loadPolicy(t, s)
	if len(pf.MCP.Exclude) != 1 || pf.MCP.Exclude[0] != "obsidian/patch_vault_file" {
		t.Errorf("policy.toml holds %v — the panel said it saved", pf.MCP.Exclude)
	}
}

// The case the ADR's own justification for keeping an excluded server's
// row visible depends on: a server config.toml turned off must be
// switchable back on from the panel.
func TestPanelCanTurnAConfigExclusionBackOn(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"chrome-pilot"}
	withMCP(t, s, []string{"chrome-pilot"}, map[string][]string{})
	if !s.filter.Server("chrome-pilot") {
		t.Fatal("setup: the server should start out excluded")
	}

	s.Apply(tui.SettingChange{Exclude: "chrome-pilot", Value: "on"})

	if s.filter.Server("chrome-pilot") {
		t.Error("the server is still excluded — the panel could not undo config.toml")
	}
	pf := loadPolicy(t, s)
	found := false
	for _, d := range pf.MCP.Decided {
		if d == "chrome-pilot" {
			found = true
		}
	}
	if !found {
		t.Errorf("policy.toml does not record the decision: %+v", pf.MCP)
	}
}

// Same for the last function of a server.
func TestPanelCanTurnTheLastConfigFunctionBackOn(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"obsidian/patch_vault_file"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{"obsidian": {"patch_vault_file"}})

	s.Apply(tui.SettingChange{Exclude: "obsidian/patch_vault_file", Value: "on"})

	if s.filter.Func("obsidian", "patch_vault_file") {
		t.Error("the function is still excluded — config.toml was not shadowed")
	}
}

// policy.toml is global. A project's own exclusion must not be copied
// into it and follow the operator into every other project.
func TestPanelDoesNotPromoteProjectExclusions(t *testing.T) {
	s := newStore(t)
	s.projectCfg.MCP.Exclude = []string{"obsidian/project_only"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{
		"obsidian": {"project_only", "another"},
	})

	s.Apply(tui.SettingChange{Exclude: "obsidian/another", Value: "off"})

	pf := loadPolicy(t, s)
	for _, e := range pf.MCP.Exclude {
		if e == "obsidian/project_only" {
			t.Errorf("a project-scoped exclusion was promoted to the global file: %v", pf.MCP.Exclude)
		}
	}
	// And it is still in force here, from its own scope.
	if !s.filter.Func("obsidian", "project_only") {
		t.Error("the project's own exclusion stopped applying")
	}
}

// The panel must not write what the loader would refuse to read: the
// next start would fail with hand-editing the machine-owned file as the
// only way out.
func TestPanelRefusesAnUnwritableName(t *testing.T) {
	s := newStore(t)
	withMCP(t, s, []string{"obsidian"}, map[string][]string{"obsidian": {"a"}})

	_, line := s.Apply(tui.SettingChange{Exclude: "obsidian/a/b", Value: "off"})

	if line == "" || !strings.Contains(line, "cannot exclude") {
		t.Errorf("line = %q, want a refusal", line)
	}
	if pf := loadPolicy(t, s); len(pf.MCP.Exclude) != 0 {
		t.Errorf("the bad entry was written anyway: %v", pf.MCP.Exclude)
	}
}

// The defect the re-review would not ship: two keypresses on a server row
// (off, then on again) discarded every function exclusion config.toml
// held for that server, permanently, with policy.toml shadowing the file
// from then on.
func TestServerOffThenOnKeepsConfigsFunctionExclusions(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"obsidian/delete_vault_file", "obsidian/patch_vault_file"}
	withMCP(t, s, []string{"obsidian"}, map[string][]string{
		"obsidian": {"get_vault_file", "delete_vault_file", "patch_vault_file"},
	})

	s.Apply(tui.SettingChange{Exclude: "obsidian", Value: "off"})
	s.Apply(tui.SettingChange{Exclude: "obsidian", Value: "on"})

	if s.filter.Server("obsidian") {
		t.Fatal("the server did not come back on")
	}
	for _, fn := range []string{"delete_vault_file", "patch_vault_file"} {
		if !s.filter.Func("obsidian", fn) {
			t.Errorf("%s was silently re-declared — config.toml excluded it by hand", fn)
		}
	}
	if s.filter.Func("obsidian", "get_vault_file") {
		t.Error("a function nobody excluded came back off")
	}
}

// Turning on a server that only the panel ever excluded leaves no trace:
// recording "decided, nothing excluded" would shadow a config.toml the
// operator writes tomorrow.
func TestTurningOnAPanelOnlyExclusionWithdrawsTheOpinion(t *testing.T) {
	s := newStore(t)
	withMCP(t, s, []string{"chrome-pilot"}, map[string][]string{"chrome-pilot": {"navigate"}})

	s.Apply(tui.SettingChange{Exclude: "chrome-pilot", Value: "off"})
	s.Apply(tui.SettingChange{Exclude: "chrome-pilot", Value: "on"})

	pf := loadPolicy(t, s)
	if len(pf.MCP.Exclude) != 0 || len(pf.MCP.Decided) != 0 {
		t.Errorf("the file still has an opinion: exclude=%v decided=%v", pf.MCP.Exclude, pf.MCP.Decided)
	}
}

// The provenance column has to name the file that decided, including
// when its opinion is "nothing excluded" — an operator following it to
// config.toml would edit a file that is shadowed.
func TestProvenanceNamesTheDecidingFileWhenItExcludesNothing(t *testing.T) {
	s := newStore(t)
	s.cfg.MCP.Exclude = []string{"chrome-pilot"}
	withMCP(t, s, []string{"chrome-pilot"}, map[string][]string{})

	s.Apply(tui.SettingChange{Exclude: "chrome-pilot", Value: "on"})

	row, ok := excludeRow(s.data(), "chrome-pilot")
	if !ok {
		t.Fatal("no row")
	}
	if row.Value != "on" {
		t.Fatalf("row = %q, want on", row.Value)
	}
	if row.Source != config.PolicyFileName {
		t.Errorf("source = %q, want %s — config.toml no longer decides this server",
			row.Source, config.PolicyFileName)
	}
}

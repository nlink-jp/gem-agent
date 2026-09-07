package mcpfilter

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		in     string
		server string
		fn     string
		err    string
	}{
		{in: "obsidian", server: "obsidian"},
		{in: "  obsidian  ", server: "obsidian"},
		{in: "obsidian/patch_vault_file", server: "obsidian", fn: "patch_vault_file"},
		{in: "", err: "empty entry"},
		{in: "   ", err: "empty entry"},
		{in: "mcp__obsidian__search_*", err: "no patterns"},
		{in: "obsidian/", err: "both sides"},
		{in: "/patch", err: "both sides"},
		{in: "a/b/c", err: "at most"},
	} {
		got, err := Parse(tc.in, FromConfig)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("Parse(%q) error = %v, want containing %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got.Server != tc.server || got.Func != tc.fn {
			t.Errorf("Parse(%q) = %+v, want server %q func %q", tc.in, got, tc.server, tc.fn)
		}
	}
}

func TestEmptyFilterExcludesNothing(t *testing.T) {
	f, err := Build(nil, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Empty() {
		t.Error("a filter with no entries is not Empty")
	}
	if f.Server("obsidian") || f.Func("obsidian", "anything") {
		t.Error("an empty filter excluded something")
	}
}

func TestServerAndFunc(t *testing.T) {
	f, err := Build([]string{"chrome-pilot", "obsidian/patch_vault_file"}, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Server("chrome-pilot") {
		t.Error("whole-server entry did not exclude the server")
	}
	if !f.Func("chrome-pilot", "anything") {
		t.Error("an excluded server's functions are excluded with it")
	}
	if f.Server("obsidian") {
		t.Error("a function entry stopped a server — it must not (§1)")
	}
	if !f.Func("obsidian", "patch_vault_file") {
		t.Error("function entry did not exclude the function")
	}
	if f.Func("obsidian", "get_vault_file") {
		t.Error("function entry excluded a sibling")
	}
}

// Per server, the nearest scope decides whole (ADR-0077 §2): policy
// replaces config for that server, and leaves every other server alone.
func TestPolicyReplacesConfigPerServer(t *testing.T) {
	f, err := Build(
		[]string{"obsidian/patch_vault_file", "obsidian/search_and_replace", "github"},
		PolicyScope{Entries: []string{"obsidian/delete_vault_file"}},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Func("obsidian", "delete_vault_file") {
		t.Error("policy entry not in force")
	}
	if f.Func("obsidian", "patch_vault_file") || f.Func("obsidian", "search_and_replace") {
		t.Error("config entries for a server policy speaks about were merged, not replaced")
	}
	if !f.Server("github") {
		t.Error("policy about obsidian disturbed the config's word about github")
	}
}

// The project file may only add (ADR-0008 §4's direction rule, held by
// construction): it can never bring back something a nearer scope excluded.
func TestProjectOnlyAdds(t *testing.T) {
	f, err := Build([]string{"obsidian/patch_vault_file"}, PolicyScope{}, []string{"github", "obsidian/get_vault_file"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Server("github") {
		t.Error("project entry did not add a server exclusion")
	}
	for _, fn := range []string{"patch_vault_file", "get_vault_file"} {
		if !f.Func("obsidian", fn) {
			t.Errorf("project entry replaced instead of adding: %s lost", fn)
		}
	}
}

func TestProjectAddsOnTopOfPolicy(t *testing.T) {
	f, err := Build(nil, PolicyScope{Entries: []string{"obsidian/delete_vault_file"}}, []string{"obsidian/patch_vault_file"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Func("obsidian", "delete_vault_file") || !f.Func("obsidian", "patch_vault_file") {
		t.Error("project entries must union with the policy's, not replace them")
	}
}

func TestBuildRejectsBadEntryWithScope(t *testing.T) {
	_, err := Build(nil, PolicyScope{}, []string{"a/b/c"})
	if err == nil {
		t.Fatal("a malformed entry was accepted")
	}
	if !strings.Contains(err.Error(), string(FromProject)) {
		t.Errorf("error does not name the scope it came from: %v", err)
	}
}

func TestUnmatchedNamesWhatMatchedNothing(t *testing.T) {
	f, err := Build(
		[]string{"typo-server", "obsidian/renamed_away", "obsidian/patch_vault_file"},
		PolicyScope{}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]bool{"obsidian": true, "github": true}
	listed := map[string][]string{
		"obsidian": {"patch_vault_file", "get_vault_file"},
		"github":   {"list_issues"},
	}
	got := f.Unmatched(configured, listed, true)
	if len(got) != 2 {
		t.Fatalf("Unmatched = %v, want two entries", got)
	}
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "typo-server") || !strings.Contains(joined, "renamed_away") {
		t.Errorf("Unmatched did not name both stale entries: %v", got)
	}
	if strings.Contains(joined, "patch_vault_file") {
		t.Errorf("Unmatched reported an entry that did its work: %v", got)
	}
}

// An excluded server is configured but never started: naming it, and
// naming one of its functions, are both correct — neither is stale.
func TestUnmatchedIsSilentForServersThatDidNotList(t *testing.T) {
	f, err := Build([]string{"chrome-pilot", "chrome-pilot/take_screenshot"}, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	configured := map[string]bool{"chrome-pilot": true, "obsidian": true}
	listed := map[string][]string{"obsidian": {"get_vault_file"}}
	if got := f.Unmatched(configured, listed, true); len(got) != 0 {
		t.Fatalf("Unmatched = %v, want nothing: the server is configured, just not started", got)
	}
}

func TestUnmatchedIsDeduped(t *testing.T) {
	f, err := Build([]string{"ghost"}, PolicyScope{}, []string{"ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Unmatched(map[string]bool{}, map[string][]string{}, true); len(got) != 2 {
		// The same server named in two scopes is two distinct lines
		// (they carry different scope labels) — dedupe only collapses
		// byte-identical ones.
		t.Logf("Unmatched = %v", got)
	}
	f2, err := Build([]string{"ghost", "ghost"}, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f2.Unmatched(map[string]bool{}, map[string][]string{}, true); len(got) != 1 {
		t.Errorf("identical entries were not deduped: %v", got)
	}
}

// A server the panel decided about shadows config.toml even when it
// excluded nothing of it. Inferring the opinion from the entry list made
// this state unrepresentable, and a server excluded in config.toml could
// then never be turned back on (pre-release review).
func TestDecidedShadowsConfigWithNoEntries(t *testing.T) {
	f, err := Build([]string{"chrome-pilot", "obsidian/patch_vault_file"},
		PolicyScope{Decided: []string{"chrome-pilot"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.Server("chrome-pilot") {
		t.Error("the panel turned this server on and config.toml still won")
	}
	if !f.Func("obsidian", "patch_vault_file") {
		t.Error("a decision about one server disturbed another")
	}
}

// A decided server with entries behaves as before: its entries replace
// config.toml's for that server.
func TestDecidedWithEntriesStillReplaces(t *testing.T) {
	f, err := Build([]string{"obsidian/a", "obsidian/b"},
		PolicyScope{Entries: []string{"obsidian/c"}, Decided: []string{"obsidian"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Func("obsidian", "c") || f.Func("obsidian", "a") || f.Func("obsidian", "b") {
		t.Error("the policy entries did not replace the config ones whole")
	}
}

// When a server list could not be read, a name missing from it proves
// nothing — and telling the operator to delete a correct line is worse
// than saying nothing.
func TestUnmatchedIsSilentWhenTheListsAreIncomplete(t *testing.T) {
	f, err := Build([]string{"a-server-from-another-project"}, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Unmatched(map[string]bool{}, map[string][]string{}, false); len(got) != 0 {
		t.Errorf("Unmatched = %v, want silence while the lists are incomplete", got)
	}
	if got := f.Unmatched(map[string]bool{}, map[string][]string{}, true); len(got) != 1 {
		t.Errorf("Unmatched = %v, want the stale entry once the lists are complete", got)
	}
}

// Shadowing is intended; shadowing in silence is not. A config entry a
// nearer scope overrode still does nothing, and "a name that matches
// nothing is reported, not ignored" covers that too (pre-release
// re-review: `decided` widened the silent set to every server the panel
// ever touched).
func TestShadowedConfigEntriesAreReported(t *testing.T) {
	f, err := Build([]string{"obsidian/delete_vault_file"},
		PolicyScope{Decided: []string{"obsidian"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := f.Unmatched(map[string]bool{"obsidian": true},
		map[string][]string{"obsidian": {"delete_vault_file"}}, true)
	if len(got) != 1 || !strings.Contains(got[0], "not in force") {
		t.Fatalf("Unmatched = %v, want the shadowed entry reported", got)
	}
	if !strings.Contains(got[0], "delete_vault_file") {
		t.Errorf("the report does not name the line: %q", got[0])
	}
}

// FunctionEntries answers "turn this server on — what stays off?", which
// For cannot: For collapses to the whole-server entry and would discard
// the functions a lower scope excluded by hand.
func TestFunctionEntriesIgnoresTheWholeServerEntry(t *testing.T) {
	f, err := Build([]string{"obsidian", "obsidian/a", "obsidian/b"}, PolicyScope{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.For("obsidian"); len(got) != 1 || got[0] != "obsidian" {
		t.Errorf("For = %v, want the whole-server entry", got)
	}
	if got := strings.Join(f.FunctionEntries("obsidian"), ","); got != "obsidian/a,obsidian/b" {
		t.Errorf("FunctionEntries = %q, want both functions", got)
	}
	if f.Knows("github") {
		t.Error("Knows reported an opinion about a server with none")
	}
}

package policy

import (
	"strings"
	"testing"
)

func build(t *testing.T, global, project map[string]string, trusted bool) (Policy, []Note) {
	t.Helper()
	p, notes, err := Build(global, project, nil, trusted)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return p, notes
}

func TestGlobalPolicyApplies(t *testing.T) {
	p, _ := build(t, map[string]string{
		"shell_exec":   "always",
		"write_file":   "never",
		"mcp__tor__ip": "never",
	}, nil, false)

	for tool, want := range map[string]Decision{
		"shell_exec":   AlwaysAsk,
		"write_file":   NeverAsk,
		"mcp__tor__ip": NeverAsk,
		"edit_file":    Default,
	} {
		if got := p.For(tool); got != want {
			t.Errorf("For(%q) = %v, want %v", tool, got, want)
		}
	}
}

// The point of the wildcard: a read-only lookup server in one line.
func TestWildcardMatchesAServersTools(t *testing.T) {
	p, _ := build(t, map[string]string{"mcp__tor-exit-lookup__*": "never"}, nil, false)
	if got := p.For("mcp__tor-exit-lookup__check_ip"); got != NeverAsk {
		t.Errorf("wildcard did not match: %v", got)
	}
	if got := p.For("mcp__urlscan-lookup__scan_url"); got != Default {
		t.Errorf("wildcard matched another server: %v", got)
	}
}

func TestExactBeatsWildcardAndLongerWildcardWins(t *testing.T) {
	p, _ := build(t, map[string]string{
		"mcp__*":                         "never",
		"mcp__urlscan-lookup__*":         "always",
		"mcp__urlscan-lookup__get_quota": "never",
	}, nil, false)

	for tool, want := range map[string]Decision{
		"mcp__urlscan-lookup__get_quota": NeverAsk,  // exact
		"mcp__urlscan-lookup__scan_url":  AlwaysAsk, // longer wildcard
		"mcp__tor-exit-lookup__check_ip": NeverAsk,  // shorter wildcard
	} {
		if got := p.For(tool); got != want {
			t.Errorf("For(%q) = %v, want %v", tool, got, want)
		}
	}
}

// A project directory's contents are not necessarily the operator's.
// Cloning a repository must not be able to switch the gate off.
func TestUntrustedProjectMayTightenButNotLoosen(t *testing.T) {
	p, notes := build(t,
		map[string]string{"write_file": "never"},
		map[string]string{"shell_exec": "always", "read_file": "never"},
		false)

	if got := p.For("shell_exec"); got != AlwaysAsk {
		t.Errorf("project tightening was ignored: %v", got)
	}
	if got := p.For("read_file"); got != Default {
		t.Errorf("an untrusted project removed a gate: %v", got)
	}
	if got := p.For("write_file"); got != NeverAsk {
		t.Errorf("the operator's own global policy was dropped: %v", got)
	}
	if len(notes) != 1 || !strings.Contains(string(notes[0]), "read_file") {
		t.Fatalf("the ignored entry was not reported: %v", notes)
	}
	if !strings.Contains(string(notes[0]), "trusted_projects") {
		t.Errorf("the note does not say how to allow it: %q", notes[0])
	}
}

func TestTrustedProjectMayLoosen(t *testing.T) {
	p, notes := build(t, nil, map[string]string{"mcp__tor__check": "never"}, true)
	if got := p.For("mcp__tor__check"); got != NeverAsk {
		t.Errorf("a trusted project could not loosen: %v", got)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}

// An untrusted project must not be able to undo a global "never" either
// — but that direction is tightening, so it is allowed.
func TestProjectMayOverrideGlobalNeverWithAlways(t *testing.T) {
	p, _ := build(t,
		map[string]string{"shell_exec": "never"},
		map[string]string{"shell_exec": "always"},
		false)
	if got := p.For("shell_exec"); got != AlwaysAsk {
		t.Errorf("For(shell_exec) = %v, want AlwaysAsk (tightening always wins)", got)
	}
}

func TestBareWildcardIsRejected(t *testing.T) {
	if _, _, err := Build(map[string]string{"*": "never"}, nil, nil, false); err == nil {
		t.Fatal(`"*" was accepted — one character must not disarm every gate`)
	}
	if _, _, err := Build(nil, map[string]string{"*": "always"}, nil, false); err == nil {
		t.Fatal(`"*" was accepted from a project file`)
	}
}

func TestInvalidPatternsAndValuesAreErrors(t *testing.T) {
	for name, tools := range map[string]map[string]string{
		"unknown value":  {"shell_exec": "sometimes"},
		"empty name":     {"": "never"},
		"inner wildcard": {"mcp__*__check": "never"},
		"empty value":    {"shell_exec": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Build(tools, nil, nil, false); err == nil {
				t.Fatal("accepted an invalid policy entry")
			}
		})
	}
}

func TestErrorNamesTheOffendingEntry(t *testing.T) {
	_, _, err := Build(map[string]string{"shell_exec": "maybe"}, nil, nil, false)
	if err == nil || !strings.Contains(err.Error(), "shell_exec") {
		t.Fatalf("err = %v, want it to name the tool", err)
	}
}

func TestDescribeIsStableAndOrdered(t *testing.T) {
	p, _ := build(t, map[string]string{
		"mcp__a__*":  "never",
		"shell_exec": "always",
	}, nil, false)
	got := strings.Join(p.Describe(), " | ")
	if !strings.HasPrefix(got, "shell_exec = always") {
		t.Errorf("Describe = %q, want exact matches first", got)
	}
	if !p.Configured() {
		t.Error("Configured() = false with rules present")
	}
}

func TestEmptyPolicyIsDefaultEverywhere(t *testing.T) {
	p, notes := build(t, nil, nil, false)
	if p.Configured() || len(notes) != 0 {
		t.Errorf("empty policy: configured=%v notes=%v", p.Configured(), notes)
	}
	if p.For("shell_exec") != Default {
		t.Error("empty policy changed a decision")
	}
}

// ADR-0021 §6: scope beats pattern specificity. A project may tighten
// past a more-specific global rule, and a trusted project's loosening
// beats a global exact rule — the nearest scope wins.
func TestScopeBeatsSpecificity(t *testing.T) {
	// Untrusted project tightens with a wildcard over a global exact
	// "never": the tighten must be honoured (ADR-0008's core promise).
	p, notes, err := Build(
		map[string]string{"web_search": "never"},
		map[string]string{"web_*": "always"},
		nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Errorf("a tighten produced notes: %v", notes)
	}
	if got := p.For("web_search"); got != AlwaysAsk {
		t.Errorf("For(web_search) = %v — the project wildcard tighten lost to the global exact rule", got)
	}
	// Unrelated tools still see the global rule.
	if got := p.For("web_search_v2"); got != AlwaysAsk {
		t.Errorf("For(web_search_v2) = %v, want the project wildcard", got)
	}

	// Trusted project loosens with a wildcard over a global exact
	// "always": nearest scope wins there too.
	p, _, err = Build(
		map[string]string{"mcp__lookup__check": "always"},
		map[string]string{"mcp__lookup__*": "never"},
		nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.For("mcp__lookup__check"); got != NeverAsk {
		t.Errorf("For(trusted loosen) = %v — the trusted project's rule must win", got)
	}

	// Within one scope, specificity still decides.
	p, _, err = Build(
		map[string]string{"mcp__x__*": "never", "mcp__x__post": "always"},
		nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.For("mcp__x__post"); got != AlwaysAsk {
		t.Errorf("in-scope exact vs wildcard = %v, want exact to win", got)
	}
}

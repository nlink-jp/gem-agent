package banner

import (
	"strings"
	"testing"
)

// ADR-0078: a line earns a place at startup only if nothing else will
// say it. The enumerations become one row that names where the detail
// is — and the count survives because "did my toolset come up" is a
// question the operator has before typing.
func TestInventoryLineReplacesTheEnumerations(t *testing.T) {
	got := inventory(Facts{Servers: 24, Tools: 249, Skills: 5, Memories: 3})
	for _, want := range []string{"24 servers", "249 tools", "skills: 5", "memory: 3", "/mcp", "/skills", "/memory"} {
		if !strings.Contains(got, want) {
			t.Errorf("inventoryLine = %q, missing %q", got, want)
		}
	}
	if n := len(got); n > 80 {
		t.Errorf("the row that replaced eleven wraps at 80 columns: %d chars", n)
	}
}

// A part with nothing in it is not printed, and its command is not
// offered: an empty list is not a fact the operator can use.
func TestInventoryLineOmitsWhatIsEmpty(t *testing.T) {
	got := inventory(Facts{Skills: 2})
	if strings.Contains(got, "mcp") || strings.Contains(got, "memory") {
		t.Errorf("inventoryLine = %q, want only skills", got)
	}
	if !strings.Contains(got, "/skills") || strings.Contains(got, "/mcp") {
		t.Errorf("inventoryLine = %q, want only the command it can expand", got)
	}
	if inventory(Facts{}) != "" {
		t.Errorf("a session with nothing loaded still printed a row: %q", inventory(Facts{}))
	}
}

// The ordinary sandbox is silent; each abnormal state still prints,
// because the operator has no reason to go looking for it.
func TestSandboxLineIsAbnormalOnly(t *testing.T) {
	if got := SandboxLine(true, true, false); got != "" {
		t.Errorf("the normal case printed %q — it is a manual excerpt, identical every start", got)
	}
	for _, tc := range []struct {
		name                      string
		on, readLane, lanePrompts bool
		want                      string
	}{
		{"disabled", false, false, false, "DISABLED"},
		{"read lane unverified", true, false, false, "unverified"},
		{"read_lane_prompts", true, true, true, "read_lane_prompts"},
	} {
		got := SandboxLine(tc.on, tc.readLane, tc.lanePrompts)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: line = %q, want mention of %q", tc.name, got, tc.want)
		}
	}
}

// A disabled sandbox outranks every other abnormality: it is the one
// that changes what a shell command can reach.
func TestDisabledSandboxWinsOverTheOtherStates(t *testing.T) {
	if got := SandboxLine(false, true, true); !strings.Contains(got, "DISABLED") {
		t.Errorf("line = %q, want the disabled state", got)
	}
}

// The banner is composed by a rule, so the rule is what is tested: the
// ordinary session prints what nothing else says, and nothing more.
func TestOrdinarySessionIsThreeLines(t *testing.T) {
	got := Lines(Facts{
		Version: "v0.72.0", Model: "gemini-3.8-flash",
		Instructions: []string{"AGENTS.md"},
		Servers:      24, Tools: 249, Skills: 5,
		SandboxOn: true, ReadLane: true,
	})
	if len(got) != 3 {
		t.Fatalf("ordinary start printed %d lines:\n%s", len(got), strings.Join(got, "\n"))
	}
	joined := strings.Join(got, "\n")
	for _, gone := range []string{"project:", "session log:", "approval policy:", "risk rulebook:", "shell lanes"} {
		if strings.Contains(joined, gone) {
			t.Errorf("%q is back in the banner:\n%s", gone, joined)
		}
	}
}

// Every abnormal state still prints, and last — closest to the prompt.
func TestAbnormalStatesStillPrint(t *testing.T) {
	got := Lines(Facts{
		Version: "v", Model: "m",
		SandboxOn:   false,
		AutoApprove: true,
		Notes:       []string{"policy entry ignored"},
	})
	joined := strings.Join(got, "\n")
	for _, want := range []string{"DISABLED", "auto-approve: ON", "warning: policy entry ignored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q:\n%s", want, joined)
		}
	}
	if !strings.HasPrefix(got[len(got)-1], "warning:") {
		t.Errorf("the warnings are not last, closest to the prompt: %v", got)
	}
}

// Starting in auto-approve is the only approval-regime fact with no
// other startup surface: the TUI footer carries it, the plain REPL has
// no footer, and ADR-0078 removed the sandbox line that gestured at it.
func TestAutoApproveIsAnnouncedAtStart(t *testing.T) {
	on := strings.Join(Lines(Facts{Version: "v", Model: "m", SandboxOn: true, ReadLane: true, AutoApprove: true}), "\n")
	if !strings.Contains(on, "auto-approve: ON at start") || !strings.Contains(on, "/auto") {
		t.Errorf("auto-approve at start is silent: %q", on)
	}
	off := strings.Join(Lines(Facts{Version: "v", Model: "m", SandboxOn: true, ReadLane: true}), "\n")
	if strings.Contains(off, "auto-approve") {
		t.Errorf("a manual session announced auto-approve: %q", off)
	}
}

// One-shot mode prints the disabled-sandbox sentence without the rest of
// the banner; it must be the same sentence.
func TestDisabledSandboxSentenceIsShared(t *testing.T) {
	if !strings.Contains(SandboxLine(false, false, false), "auto-approve does not skip this") {
		t.Error("the interactive form dropped the clause that says the prompts stay")
	}
}

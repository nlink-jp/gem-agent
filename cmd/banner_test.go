package cmd

import (
	"strings"
	"testing"
)

// ADR-0078: a line earns a place at startup only if nothing else will
// say it. The enumerations become one row that names where the detail
// is — and the count survives because "did my toolset come up" is a
// question the operator has before typing.
func TestInventoryLineReplacesTheEnumerations(t *testing.T) {
	got := inventoryLine(24, 249, 5, 3)
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
	got := inventoryLine(0, 0, 2, 0)
	if strings.Contains(got, "mcp") || strings.Contains(got, "memory") {
		t.Errorf("inventoryLine = %q, want only skills", got)
	}
	if !strings.Contains(got, "/skills") || strings.Contains(got, "/mcp") {
		t.Errorf("inventoryLine = %q, want only the command it can expand", got)
	}
	if inventoryLine(0, 0, 0, 0) != "" {
		t.Errorf("a session with nothing loaded still printed a row: %q", inventoryLine(0, 0, 0, 0))
	}
}

// The ordinary sandbox is silent; each abnormal state still prints,
// because the operator has no reason to go looking for it.
func TestSandboxLineIsAbnormalOnly(t *testing.T) {
	if got := sandboxAbnormalLine(true, true, false); got != "" {
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
		got := sandboxAbnormalLine(tc.on, tc.readLane, tc.lanePrompts)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: line = %q, want mention of %q", tc.name, got, tc.want)
		}
	}
}

// A disabled sandbox outranks every other abnormality: it is the one
// that changes what a shell command can reach.
func TestDisabledSandboxWinsOverTheOtherStates(t *testing.T) {
	if got := sandboxAbnormalLine(false, true, true); !strings.Contains(got, "DISABLED") {
		t.Errorf("line = %q, want the disabled state", got)
	}
}

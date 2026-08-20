package cmd

import (
	"strings"
	"testing"
)

// The banner is a glance: a policy grown by 'p' answers and MCP
// wildcards must summarise, not dump (operator report).
func TestPolicyBannerLineSummarises(t *testing.T) {
	few := []string{"web_search = never", "web_fetch = never"}
	if got := policyBannerLine(few); !strings.Contains(got, "web_fetch = never") || strings.Contains(got, "rules total") {
		t.Errorf("short policy must list in full: %q", got)
	}
	many := []string{"a = never", "b = never", "c = always", "d = never", "e = never", "f = always"}
	got := policyBannerLine(many)
	if !strings.Contains(got, "6 rules total") || !strings.Contains(got, "/tools") {
		t.Errorf("long policy must summarise with the count and pointer: %q", got)
	}
	if strings.Contains(got, "f = always") {
		t.Errorf("summary still dumps the tail: %q", got)
	}
}

package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// ADR-0083 §5: the trigger names the condition and the two tools, says
// nothing about any other path, and forbids nothing.
func TestOnRequestTriggerWording(t *testing.T) {
	for _, want := range []string{"find_tools", "mcp_load", "no tool for it is in your tool list", "in your own words"} {
		if !strings.Contains(mcpOnRequestTrigger, want) {
			t.Errorf("trigger lacks %q", want)
		}
	}
	for _, banned := range []string{"do not", "never", "work around", "workaround", "instead of", "before working"} {
		if strings.Contains(strings.ToLower(mcpOnRequestTrigger), banned) {
			t.Errorf("trigger names a competing path or forbids: %q", banned)
		}
	}
	if mcpOnRequestSection(nil) != "" {
		t.Error("no servers, no section")
	}
	sec := mcpOnRequestSection([]string{"- whois-lookup (1 tools)", "- slack (24 tools)"})
	if !strings.Contains(sec, "- slack (24 tools)") || !strings.Contains(sec, mcpOnRequestTrigger) {
		t.Errorf("section = %q", sec)
	}
}

// Nothing set, nothing changed: the predicate and the hook are nil
// under "all", so the agent declares every registered tool as before.
func TestOptInLeavesTheAgentUntouched(t *testing.T) {
	adv := newMCPAdvertiser(false, nil, nil, nil)
	all := &config.Config{MCP: config.MCPConfig{Advertise: "all"}}
	if advertisePredicate(all, adv) != nil || afterToolHook(all, adv) != nil {
		t.Error("under all, the agent must get nil for both")
	}
	on := &config.Config{MCP: config.MCPConfig{Advertise: "on-request"}}
	if advertisePredicate(on, adv) == nil || afterToolHook(on, adv) == nil {
		t.Error("under on-request, the agent must get both")
	}
}

func TestLibrarianLanguage(t *testing.T) {
	if librarianLanguage(uitext.JA) != "Japanese" || librarianLanguage(uitext.EN) != "English" {
		t.Error("language names")
	}
}

// /settings rows say what an unset value means (ADR-0009).
func TestLibrarianAndPreloadLabels(t *testing.T) {
	if got := librarianModelLabel(config.ModelConfig{}); got != "(model tier)" {
		t.Errorf("unset librarian = %q", got)
	}
	if got := librarianThinkingLabel(config.ModelConfig{}); got != "(follows the model tier)" {
		t.Errorf("unset level, no slot = %q", got)
	}
	if got := librarianThinkingLabel(config.ModelConfig{Librarian: "judge"}); got != "(model default)" {
		t.Errorf("unset level with a slot = %q", got)
	}
	if got := preloadLabel(nil); got != "(none)" {
		t.Errorf("empty preload = %q", got)
	}
	if got := preloadLabel([]string{"a", "b"}); got != "a, b" {
		t.Errorf("preload = %q", got)
	}
}

// /mcp load <server> routes to the operator's override; a bare
// "/mcp load" is a usage error, and the listing carries each server's
// status and the withheld tools.
func TestSlashMCPLoadAndStatus(t *testing.T) {
	msgs := uitext.For(uitext.EN)
	var loaded string
	reload := slashReloads{
		mcpLoad:   func(s string) string { loaded = s; return s + " loaded\n" },
		mcpStatus: func(s string) string { return map[string]string{"slack": "not loaded"}[s] },
		mcpWithheld: func() []WithheldTool {
			return []WithheldTool{{Name: "mcp__helper__exec", Why: "addresses the assistant"}}
		},
	}
	out, isErr, _ := slashOutput("/mcp load slack", nil, nil, nil, nil, reload, nil, nil, nil, "v", msgs, nil)
	if isErr || loaded != "slack" || !strings.Contains(out, "slack loaded") {
		t.Errorf("/mcp load: out=%q err=%v loaded=%q", out, isErr, loaded)
	}
	if _, isErr, _ := slashOutput("/mcp load", nil, nil, nil, nil, reload, nil, nil, nil, "v", msgs, nil); !isErr {
		t.Error("bare /mcp load must be a usage error")
	}
	out, _, _ = slashOutput("/mcp", nil, nil, []string{"slack [global] (24 tools)", "whois-lookup [global] (1 tools)"}, nil, reload, nil, nil, nil, "v", msgs, nil)
	if !strings.Contains(out, "slack [global] (24 tools) — not loaded") || !strings.Contains(out, "whois-lookup [global] (1 tools)\n") {
		t.Errorf("/mcp listing:\n%s", out)
	}
	if !strings.Contains(out, "withheld: mcp__helper__exec — addresses the assistant") {
		t.Errorf("/mcp listing lacks the withheld tool:\n%s", out)
	}
}

// agent_info names the advertised set under on-request and stays
// silent about it under "all".
func TestRenderInfoMCPCounts(t *testing.T) {
	s := infoFixture()
	if strings.Contains(renderInfo(s), "mcp tools:") {
		t.Error("under all, no mcp tools line")
	}
	s.MCPOnRequest, s.MCPRegistered, s.MCPAdvertised, s.MCPWithheld = true, 243, 12, 2
	if out := renderInfo(s); !strings.Contains(out, "mcp tools: 243 registered · 12 in your tool list · 2 withheld") {
		t.Errorf("info lacks the counts:\n%s", out)
	}
}

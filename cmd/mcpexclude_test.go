package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
)

func obsidianTools() []mcp.Tool {
	return []mcp.Tool{
		{Name: "get_vault_file"},
		{Name: "search_vault"},
		{Name: "search_and_replace"},
		{Name: "patch_vault_file"},
	}
}

func mustFilter(t *testing.T, cfg, policy, project []string) mcpfilter.Filter {
	t.Helper()
	f, err := mcpfilter.Build(cfg, mcpfilter.PolicyScope{Entries: policy}, project)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// ADR-0077 §1: the two levels are a server and a function of a server;
// a function entry removes exactly that function.
func TestSplitByFilterRemovesNamedFunctions(t *testing.T) {
	f := mustFilter(t, []string{"obsidian/patch_vault_file", "obsidian/search_and_replace"}, nil, nil)
	offered, kept, excluded := splitByFilter("obsidian", obsidianTools(), f)

	if len(offered) != 4 {
		t.Errorf("offered = %v, want every advertised name", offered)
	}
	var names []string
	for _, k := range kept {
		names = append(names, k.Name)
	}
	if strings.Join(names, ",") != "get_vault_file,search_vault" {
		t.Errorf("kept = %v, want the two read functions", names)
	}
	if len(excluded) != 2 {
		t.Fatalf("excluded = %v, want two", excluded)
	}
}

// The filter matches the name the server spells; what is recorded is the
// registry name the executor will see. Filing the exclusion under the
// server's own spelling would file it under a name no call ever carries.
func TestSplitByFilterRecordsRegistryNames(t *testing.T) {
	f := mustFilter(t, []string{"obsidian/patch_vault_file"}, nil, nil)
	_, _, excluded := splitByFilter("obsidian", obsidianTools(), f)
	if len(excluded) != 1 || excluded[0] != mcpToolName("obsidian", "patch_vault_file") {
		t.Errorf("excluded = %v, want the registry name", excluded)
	}
	if !strings.HasPrefix(excluded[0], "mcp__") {
		t.Errorf("excluded name %q is not a registry name", excluded[0])
	}
}

// A server name with characters Gemini's function names disallow is
// sanitized into the registry name; the exclusion must follow it there.
func TestSplitByFilterFollowsNameSanitizing(t *testing.T) {
	f := mustFilter(t, []string{"slack-extender-nlink-jp/slack_send_message"}, nil, nil)
	_, kept, excluded := splitByFilter("slack-extender-nlink-jp",
		[]mcp.Tool{{Name: "slack_send_message"}, {Name: "slack_read_channel"}}, f)
	if len(kept) != 1 || kept[0].Name != "slack_read_channel" {
		t.Errorf("kept = %v, want only the read tool", kept)
	}
	if len(excluded) != 1 || excluded[0] != mcpToolName("slack-extender-nlink-jp", "slack_send_message") {
		t.Errorf("excluded = %v, want the sanitized registry name", excluded)
	}
}

// A whole-server exclusion covers every function of it, which is what
// lets the connect loop skip the server without listing it at all.
func TestSplitByFilterWholeServer(t *testing.T) {
	f := mustFilter(t, []string{"obsidian"}, nil, nil)
	if !f.Server("obsidian") {
		t.Fatal("whole-server entry did not exclude the server")
	}
	_, kept, excluded := splitByFilter("obsidian", obsidianTools(), f)
	if len(kept) != 0 || len(excluded) != 4 {
		t.Errorf("kept = %v, excluded = %v, want everything excluded", kept, excluded)
	}
}

// Nothing configured means nothing removed: an operator who sets nothing
// sees today's behaviour, and .mcp.json keeps the meaning it has.
func TestSplitByFilterEmptyKeepsEverything(t *testing.T) {
	f := mustFilter(t, nil, nil, nil)
	_, kept, excluded := splitByFilter("obsidian", obsidianTools(), f)
	if len(kept) != 4 || len(excluded) != 0 {
		t.Errorf("kept = %d, excluded = %d, want all four kept", len(kept), len(excluded))
	}
}

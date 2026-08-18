package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "sekrit")
	path := filepath.Join(t.TempDir(), ".mcp.json")
	content := `{
  "mcpServers": {
    "tor-exit": {
      "command": "tor-exit-lookup",
      "args": ["mcp"]
    },
    "with-env": {
      "type": "stdio",
      "command": "${MCP_TEST_TOKEN}-cmd",
      "args": ["--token", "${MCP_TEST_TOKEN}"],
      "env": {"API_KEY": "${MCP_TEST_TOKEN}"}
    },
    "remote": {
      "type": "http",
      "url": "https://example.com/mcp"
    },
    "broken": {
      "args": ["no-command"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	servers, skipped, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("servers = %+v", servers)
	}
	if servers["tor-exit"].Command != "tor-exit-lookup" || servers["tor-exit"].Args[0] != "mcp" {
		t.Errorf("tor-exit = %+v", servers["tor-exit"])
	}
	we := servers["with-env"]
	if we.Command != "sekrit-cmd" || we.Args[1] != "sekrit" || we.Env["API_KEY"] != "sekrit" {
		t.Errorf("env expansion failed: %+v", we)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %v (want http transport + missing command)", skipped)
	}
}

func TestMergeProjectWinsCollision(t *testing.T) {
	global := map[string]ServerConfig{
		"tor-exit": {Command: "tor-exit-lookup", Args: []string{"mcp"}},
		"asn":      {Command: "asn-lookup", Args: []string{"mcp"}},
	}
	project := map[string]ServerConfig{
		"tor-exit": {Command: "/custom/tor-exit-lookup", Args: []string{"mcp"}},
		"local":    {Command: "my-server"},
	}
	merged, scopes, overridden := Merge(global, project)
	if len(merged) != 3 {
		t.Fatalf("merged = %d servers, want 3", len(merged))
	}
	if merged["tor-exit"].Command != "/custom/tor-exit-lookup" {
		t.Error("project entry must win the name collision")
	}
	if scopes["asn"] != "global" || scopes["local"] != "project" || scopes["tor-exit"] != "project" {
		t.Errorf("scopes = %v", scopes)
	}
	if len(overridden) != 1 || overridden[0] != "tor-exit" {
		t.Errorf("overridden = %v", overridden)
	}
}

func TestMergeEmptySides(t *testing.T) {
	merged, scopes, overridden := Merge(nil, map[string]ServerConfig{"s": {Command: "c"}})
	if len(merged) != 1 || scopes["s"] != "project" || overridden != nil {
		t.Errorf("project-only merge wrong: %v %v %v", merged, scopes, overridden)
	}
	merged, scopes, _ = Merge(map[string]ServerConfig{"g": {Command: "c"}}, nil)
	if len(merged) != 1 || scopes["g"] != "global" {
		t.Errorf("global-only merge wrong: %v %v", merged, scopes)
	}
}

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	servers, skipped, err := LoadConfig(filepath.Join(t.TempDir(), ".mcp.json"))
	if err != nil || len(servers) != 0 || skipped != nil {
		t.Fatalf("missing file should be empty: %v %v %v", servers, skipped, err)
	}
}

func TestLoadConfigUnknownKeysIgnored(t *testing.T) {
	// .mcp.json is Claude Code's format — foreign files may carry keys
	// we do not know; they must not be errors (unlike our own config).
	path := filepath.Join(t.TempDir(), ".mcp.json")
	content := `{"mcpServers": {"s": {"command": "c", "futureField": true}}, "otherTopLevel": 1}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	servers, _, err := LoadConfig(path)
	if err != nil || len(servers) != 1 {
		t.Fatalf("unknown keys should be tolerated: %v %v", servers, err)
	}
}

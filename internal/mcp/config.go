// Package mcp implements a stdio MCP client and the Claude Code
// `.mcp.json` configuration format — the drop-in half of the RFP: an
// existing project's MCP setup works without gem-agent-specific config.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerConfig is one entry under "mcpServers" in .mcp.json.
type ServerConfig struct {
	// Type is the transport. Empty and "stdio" are supported; other
	// transports (sse, http) are skipped with a warning — recorded as
	// out of scope for Phase 2.
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

type mcpFile struct {
	MCPServers map[string]ServerConfig `json:"mcpServers"`
}

// GlobalConfigPath is the user-scope server list: same format as a
// project's .mcp.json, so entries can be copied verbatim between the
// two (and from a Claude Code setup).
func GlobalConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gem-agent", "mcp.json")
}

// Merge combines the global and project server maps. On a name
// collision the project entry wins — the more specific scope overrides
// the broader one. Returns the merged map, each server's scope
// ("global" or "project"), and the names the project overrode.
func Merge(global, project map[string]ServerConfig) (merged map[string]ServerConfig, scopes map[string]string, overridden []string) {
	merged = map[string]ServerConfig{}
	scopes = map[string]string{}
	for name, sc := range global {
		merged[name] = sc
		scopes[name] = "global"
	}
	for name, sc := range project {
		if _, exists := merged[name]; exists {
			overridden = append(overridden, name)
		}
		merged[name] = sc
		scopes[name] = "project"
	}
	return merged, scopes, overridden
}

// LoadConfig reads a .mcp.json file. A missing file yields an empty map
// and no error (projects without MCP are the common case). Returns the
// usable stdio servers and the names of skipped entries (unsupported
// transport or missing command).
//
// ${VAR} references in command, args, and env values are expanded from
// the environment, matching Claude Code's behaviour. This file format is
// owned by another tool, so unknown keys are ignored rather than
// rejected — the strict-decode rule applies to our own config only.
func LoadConfig(path string) (servers map[string]ServerConfig, skipped []string, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]ServerConfig{}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f mcpFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}

	servers = map[string]ServerConfig{}
	for name, sc := range f.MCPServers {
		if sc.Type != "" && sc.Type != "stdio" {
			skipped = append(skipped, name+" (transport "+sc.Type+")")
			continue
		}
		if sc.Command == "" {
			skipped = append(skipped, name+" (no command)")
			continue
		}
		sc.Command = expandEnv(sc.Command)
		args := make([]string, len(sc.Args))
		for i, a := range sc.Args {
			args[i] = expandEnv(a)
		}
		sc.Args = args
		env := make(map[string]string, len(sc.Env))
		for k, v := range sc.Env {
			env[k] = expandEnv(v)
		}
		sc.Env = env
		servers[name] = sc
	}
	return servers, skipped, nil
}

// expandEnv expands ${VAR} and $VAR like os.ExpandEnv, plus Claude
// Code's ${VAR:-default} — an entry copied from a Claude Code setup
// must keep its default rather than lose the argument (review round 4:
// os.ExpandEnv looked up a variable literally named "VAR:-default").
// An unset variable without a default expands to the empty string, as
// before.
func expandEnv(s string) string {
	return os.Expand(s, func(name string) string {
		if i := strings.Index(name, ":-"); i >= 0 {
			if v, ok := os.LookupEnv(name[:i]); ok && v != "" {
				return v
			}
			return name[i+2:]
		}
		return os.Getenv(name)
	})
}

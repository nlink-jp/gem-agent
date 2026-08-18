// Package mcp implements a stdio MCP client and the Claude Code
// `.mcp.json` configuration format — the drop-in half of the RFP: an
// existing project's MCP setup works without gem-agent-specific config.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
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
		sc.Command = os.ExpandEnv(sc.Command)
		args := make([]string, len(sc.Args))
		for i, a := range sc.Args {
			args[i] = os.ExpandEnv(a)
		}
		sc.Args = args
		env := make(map[string]string, len(sc.Env))
		for k, v := range sc.Env {
			env[k] = os.ExpandEnv(v)
		}
		sc.Env = env
		servers[name] = sc
	}
	return servers, skipped, nil
}

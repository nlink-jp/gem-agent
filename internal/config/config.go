// Package config loads gem-agent configuration following the org-standard
// Vertex AI schema: ~/.config/gem-agent/config.toml with env precedence
// GEMAGENT_* > GOOGLE_CLOUD_* > config file > built-in defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the gem-agent configuration.
type Config struct {
	GCP      GCPConfig      `toml:"gcp"`
	Model    ModelConfig    `toml:"model"`
	Sandbox  SandboxConfig  `toml:"sandbox"`
	Agent    AgentConfig    `toml:"agent"`
	MCP      MCPConfig      `toml:"mcp"`
	TUI      TUIConfig      `toml:"tui"`
	Approval ApprovalConfig `toml:"approval"`
}

// ApprovalConfig carries the per-tool approval policy (ADR-0008).
type ApprovalConfig struct {
	// Tools maps a tool name — or a trailing-wildcard prefix such as
	// "mcp__tor-exit-lookup__*" — to "always" or "never".
	Tools map[string]string `toml:"tools"`
	// TrustedProjects lists project directories whose own
	// .gem-agent.toml may REMOVE approvals. Everywhere else a project
	// file may only add them: a directory's contents are not necessarily
	// written by the operator, and cloning a repository must not be able
	// to switch the gate off (ADR-0008 §4).
	TrustedProjects []string `toml:"trusted_projects"`
}

// ProjectConfig is <project>/.gem-agent.toml — the project-scoped half
// of ADR-0008. Deliberately tiny: it carries policy, nothing else. Model
// names, credentials and sandbox settings stay in the operator's own
// config, where a checked-out repository cannot reach them.
type ProjectConfig struct {
	Approval ProjectApproval `toml:"approval"`
}

// ProjectApproval is the project file's [approval] table.
type ProjectApproval struct {
	Tools map[string]string `toml:"tools"`
}

// ProjectFileName is the project-scoped config file.
const ProjectFileName = ".gem-agent.toml"

// LoadProject reads <dir>/.gem-agent.toml. A missing file is not an
// error. Unknown keys are, for the same reason as in the main config:
// a policy that does not do what it says is worse than no policy.
func LoadProject(dir string) (*ProjectConfig, error) {
	path := filepath.Join(dir, ProjectFileName)
	var cfg ProjectConfig
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	md, err := toml.DecodeFile(path, &cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("unknown key(s) in %s: %s (this file carries [approval.tools] only)",
			path, strings.Join(keys, ", "))
	}
	return &cfg, nil
}

// TrustsProject reports whether the operator listed dir as a project
// whose own policy file may remove approvals.
func (c *Config) TrustsProject(dir string) bool {
	for _, p := range c.Approval.TrustedProjects {
		if p == dir {
			return true
		}
	}
	return false
}

// TUIConfig controls the interactive UI appearance.
type TUIConfig struct {
	// Theme: "auto" (detect background before startup), "dark", "light",
	// or "plain" (no colors at all — the escape hatch for terminal
	// themes that fight any styling).
	Theme string `toml:"theme"`
}

// MCPConfig controls the MCP client. Server definitions live in the
// project's .mcp.json (Claude Code format), not here — drop-in
// compatibility is the point.
type MCPConfig struct {
	Enabled        bool `toml:"enabled"`
	CallTimeoutSec int  `toml:"call_timeout_sec"`
}

// GCPConfig identifies the Vertex AI project.
type GCPConfig struct {
	Project  string `toml:"project"`
	Location string `toml:"location"`
}

// ModelConfig selects the Gemini model. The name is always config-driven —
// Gemini model IDs churn (2.5 retires 2026-10), so there is no built-in
// default and hardcoding one anywhere else is a bug.
type ModelConfig struct {
	Name string `toml:"name"`
	// ContextWindow overrides the context window size shown in the TUI
	// footer. 0 (default) auto-detects from the model metadata.
	ContextWindow int `toml:"context_window"`
	// Safety selects the configurable content-filter thresholds:
	// "default" (the provider's own), "relaxed" (block only high-
	// confidence hits), or "off". Security work trips the dangerous-
	// content filter on ordinary material, so the escape hatch is
	// explicit rather than silent — and some filters cannot be turned
	// off at any setting.
	Safety string `toml:"safety"`
}

// SandboxConfig controls the sandbox-exec wrapper for shell_exec.
type SandboxConfig struct {
	Enabled bool `toml:"enabled"`
}

// AgentConfig holds agent-loop tunables.
type AgentConfig struct {
	MaxTurns        int `toml:"max_turns"`
	ShellTimeoutSec int `toml:"shell_timeout_sec"`
	// AutoApprove starts sessions in auto-approve mode (ADR-0004).
	// Default false: weakening the primary defense is opt-in.
	AutoApprove bool `toml:"auto_approve"`
	// AutoCompact summarises older history when the context window fills
	// (ADR-0006). Default true — unlike auto-approve this weakens no
	// defense, and a session that dies at the window is not a fallback.
	AutoCompact bool `toml:"auto_compact"`
	// CompactAtPct is the share of the model's input window at which
	// compaction fires.
	CompactAtPct int `toml:"compact_at_pct"`
}

// DefaultPath returns the org-standard per-tool config path.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "gem-agent", "config.toml"), nil
}

func defaults() Config {
	return Config{
		// "global": Vertex AI serves the Gemini 3 family (gem-agent's
		// target, RFP §3) only from the global endpoint — regional
		// endpoints 404 them (measured 2026-08 with gemini-3-flash-preview
		// and gemini-3.7-flash). Gemini 2.5 users set a regional location.
		GCP:     GCPConfig{Location: "global"},
		Model:   ModelConfig{Safety: "default"},
		Sandbox: SandboxConfig{Enabled: true},
		Agent:   AgentConfig{MaxTurns: 50, ShellTimeoutSec: 120, AutoCompact: true, CompactAtPct: 80},
		MCP:     MCPConfig{Enabled: true, CallTimeoutSec: 60},
		TUI:     TUIConfig{Theme: "auto"},
	}
}

// Overrides carries CLI-flag values, which sit at the top of the
// precedence order: flags > GEMAGENT_* > GOOGLE_CLOUD_* > file > defaults.
type Overrides struct {
	Model string
}

// Load reads the config with no CLI overrides.
func Load(path string) (*Config, error) {
	return LoadWithOverrides(path, Overrides{})
}

// LoadWithOverrides reads the config file at path (missing file is not an
// error — env vars alone can carry a complete config), applies env and
// flag overrides, and validates. Unknown keys in the file are an error
// (strict decode): a typo like [modle] silently ignored would surface as
// a confusing runtime failure far from its cause.
func LoadWithOverrides(path string, ov Overrides) (*Config, error) {
	cfg := defaults()

	if _, err := os.Stat(path); err == nil {
		md, err := toml.DecodeFile(path, &cfg)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if undecoded := md.Undecoded(); len(undecoded) > 0 {
			keys := make([]string, len(undecoded))
			for i, k := range undecoded {
				keys[i] = k.String()
			}
			return nil, fmt.Errorf("unknown key(s) in %s: %s", path, strings.Join(keys, ", "))
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	applyEnv(&cfg)

	if ov.Model != "" {
		cfg.Model.Name = ov.Model
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnv applies the org-standard precedence: GEMAGENT_<FIELD> beats
// GOOGLE_CLOUD_<FIELD>, which beats the file value.
func applyEnv(cfg *Config) {
	if v := os.Getenv("GOOGLE_CLOUD_PROJECT"); v != "" {
		cfg.GCP.Project = v
	}
	if v := os.Getenv("GOOGLE_CLOUD_LOCATION"); v != "" {
		cfg.GCP.Location = v
	}
	if v := os.Getenv("GEMAGENT_PROJECT"); v != "" {
		cfg.GCP.Project = v
	}
	if v := os.Getenv("GEMAGENT_LOCATION"); v != "" {
		cfg.GCP.Location = v
	}
	if v := os.Getenv("GEMAGENT_MODEL"); v != "" {
		cfg.Model.Name = v
	}
}

func (c *Config) validate() error {
	var missing []string
	if c.GCP.Project == "" {
		missing = append(missing, "[gcp].project (or GEMAGENT_PROJECT / GOOGLE_CLOUD_PROJECT)")
	}
	if c.Model.Name == "" {
		missing = append(missing, "[model].name (or GEMAGENT_MODEL)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, "; "))
	}
	if c.Agent.MaxTurns <= 0 {
		return fmt.Errorf("[agent].max_turns must be positive")
	}
	if c.Agent.ShellTimeoutSec <= 0 {
		return fmt.Errorf("[agent].shell_timeout_sec must be positive")
	}
	if c.MCP.CallTimeoutSec <= 0 {
		return fmt.Errorf("[mcp].call_timeout_sec must be positive")
	}
	// Below ~10% compaction would fire constantly and summarise nothing
	// useful; at 100% it would fire after the request that already
	// failed, which is too late to help.
	if c.Agent.CompactAtPct < 10 || c.Agent.CompactAtPct > 99 {
		return fmt.Errorf("[agent].compact_at_pct must be between 10 and 99 (got %d)", c.Agent.CompactAtPct)
	}
	switch c.Model.Safety {
	case "default", "relaxed", "off":
	default:
		return fmt.Errorf("[model].safety must be default, relaxed, or off (got %q)", c.Model.Safety)
	}
	switch c.TUI.Theme {
	case "auto", "dark", "light", "plain":
	default:
		return fmt.Errorf("[tui].theme must be auto, dark, light, or plain (got %q)", c.TUI.Theme)
	}
	if c.Model.ContextWindow < 0 {
		return fmt.Errorf("[model].context_window must not be negative")
	}
	return nil
}

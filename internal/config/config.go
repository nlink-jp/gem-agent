// Package config loads gem-agent configuration following the org-standard
// Vertex AI schema: ~/.config/gem-agent/config.toml with env precedence
// GEMAGENT_* > GOOGLE_CLOUD_* > config file > built-in defaults.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the gem-agent configuration.
type Config struct {
	GCP       GCPConfig       `toml:"gcp"`
	Model     ModelConfig     `toml:"model"`
	Sandbox   SandboxConfig   `toml:"sandbox"`
	Agent     AgentConfig     `toml:"agent"`
	MCP       MCPConfig       `toml:"mcp"`
	TUI       TUIConfig       `toml:"tui"`
	Telemetry TelemetryConfig `toml:"telemetry"`
	Approval  ApprovalConfig  `toml:"approval"`
	Hooks     HooksConfig     `toml:"hooks"`

	// Sources records where each setting's effective value came from,
	// keyed by its TOML path ("model.name"). Four precedence layers with
	// nothing on screen is a design that assumes the operator remembers
	// them; /settings shows this instead (ADR-0009).
	Sources map[string]string `toml:"-"`
}

// Provenance values used in Sources.
const (
	FromFlag    = "flag"
	FromEnv     = "env"
	FromFile    = "config.toml"
	FromDefault = "default"
)

func (c *Config) note(key, source string) {
	if c.Sources == nil {
		c.Sources = map[string]string{}
	}
	c.Sources[key] = source
}

// Source returns where a setting came from, for display.
func (c *Config) Source(key string) string {
	if s, ok := c.Sources[key]; ok {
		return s
	}
	return FromDefault
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
	// Untrusted input read before the trust prompt (ADR-0072 §4.5): a
	// bounded read, then the decode — never the file whole.
	raw, err := readCapped(path, ProjectFileCap)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	md, err := toml.Decode(string(raw), &cfg)
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
// whose own policy file may remove approvals. Both sides are compared
// as resolved paths (ADR-0021): dir arrives symlink-resolved (/tmp is
// /private/tmp on macOS), so raw string equality made a trust entry
// under a symlinked path silently never match — and the startup note
// told the operator to add exactly the path that would not work.
func (c *Config) TrustsProject(dir string) bool {
	canon := canonicalPath(dir)
	for _, p := range c.Approval.TrustedProjects {
		if canonicalPath(expandHome(p)) == canon {
			return true
		}
	}
	return false
}

// canonicalPath cleans and symlink-resolves; a path that does not
// resolve (not yet existing) falls back to the cleaned form.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// expandHome resolves a leading ~ so trusted_projects entries may be
// written the way operators write paths.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// TUIConfig controls the interactive UI appearance.
type TUIConfig struct {
	// Theme: "auto" (detect background before startup), "dark", "light",
	// or "plain" (no colors at all — the escape hatch for terminal
	// themes that fight any styling).
	Theme string `toml:"theme"`
	// Language: "auto" (LC_ALL → LC_MESSAGES → LANG, POSIX-style),
	// "ja", or "en" — the language of the interactive chrome
	// (ADR-0029). Resolved once at startup.
	Language string `toml:"language"`
	// ShowThoughts streams the model's thought summaries into the live
	// area (ADR-0033 §3). Display-only — never stored or replayed.
	ShowThoughts bool `toml:"show_thoughts"`
}

// TelemetryConfig is the audit-log exporter (ADR-0035). Deliberately
// absent from ProjectConfig: the exporter is an egress channel, and a
// cloned repository must not be able to enable it or redirect it.
type TelemetryConfig struct {
	Enabled bool `toml:"enabled"`
	// Backend: "gcp" (default — Cloud Logging in the [gcp] project via
	// the same ADC as Vertex), "otlp-grpc", or "otlp-http".
	Backend  string `toml:"backend"`
	Endpoint string `toml:"endpoint"` // otlp-* only
	Insecure bool   `toml:"insecure"` // otlp-* only
	// HeadersFile: JSON file of OTLP auth headers (mode 0600). A file
	// survives launchd/cron/fresh shells; the env variable does not.
	HeadersFile string `toml:"headers_file"`
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
	// Bucket, when set, routes audio/video attachments through this
	// GCS bucket as gs:// URIs (ADR-0027). Empty = inline media only,
	// under the inline size cap.
	Bucket string `toml:"bucket"`
}

// ModelConfig selects the Gemini model. The name is always config-driven —
// Gemini model IDs churn (2.5 retires 2026-10), so there is no built-in
// default and hardcoding one anywhere else is a bug.
type ModelConfig struct {
	Name string `toml:"name"`
	// ContextWindow overrides the context window size shown in the TUI
	// footer. 0 (default) auto-detects from the model metadata.
	ContextWindow int `toml:"context_window"`
	// Summary names the lightweight model used by the summarize_file
	// tool (ADR-0014). Empty means the main model: the context saving
	// does not depend on the model being cheaper.
	Summary string `toml:"summary"`
	// Safety selects the configurable content-filter thresholds:
	// "default" (the provider's own), "relaxed" (block only high-
	// confidence hits), or "off". Security work trips the dangerous-
	// content filter on ordinary material, so the escape hatch is
	// explicit rather than silent — and some filters cannot be turned
	// off at any setting.
	Safety string `toml:"safety"`
	// Thinking sets the Gemini 3 thinking level for main-model calls
	// (ADR-0025): "minimal", "low", "medium", or "high". Empty means
	// the model's own default. The summary model is unaffected.
	Thinking string `toml:"thinking"`
}

// ValidThinking reports whether s is an accepted [model].thinking value.
func ValidThinking(s string) bool {
	switch s {
	case "", "minimal", "low", "medium", "high":
		return true
	}
	return false
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

// HooksConfig holds operator hooks: pre-tool (ADR-0044) and the two
// context events, session start and prompt submit (ADR-0069). Global
// config only: a project-level hook would let a cloned repository
// execute an arbitrary command on every tool call or turn (ADR-0044 §5).
type HooksConfig struct {
	PreToolUse       []HookEntry `toml:"pre_tool_use"`
	SessionStart     []HookEntry `toml:"session_start"`
	UserPromptSubmit []HookEntry `toml:"user_prompt_submit"`
	SessionEnd       []HookEntry `toml:"session_end"`
}

// HookEntry is one configured hook. For pre_tool_use, Matcher is an
// exact tool name, a "a|b" alternation, or "*", matched against both
// gem-agent's and Claude Code's vocabulary; for session_start it
// selects the source (startup, resume, clear) and may be omitted;
// user_prompt_submit takes none. Command runs via sh -c with the Claude
// Code JSON payload of its event on stdin.
type HookEntry struct {
	Matcher    string `toml:"matcher"`
	Command    string `toml:"command"`
	TimeoutSec int    `toml:"timeout_sec"` // 0 = default
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
		// target, RFP §3) from the global endpoint and the "us" / "eu"
		// multi-regions ("eu" per the Vertex model page); single
		// regions such as us-central1 404 them
		// (measured 2026-08 with gemini-3-flash-preview and
		// gemini-3.7-flash, re-measured 2026-09-04 with gemini-3.8-flash:
		// global and "us" answer, us-central1 404s). Gemini 2.5 users
		// set a regional location.
		GCP:       GCPConfig{Location: "global"},
		Model:     ModelConfig{Safety: "default"},
		Sandbox:   SandboxConfig{Enabled: true},
		Agent:     AgentConfig{MaxTurns: 50, ShellTimeoutSec: 120, AutoCompact: true, CompactAtPct: 80},
		MCP:       MCPConfig{Enabled: true, CallTimeoutSec: 60},
		TUI:       TUIConfig{Theme: "auto", Language: "auto", ShowThoughts: true},
		Telemetry: TelemetryConfig{Backend: "gcp", Endpoint: "localhost:4317"},
	}
}

// Overrides carries CLI-flag values, which sit at the top of the
// precedence order: flags > GEMAGENT_* > GOOGLE_CLOUD_* > file > defaults.
type Overrides struct {
	Model string
	// Thinking overrides [model].thinking for this run. The literal
	// "default" clears a configured level back to the model default —
	// the empty string already means "flag not given", so clearing
	// needs its own word.
	Thinking string
	// MCP overrides [mcp].enabled for this run: "on" or "off"
	// (ADR-0039). "off" is the one-shot pipeline case — no server
	// child is spawned; "on" forces MCP against a config that
	// disables it. Empty means the flag was not given.
	MCP string
	// Auto arms auto-approve for this run (ADR-0053). One-way: the
	// flag can only arm, so false simply means "flag not given" and
	// the config value stands.
	Auto bool
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
		// IsDefined tells us exactly which keys the file set, which is
		// the only honest way to say "this came from the file" rather
		// than "this happens to differ from the default".
		for _, key := range trackedKeys {
			if md.IsDefined(strings.Split(key, ".")...) {
				cfg.note(key, FromFile)
			}
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
		cfg.note("model.name", FromFlag)
	}
	if ov.Thinking != "" {
		if ov.Thinking == "default" {
			cfg.Model.Thinking = ""
		} else {
			cfg.Model.Thinking = ov.Thinking
		}
		cfg.note("model.thinking", FromFlag)
	}
	switch ov.MCP {
	case "":
	case "on", "off":
		cfg.MCP.Enabled = ov.MCP == "on"
		cfg.note("mcp.enabled", FromFlag)
	default:
		return nil, fmt.Errorf("--mcp must be on or off (got %q)", ov.MCP)
	}
	if ov.Auto {
		cfg.Agent.AutoApprove = true
		cfg.note("agent.auto_approve", FromFlag)
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyEnv applies the org-standard precedence: GEMAGENT_<FIELD> beats
// GOOGLE_CLOUD_<FIELD>, which beats the file value.
func applyEnv(cfg *Config) {
	for _, e := range []struct {
		env, key string
		set      func(string)
	}{
		{"GOOGLE_CLOUD_PROJECT", "gcp.project", func(v string) { cfg.GCP.Project = v }},
		{"GOOGLE_CLOUD_LOCATION", "gcp.location", func(v string) { cfg.GCP.Location = v }},
		{"GEMAGENT_PROJECT", "gcp.project", func(v string) { cfg.GCP.Project = v }},
		{"GEMAGENT_LOCATION", "gcp.location", func(v string) { cfg.GCP.Location = v }},
		{"GEMAGENT_MODEL", "model.name", func(v string) { cfg.Model.Name = v }},
	} {
		if v := os.Getenv(e.env); v != "" {
			e.set(v)
			cfg.note(e.key, FromEnv+":"+e.env)
		}
	}
}

// trackedKeys are the settings /settings displays with provenance.
var trackedKeys = []string{
	"gcp.project", "gcp.location",
	"model.name", "model.context_window", "model.safety", "model.summary",
	"model.thinking", "gcp.bucket",
	"sandbox.enabled",
	"agent.max_turns", "agent.shell_timeout_sec", "agent.auto_approve",
	"agent.auto_compact", "agent.compact_at_pct",
	"mcp.enabled", "mcp.call_timeout_sec",
	"tui.theme", "tui.language", "tui.show_thoughts",
	"telemetry.enabled", "telemetry.backend", "telemetry.endpoint", "telemetry.insecure",
	"telemetry.headers_file",
}

func (c *Config) validate() error {
	for i, h := range c.Hooks.PreToolUse {
		if strings.TrimSpace(h.Matcher) == "" {
			return fmt.Errorf("hooks.pre_tool_use[%d]: matcher is required (a tool name, \"a|b\", or \"*\")", i)
		}
		if strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("hooks.pre_tool_use[%d]: command is required", i)
		}
		if h.TimeoutSec < 0 {
			return fmt.Errorf("hooks.pre_tool_use[%d]: timeout_sec must be >= 0", i)
		}
	}
	for i, h := range c.Hooks.SessionStart {
		if strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("hooks.session_start[%d]: command is required", i)
		}
		if h.TimeoutSec < 0 {
			return fmt.Errorf("hooks.session_start[%d]: timeout_sec must be >= 0", i)
		}
	}
	for i, h := range c.Hooks.SessionEnd {
		if strings.TrimSpace(h.Matcher) != "" {
			return fmt.Errorf("hooks.session_end[%d]: takes no matcher — every session end runs it", i)
		}
		if strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("hooks.session_end[%d]: command is required", i)
		}
		if h.TimeoutSec < 0 {
			return fmt.Errorf("hooks.session_end[%d]: timeout_sec must be >= 0", i)
		}
	}
	for i, h := range c.Hooks.UserPromptSubmit {
		if strings.TrimSpace(h.Matcher) != "" {
			return fmt.Errorf("hooks.user_prompt_submit[%d]: takes no matcher — every prompt runs it", i)
		}
		if strings.TrimSpace(h.Command) == "" {
			return fmt.Errorf("hooks.user_prompt_submit[%d]: command is required", i)
		}
		if h.TimeoutSec < 0 {
			return fmt.Errorf("hooks.user_prompt_submit[%d]: timeout_sec must be >= 0", i)
		}
	}
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
	if !ValidThinking(c.Model.Thinking) {
		return fmt.Errorf("[model].thinking must be minimal, low, medium, or high (got %q; empty means the model default)", c.Model.Thinking)
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
	switch c.TUI.Language {
	case "auto", "ja", "en":
	default:
		return fmt.Errorf("[tui].language must be auto, ja, or en (got %q)", c.TUI.Language)
	}
	// An explicit `location = ""` in the file overwrites the default
	// and used to surface as a backend-shaped error far from its cause
	// (review round 2) — the exact failure the strict decode exists to
	// prevent.
	if c.GCP.Location == "" {
		return fmt.Errorf("[gcp].location must not be empty — \"global\" is the default; delete the key to use it")
	}
	if c.Telemetry.Enabled {
		switch c.Telemetry.Backend {
		case "", "gcp":
			// Cloud Logging rides [gcp].project, already required.
			if c.Telemetry.HeadersFile != "" {
				return fmt.Errorf("[telemetry].headers_file applies to the otlp backends; the gcp backend authenticates via ADC")
			}
		case "otlp-grpc", "otlp-http":
			if c.Telemetry.Endpoint == "" {
				return fmt.Errorf("[telemetry].endpoint is required for backend %q", c.Telemetry.Backend)
			}
		default:
			return fmt.Errorf("[telemetry].backend must be gcp, otlp-grpc, or otlp-http (got %q)", c.Telemetry.Backend)
		}
	}
	if c.Model.ContextWindow < 0 {
		return fmt.Errorf("[model].context_window must not be negative")
	}
	return nil
}

// ProjectFileCap bounds a project-supplied config file (.gem-agent.toml,
// .mcp.json): both are read before the operator has said whether to
// trust the directory, so their size is not the directory's to choose.
const ProjectFileCap = 1 << 20

// readCapped reads path up to cap bytes and refuses a longer file.
func readCapped(path string, cap int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, cap+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > cap {
		return nil, fmt.Errorf("larger than %d bytes", cap)
	}
	return data, nil
}

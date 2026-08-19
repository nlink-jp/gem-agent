package cmd

import (
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"
)

// settingsStore builds the panel's rows and applies its edits (ADR-0009).
// It owns the merge of the two policy sources so the panel can stay a
// renderer: the UI shows what was actually stored, never what a keypress
// asked for.
type settingsStore struct {
	cfg        *config.Config
	projectCfg *config.ProjectConfig
	policyFile *config.PolicyFile
	policyPath string
	projectDir string
	registry   *tools.Registry
	ag         *agent.Agent
	// current is the resolved policy the agent is using.
	current policy.Policy
}

// policyValues are the choices a policy row cycles through. "default"
// means no entry at all, which is how the panel expresses "stop having
// an opinion about this tool".
var policyValues = []string{"default", "always", "never"}

// Rebuild resolves the policy from both files and hands it to the agent,
// then returns the panel content. Called after every edit, so the panel
// and the running agent can never disagree.
func (s *settingsStore) Rebuild() (tui.SettingsData, error) {
	merged := map[string]string{}
	for k, v := range s.cfg.Approval.Tools {
		merged[k] = v
	}
	// The machine-owned file wins: a change made in the UI must not
	// silently do nothing because config.toml mentions the same tool.
	for k, v := range s.policyFile.ForProject(s.projectDir) {
		merged[k] = v
	}
	p, _, err := policy.Build(merged, s.projectCfg.Approval.Tools, s.cfg.TrustsProject(s.projectDir))
	if err != nil {
		return tui.SettingsData{}, err
	}
	s.current = p
	s.ag.SetPolicy(p)
	return s.data(), nil
}

// data renders the current state as panel rows.
func (s *settingsStore) data() tui.SettingsData {
	d := tui.SettingsData{ProjectDir: abbreviateHome(s.projectDir)}
	ro := func(section, label, value, key, detail string) {
		d.Rows = append(d.Rows, tui.SettingRow{
			Section: section, Label: label, Value: value,
			Source: s.cfg.Source(key), Detail: detail,
		})
	}

	const needsRestart = "changing this needs a new backend client — edit the config file and restart"
	ro("backend", "gcp.project", s.cfg.GCP.Project, "gcp.project", needsRestart)
	ro("backend", "gcp.location", s.cfg.GCP.Location, "gcp.location", needsRestart)
	ro("backend", "model.name", s.cfg.Model.Name, "model.name", needsRestart)
	ro("backend", "model.safety", s.cfg.Model.Safety, "model.safety", needsRestart)
	ro("backend", "model.summary", summaryLabel(s.cfg.Model.Summary), "model.summary", needsRestart)
	ro("backend", "model.context_window", contextWindowLabel(s.cfg.Model.ContextWindow),
		"model.context_window", "auto-detected when unset")
	ro("safety", "sandbox.enabled", strconv.FormatBool(s.cfg.Sandbox.Enabled), "sandbox.enabled",
		"the sandbox is not a menu item — restart with --no-sandbox if you must")
	ro("limits", "agent.max_turns", strconv.Itoa(s.cfg.Agent.MaxTurns), "agent.max_turns", "")
	ro("limits", "agent.shell_timeout_sec", strconv.Itoa(s.cfg.Agent.ShellTimeoutSec), "agent.shell_timeout_sec", "")
	ro("limits", "mcp.call_timeout_sec", strconv.Itoa(s.cfg.MCP.CallTimeoutSec), "mcp.call_timeout_sec", "")

	// Editable: what can take effect without rebuilding anything.
	d.Rows = append(d.Rows,
		tui.SettingRow{Section: "session", Label: "agent.auto_approve",
			Value: strconv.FormatBool(s.ag.AutoApprove()), Source: s.cfg.Source("agent.auto_approve"),
			Values: []string{"false", "true"}},
		tui.SettingRow{Section: "session", Label: "agent.auto_compact",
			Value: strconv.FormatBool(s.ag.AutoCompact()), Source: s.cfg.Source("agent.auto_compact"),
			Values: []string{"false", "true"}},
		tui.SettingRow{Section: "session", Label: "tui.theme",
			Value: s.cfg.TUI.Theme, Source: s.cfg.Source("tui.theme"),
			Values: []string{"auto", "dark", "light", "plain"}},
	)

	for _, t := range s.registry.List() {
		d.Rows = append(d.Rows, tui.SettingRow{
			Section: "approval policy", Label: t.Name, Tool: t.Name,
			Value: s.current.For(t.Name).String(), Source: s.policySource(t.Name),
			Values: policyValues,
		})
	}
	return d
}

// policySource says which file decided a tool's policy — the point of
// the panel is that a shadowed entry is visible rather than mysterious.
func (s *settingsStore) policySource(tool string) string {
	if _, ok := s.projectCfg.Approval.Tools[tool]; ok {
		// An entry that was dropped for being an untrusted loosening
		// decided nothing, and crediting it would say the opposite of
		// what happened (ADR-0008 §4).
		if s.current.For(tool) == policy.Default {
			return config.ProjectFileName + " (ignored: untrusted)"
		}
		return config.ProjectFileName
	}
	if _, ok := s.policyFile.ForProject(s.projectDir)[tool]; ok {
		return config.PolicyFileName
	}
	if _, ok := s.cfg.Approval.Tools[tool]; ok {
		return config.FromFile
	}
	// Not an exact entry: a wildcard, or nothing at all.
	if s.current.For(tool) != policy.Default {
		return "pattern"
	}
	return config.FromDefault
}

// Apply stores one edit and returns the refreshed panel plus a line for
// scrollback.
func (s *settingsStore) Apply(ch tui.SettingChange) (tui.SettingsData, string) {
	if ch.Tool != "" {
		return s.applyPolicy(ch)
	}
	switch ch.Label {
	case "agent.auto_approve":
		s.ag.SetAutoApprove(ch.Value == "true")
		return s.data(), "auto-approve: " + ch.Value + " (this session)"
	case "agent.auto_compact":
		s.ag.SetAutoCompact(ch.Value == "true")
		return s.data(), "auto-compact: " + ch.Value + " (this session)"
	case "tui.theme":
		s.cfg.TUI.Theme = ch.Value
		return s.data(), "theme: " + ch.Value + " (this session; set [tui].theme to keep it)"
	}
	return s.data(), ""
}

func (s *settingsStore) applyPolicy(ch tui.SettingChange) (tui.SettingsData, string) {
	value := ch.Value
	if value == "default" {
		value = "" // remove the entry rather than record a third state
	}
	scopeDir := ""
	scopeLabel := "everywhere"
	if ch.Scope == tui.ScopeProject {
		scopeDir = s.projectDir
		scopeLabel = "in " + abbreviateHome(s.projectDir)
	}
	s.policyFile.Set(scopeDir, ch.Tool, value)
	if err := s.policyFile.Save(s.policyPath); err != nil {
		return s.data(), "could not save the policy: " + err.Error()
	}
	data, err := s.Rebuild()
	if err != nil {
		return s.data(), "policy saved but not applied: " + err.Error()
	}
	if value == "" {
		return data, fmt.Sprintf("%s: back to the default %s (saved)", ch.Tool, scopeLabel)
	}
	return data, fmt.Sprintf("%s: %s %s (saved to %s)", ch.Tool, value, scopeLabel, config.PolicyFileName)
}

// writeSettingsTable renders the panel content as plain text, for the
// non-TTY REPL and pipes. Same rows, no editor.
func writeSettingsTable(out io.Writer, d tui.SettingsData) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	section := ""
	for _, row := range d.Rows {
		if row.Section != section {
			section = row.Section
			fmt.Fprintf(tw, "\n[%s]\n", section)
		}
		editable := ""
		if len(row.Values) > 0 {
			editable = "\t(editable in the TUI)"
		}
		fmt.Fprintf(tw, "  %s\t%s\t(%s)%s\n", row.Label, row.Value, row.Source, editable)
	}
	tw.Flush()
	fmt.Fprintln(out, "\nrun gem-agent in a terminal for the interactive panel")
}

// summaryLabel renders "" as what it means.
func summaryLabel(s string) string {
	if s == "" {
		return "(main model)"
	}
	return s
}

// contextWindowLabel renders 0 as what it means.
func contextWindowLabel(n int) string {
	if n == 0 {
		return "(auto-detect)"
	}
	return strconv.Itoa(n)
}

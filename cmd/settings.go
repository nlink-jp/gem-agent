package cmd

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/banner"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
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
	// filter and inv are the MCP half of the panel (ADR-0077 §3): what
	// is excluded, and what there is to exclude. Both are replaced by
	// reloadMCP, which re-derives the filter from the files and
	// reconnects that one server — the panel never edits the live tool
	// set itself.
	filter    mcpfilter.Filter
	inv       mcpInventory
	reloadMCP func(server string) (mcpfilter.Filter, mcpInventory, string)
	// sessionEdits marks keys the panel changed this session: their
	// provenance is "session", not whatever startup layer set the
	// value the panel just replaced — the display claimed config.toml
	// authored values it never held (review round 2).
	sessionEdits map[string]bool
}

// settingSource is Config.Source with the session-edit override.
func (s *settingsStore) settingSource(key string) string {
	if s.sessionEdits[key] {
		return "session"
	}
	return s.cfg.Source(key)
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
	// Learned command rules are parsed but not applied (ADR-0049 §3).
	p, _, err := policy.Build(merged, s.projectCfg.Approval.Tools,
		nil, s.cfg.TrustsProject(s.projectDir))
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
	ro("backend", "gcp.bucket", bucketLabel(s.cfg.GCP.Bucket), "gcp.bucket", needsRestart)
	ro("backend", "model.name", s.cfg.Model.Name, "model.name", needsRestart)
	ro("backend", "model.safety", s.cfg.Model.Safety, "model.safety", needsRestart)
	ro("backend", "model.summary", summaryLabel(s.cfg.Model.Summary), "model.summary", needsRestart)
	ro("backend", "model.thinking", thinkingLabel(s.cfg.Model.Thinking), "model.thinking", needsRestart)
	ro("backend", "model.context_window", contextWindowLabel(s.cfg.Model.ContextWindow),
		"model.context_window", "auto-detected when unset")
	// The measured state, not the configured one: --no-sandbox is never
	// folded back into cfg, and a failed write-lane probe leaves the
	// setting true while the runtime is unconfined. Two documents send
	// the operator here to check the sandbox, so the row has to answer
	// the question they were sent with (pre-release review).
	// Source "measured", not the config file: the value is what the
	// startup probes established, and crediting config.toml would name a
	// file that does not decide it — `--no-sandbox` is never folded back
	// into cfg, and a failed probe leaves the setting true.
	d.Rows = append(d.Rows, tui.SettingRow{
		Section: "safety", Label: "sandbox",
		Value:  banner.State(s.registry.Confined(), s.registry.ReadLane(), s.cfg.Sandbox.ReadLanePrompts),
		Source: "measured",
		Detail: "established by probes at startup — restart with or without --no-sandbox to change it",
	})
	ro("limits", "agent.max_turns", strconv.Itoa(s.cfg.Agent.MaxTurns), "agent.max_turns", "")
	ro("limits", "agent.shell_timeout_sec", strconv.Itoa(s.cfg.Agent.ShellTimeoutSec), "agent.shell_timeout_sec", "")
	ro("limits", "agent.compact_at_pct", strconv.Itoa(s.cfg.Agent.CompactAtPct)+"%", "agent.compact_at_pct", "")
	ro("limits", "mcp.call_timeout_sec", strconv.Itoa(s.cfg.MCP.CallTimeoutSec), "mcp.call_timeout_sec", "")
	// mcp.enabled was tracked but never shown: an operator whose file
	// disabled MCP was sent to debug mcp.json by /mcp's "no servers"
	// message instead of seeing the real cause here (review round 2).
	ro("limits", "mcp.enabled", strconv.FormatBool(s.cfg.MCP.Enabled), "mcp.enabled",
		"false disables ALL MCP servers, global and project")
	ro("telemetry", "telemetry.enabled", strconv.FormatBool(s.cfg.Telemetry.Enabled), "telemetry.enabled",
		"audit events to Cloud Logging or an OTLP collector; applies at next start")
	if s.cfg.Telemetry.Enabled {
		ro("telemetry", "telemetry.backend", s.cfg.Telemetry.Backend, "telemetry.backend", "")
		if s.cfg.Telemetry.Backend != "gcp" && s.cfg.Telemetry.Backend != "" {
			ro("telemetry", "telemetry.endpoint", s.cfg.Telemetry.Endpoint, "telemetry.endpoint", "")
			ro("telemetry", "telemetry.insecure", strconv.FormatBool(s.cfg.Telemetry.Insecure), "telemetry.insecure", "")
			if s.cfg.Telemetry.HeadersFile != "" {
				ro("telemetry", "telemetry.headers_file", s.cfg.Telemetry.HeadersFile, "telemetry.headers_file", "")
			}
		}
	}
	// Read-only by design (ADR-0029 §1): the chrome is built with the
	// resolved language at startup; a live switch would bisect the
	// scrollback into two languages. "auto" shows what it resolved TO
	// — the one row whose purpose is display gave the least display
	// (review round 2).
	langValue := s.cfg.TUI.Language
	if langValue == "auto" {
		langValue = fmt.Sprintf("auto (→ %s)", uitext.Resolve(langValue, os.Getenv))
	}
	ro("session", "tui.language", langValue, "tui.language",
		"UI language: auto (from LC_ALL/LC_MESSAGES/LANG), ja, or en — applies at next start")

	// Editable: what can take effect without rebuilding anything.
	d.Rows = append(d.Rows,
		tui.SettingRow{Section: "session", Label: "agent.auto_approve",
			Value: strconv.FormatBool(s.ag.AutoApprove()), Source: s.settingSource("agent.auto_approve"),
			Values: []string{"false", "true"}},
		tui.SettingRow{Section: "session", Label: "agent.auto_compact",
			Value: strconv.FormatBool(s.ag.AutoCompact()), Source: s.settingSource("agent.auto_compact"),
			Values: []string{"false", "true"}},
	)
	// Read-only by design (review round 2): the row was editable but
	// applied NOTHING — styles and the glamour renderer are built once
	// at startup, and "auto" cannot re-detect mid-session (the OSC
	// query would leak into the input box). An honest restart note
	// beats a menu that lies, per this panel's own design rule.
	ro("session", "tui.theme", s.cfg.TUI.Theme, "tui.theme", "applies at next start")
	ro("session", "tui.show_thoughts", strconv.FormatBool(s.cfg.TUI.ShowThoughts), "tui.show_thoughts",
		"live thought summaries in the TUI; applies at next start")

	s.mcpRows(&d)
	s.approvalRows(&d)
	return d
}

// declaredValue renders an exclusion state the way the operator reads
// it: a row is on when the session has it. Lower case, unlike the
// banner's ON/OFF — these are row values the panel cycles, not status.
func declaredValue(present bool) string {
	if present {
		return "on"
	}
	return "off"
}

var onOffValues = []string{"on", "off"}

// mcpRows draws the two levels of ADR-0077 §3: every configured server —
// present whether or not it is running, because the row comes from
// .mcp.json and not from the server — and, for the ones that listed,
// their functions.
func (s *settingsStore) mcpRows(d *tui.SettingsData) {
	for _, server := range s.inv.Servers {
		serverOff := s.filter.Server(server)
		detail := ""
		if serverOff {
			detail = "not started — turn it on to start it"
		}
		d.Rows = append(d.Rows, tui.SettingRow{
			Section: "mcp tools", Label: server, Value: declaredValue(!serverOff),
			Values: onOffValues, Exclude: server, Group: "mcp:" + server,
			Collapsible: true, Source: s.excludeSource(server, server), Detail: detail,
		})
		for _, fn := range s.inv.Offered[server] {
			entry := server + mcpfilter.Separator + fn
			d.Rows = append(d.Rows, tui.SettingRow{
				Section: "mcp tools", Label: fn, Value: declaredValue(!s.filter.Func(server, fn)),
				Values: onOffValues, Exclude: entry, Group: "mcp:" + server, Child: true,
				Source: s.excludeSource(server, entry),
			})
		}
	}
}

// excludeSource says which file decided this row. Per server the nearest
// scope decides whole, so policy.toml having spoken about a server is
// the answer for every row under it — including the ones it left on,
// which is the shadowing this panel exists to make visible.
func (s *settingsStore) excludeSource(server, entry string) string {
	for _, e := range s.projectCfg.MCP.Exclude {
		if e == entry {
			return config.ProjectFileName
		}
	}
	for _, e := range s.policyFile.MCP.Exclude {
		if es, _ := mcpfilter.Split(e); es == server {
			return config.PolicyFileName
		}
	}
	// An opinion with no entries is still this file's opinion, and it is
	// the case `decided` was added for: without this the row for a
	// server the panel turned back on credited the very file that says
	// it is off, and an operator following the column would edit
	// config.toml and see nothing happen (pre-release re-review).
	for _, d := range s.policyFile.MCP.Decided {
		if d == server {
			return config.PolicyFileName
		}
	}
	for _, e := range s.cfg.MCP.Exclude {
		if e == entry {
			return config.FromFile
		}
	}
	return config.FromDefault
}

// approvalRows groups the approval policy by server, for the reason the
// tool rows are grouped: flat, this section is hundreds of lines on a
// machine with a full server list (ADR-0009 decision 1, amended by
// ADR-0077). Built-ins stay ungrouped — there are a dozen of them and
// they have no server to sit under.
func (s *settingsStore) approvalRows(d *tui.SettingsData) {
	row := func(name string, child bool, group string) tui.SettingRow {
		return tui.SettingRow{
			Section: "approval policy", Label: name, Tool: name,
			Value: s.current.For(name).String(), Source: s.policySource(name),
			Values: policyValues, Child: child, Group: group,
		}
	}
	prefixes := make([][2]string, 0, len(s.inv.Servers))
	for _, server := range s.inv.Servers {
		prefixes = append(prefixes, [2]string{server, "mcp__" + sanitizeToolName(server) + "__"})
	}
	byServer := map[string][]string{}
	var ungrouped []string
	for _, t := range s.registry.List() {
		matched := ""
		for _, p := range prefixes {
			if strings.HasPrefix(t.Name, p[1]) {
				matched = p[0]
				break
			}
		}
		if matched == "" {
			ungrouped = append(ungrouped, t.Name)
			continue
		}
		byServer[matched] = append(byServer[matched], t.Name)
	}
	for _, name := range ungrouped {
		d.Rows = append(d.Rows, row(name, false, ""))
	}
	for _, server := range s.inv.Servers {
		names := byServer[server]
		if len(names) == 0 {
			continue
		}
		group := "approval:" + server
		d.Rows = append(d.Rows, tui.SettingRow{
			Section: "approval policy", Label: server,
			Value: fmt.Sprintf("%d tools", len(names)), Group: group, Collapsible: true,
			Source: config.FromDefault,
			Detail: "a group heading — open it to set a policy per tool",
		})
		for _, name := range names {
			d.Rows = append(d.Rows, row(name, true, group))
		}
	}
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
	if ch.Exclude != "" {
		return s.applyExclude(ch)
	}
	if ch.Tool != "" {
		return s.applyPolicy(ch)
	}
	switch ch.Label {
	case "agent.auto_approve":
		s.ag.SetAutoApprove(ch.Value == "true")
		s.markSessionEdit("agent.auto_approve")
		return s.data(), "auto-approve: " + ch.Value + " (this session)"
	case "agent.auto_compact":
		s.ag.SetAutoCompact(ch.Value == "true")
		s.markSessionEdit("agent.auto_compact")
		return s.data(), "auto-compact: " + ch.Value + " (this session)"
	}
	return s.data(), ""
}

func (s *settingsStore) markSessionEdit(key string) {
	if s.sessionEdits == nil {
		s.sessionEdits = map[string]bool{}
	}
	s.sessionEdits[key] = true
}

// applyExclude writes one server's whole exclusion state and reconnects.
//
// The whole state, not a delta: per server the nearest scope decides
// (ADR-0077 §2), so the first thing the panel writes about a server
// shadows config.toml's word about it entirely. Carrying the effective
// set across is what keeps an operator who toggled one function from
// silently losing the three their own file excluded.
func (s *settingsStore) applyExclude(ch tui.SettingChange) (tui.SettingsData, string) {
	// Refuse to write what the loader would refuse to read: a name
	// carrying "*" or a second "/" would be saved into the machine-owned
	// file and then fail every later start, with hand-editing the only
	// way out (pre-release review).
	if _, err := mcpfilter.Parse(ch.Exclude, mcpfilter.FromPolicy); err != nil {
		return s.data(), "cannot exclude this name: " + err.Error()
	}
	server, fn := mcpfilter.Split(ch.Exclude)
	want := ch.Value == "off" // "off" means excluded
	// config.toml alone: what this server's state falls back to when the
	// panel withdraws, and the function exclusions the operator wrote by
	// hand — which turning the server off and on again used to discard
	// for good (pre-release re-review).
	configOnly, err := mcpfilter.Build(s.cfg.MCP.Exclude, mcpfilter.PolicyScope{}, nil)
	if err != nil {
		return s.data(), "cannot read the current exclusions: " + err.Error()
	}
	withdraw := false
	fresh, err := config.MutatePolicyFile(s.policyPath, func(pf *config.PolicyFile) {
		// Derived inside the lock, from the file this write is based on:
		// a set computed from the startup snapshot would erase whatever
		// another instance committed meanwhile, which is the failure the
		// lock exists for (pre-release re-review).
		globals, berr := mcpfilter.Build(s.cfg.MCP.Exclude, policyScope(pf), nil)
		if berr != nil {
			return
		}
		var entries []string
		switch {
		case fn != "":
			// Not the composed filter: it carries the project file's
			// additions, and writing those here would promote a
			// project-scoped exclusion into every project.
			entries = withEntry(globals.For(server), ch.Exclude, want)
		case want:
			entries = []string{server}
		default:
			// Turning a server on lifts the whole-server exclusion and
			// nothing else: the functions a lower scope excluded stay
			// excluded. If config.toml has no opinion about this server
			// at all, the panel withdraws instead of recording one —
			// otherwise it would shadow a config.toml written tomorrow.
			entries = configOnly.FunctionEntries(server)
			if !configOnly.Knows(server) {
				withdraw = true
			}
		}
		if withdraw {
			pf.ClearMCPServer(server)
			return
		}
		pf.SetMCPExclusions(server, entries)
	})
	if err != nil {
		return s.data(), "could not save the exclusion: " + err.Error()
	}
	*s.policyFile = *fresh
	if s.reloadMCP == nil {
		return s.data(), ch.Exclude + ": saved, but this session cannot reconnect MCP"
	}
	filter, inv, note := s.reloadMCP(server)
	s.filter, s.inv = filter, inv
	data, err := s.Rebuild()
	if err != nil {
		return s.data(), "saved but not applied: " + err.Error()
	}
	line := fmt.Sprintf("%s: %s (saved to %s)", ch.Exclude, ch.Value, config.PolicyFileName)
	if withdraw {
		line = fmt.Sprintf("%s: %s (%s no longer has an opinion about %s)",
			ch.Exclude, ch.Value, config.PolicyFileName, server)
	}
	// What the reconnect said — a server that would not start, a stale
	// entry — belongs on the same line as the edit that caused it.
	// Discarding it left the row reading "on" with no children and no
	// reason anywhere (pre-release re-review).
	if note != "" {
		line += " — " + strings.TrimSpace(note)
	}
	// Also at the server level: Func(server, "") answers for the whole
	// server, and the fn != "" guard meant a server row snapped back to
	// off with nothing but the Source column to explain it (pre-release
	// review).
	if ch.Value == "on" && s.filter.Func(server, fn) {
		line += " — still excluded by " + config.ProjectFileName
	}
	return data, line
}

// withEntry adds or removes one entry, keeping the rest.
func withEntry(entries []string, entry string, want bool) []string {
	out := make([]string, 0, len(entries)+1)
	found := false
	for _, e := range entries {
		if e == entry {
			found = true
			if !want {
				continue
			}
		}
		out = append(out, e)
	}
	if want && !found {
		out = append(out, entry)
	}
	return out
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
	// Flocked read-modify-write: rewriting this process's startup
	// snapshot whole clobbered any decision a concurrent instance had
	// persisted meanwhile (review round 2).
	fresh, err := config.MutatePolicyFile(s.policyPath, func(pf *config.PolicyFile) {
		pf.Set(scopeDir, ch.Tool, value)
	})
	if err != nil {
		return s.data(), "could not save the policy: " + err.Error()
	}
	// Through the pointer: runREPL holds the same PolicyFile for the
	// pin checks, and a rebound pointer would leave it stale
	// (verification D).
	*s.policyFile = *fresh
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
	// The project, first. ADR-0078 §5 dropped `project:` from the banner
	// on the grounds that /settings shows it in both modes — and this
	// renderer, the footer-less one, never printed it. The path is
	// symlink-resolved, so "the operator is standing in it" is not an
	// answer either: launching in /tmp/x confines the file tools to
	// /private/tmp/x (pre-release review).
	if d.ProjectDir != "" {
		fmt.Fprintf(out, "project: %s\n", d.ProjectDir)
	}
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
		indent := "  "
		if row.Child {
			indent = "      "
		}
		fmt.Fprintf(tw, "%s%s\t%s\t(%s)%s\n", indent, row.Label, row.Value, row.Source, editable)
		// The remedy, in the mode that has no way to press a key on the
		// row: the measured sandbox value arrived here with nothing
		// saying what to do about it (pre-release review).
		if row.Detail != "" {
			fmt.Fprintf(tw, "%s  \t%s\t\n", indent, row.Detail)
		}
	}
	_ = tw.Flush()
	fmt.Fprintln(out, "\nrun gem-agent in a terminal for the interactive panel")
}

// summaryLabel renders "" as what it means.
func summaryLabel(s string) string {
	if s == "" {
		return "(main model)"
	}
	return s
}

func thinkingLabel(s string) string {
	if s == "" {
		return "(model default)"
	}
	return s
}

func bucketLabel(s string) string {
	if s == "" {
		return "(unset — media attaches inline up to the cap)"
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

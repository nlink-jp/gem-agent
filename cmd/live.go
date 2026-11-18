package cmd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/hooks"
	"github.com/nlink-jp/gem-agent/internal/mcp"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/uitext"
	"github.com/nlink-jp/gem-agent/internal/workdir"
)

// The state holders (ADR-0124 §1, §2.3): what /clear or a reload
// replaces lives in a holder, shared by pointer, so whatever was built
// before the change and must follow it reads through the holder — as it
// read through a closure over runREPL's locals before.

// transcript is the session's log: the state directory it lives under,
// the logger in use and its identity. /clear replaces the logger.
type transcript struct {
	sessionDir    string
	sessionDirErr error
	// sessionLog is the logger as the agent sees it: nil, not a nil
	// *session.Logger, when there is none.
	sessionLog  agent.SessionLog
	curLog      *session.Logger
	sessionPath string
	sessionID   string
}

// workArea is the session work directory (ADR-0058); "" when there is
// none. /clear rotates it.
type workArea struct {
	workDir string
}

// grantHolder is what the project's trust lets this session load
// (ADR-0074); every pin check rewrites its excluded set.
type grantHolder struct {
	grant projectGrant
}

// persistentState is the persistent-file snapshot (ADR-0074 §3/§4) and
// where it is kept between sessions.
type persistentState struct {
	persistentSnap    map[string]string
	persistentPath    string
	persistentPathErr error
}

// projectContent is what the session read from the project: the
// instruction files, the skills and the memory section of the prompt.
type projectContent struct {
	projectContext string
	contextLabels  []string
	contextNotes   []string
	skillsList     []skills.Skill
	memorySection  string
}

// mcpSet is the connected MCP servers and the filter they were connected
// under.
type mcpSet struct {
	mcpClients []*mcp.Client
	mcpSummary []string
	mcpInv     mcpInventory
	mcpFilter  mcpfilter.Filter
}

// liveSession owns the holders after startup and the parts its methods
// call into. Its methods are the session's post-startup mutators; they run
// between turns only — a slash command cannot run while a turn is in
// flight — so one writer at a time is the rule, and nothing locks.
type liveSession struct {
	*transcript
	*workArea
	*grantHolder
	*persistentState
	*projectContent
	*mcpSet

	ctx            context.Context
	version        string
	cfg            *config.Config
	msgs           *uitext.Messages
	stderr         io.Writer
	projectDir     string
	projectTrusted bool
	policyFile     *config.PolicyFile
	policyPath     string
	projectCfg     *config.ProjectConfig
	registry       *tools.Registry
	shellExec      *liveExec
	sandboxOn      bool
	granter        *dirGranter
	watch          *addedWatch
	adv            *mcpAdvertiser
	backend        modelClient
	sink           *telemetry.Sink
	surf           *surface
	hookRunner     *hooks.Runner
	hookSession    hooks.Session
	dates          sessionDates
	ag             *agent.Agent
	settings       *settingsStore
}

func newLiveSession() *liveSession {
	return &liveSession{
		transcript:      &transcript{},
		workArea:        &workArea{},
		grantHolder:     &grantHolder{},
		persistentState: &persistentState{},
		projectContent:  &projectContent{},
		mcpSet:          &mcpSet{},
	}
}

func (sess *liveSession) reportPersistent(reason string, log func(kind string, data any) error) {
	now, a, c, r, cut := persistentReport(sess.persistentSnap, func() (map[string]string, bool) { return trustpin.Snapshot(sess.projectDir) })
	if cut {
		fmt.Fprintf(sess.stderr, "note: change detection covers the first %d files under the project only\n", trustpin.WalkEntries)
	}
	if len(a)+len(c)+len(r) > 0 || cut {
		if len(a)+len(c)+len(r) > 0 {
			fmt.Fprintf(sess.stderr, sess.msgs.PersistentSessionFmt+"\n", describeSnapshotDiff(a, c, r))
		}
		if log != nil {
			_ = log("persistent_changes", map[string]any{"reason": reason, "added": a, "changed": c, "removed": r, "cut": cut})
		}
	}
	sess.persistentSnap = now
	if sess.persistentPathErr == nil {
		_ = savePersistentSnapshot(sess.persistentPath, now)
	}
}

func (sess *liveSession) composeSystem() string {
	return buildSystemPrompt(sess.projectDir, sess.workDir, sess.projectContext, sess.dates, sess.registry.AddedDirs()...) + skills.PromptSection(sess.skillsList) + sess.memorySection + mcpOnRequestSection(sess.adv.PromptLines())
}

// syncAdvertised follows an inventory change (a reconnect, from
// /mcp reload or the settings panel): the advertiser re-resolves
// its sets, the declarations are rebuilt, and the system prompt's
// server list is recomposed (ADR-0083 §5, §8). ag is set below and
// every caller runs after that.
func (sess *liveSession) syncAdvertised() {
	sess.adv.setInventory(sess.mcpInv, registeredIn(sess.registry))
	sess.ag.RefreshTools()
	sess.ag.SetSystem(sess.composeSystem())
}

// --- in-session integration reload (ADR-0039) ---
// Both closures reuse the startup code paths and the startup trust
// verdict — a reload can never widen what the trust gate allowed.
// They run only between turns (slash commands cannot be queued), so
// reassigning the captured variables is single-writer safe.
// reconnectMCP is /mcp reload; /clear calls it after its own pin
// check, so recheck is false there and the note is not printed twice.
func (sess *liveSession) reconnectMCP(recheck bool) string {
	if !sess.cfg.MCP.Enabled {
		return sess.msgs.MCPDisabled
	}
	sess.registry.RemoveByPrefix("mcp__")
	for _, c := range sess.mcpClients {
		c.Close()
	}
	// Warnings go into the command output: under the TUI a stderr
	// write would corrupt the display.
	var warn bytes.Buffer
	// A changed .mcp.json is re-checked against its pin first
	// (ADR-0074); mid-session there is nobody at a prompt, so a
	// change is left out and named.
	if recheck {
		var pinNotes []string
		sess.grant.excluded, pinNotes = checkPins(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, false, nil, &warn, sess.msgs)
		for _, n := range pinNotes {
			fmt.Fprintf(&warn, "%s\n", n)
		}
	}
	sess.mcpClients, sess.mcpSummary, sess.mcpInv = connectMCPServers(sess.ctx, sess.cfg, sess.projectDir, sess.version, sess.registry, &warn, sess.grant, sess.mcpFilter)
	// Loads survive a reconnect by name; the librarian's flags do
	// not (ADR-0083 §8). The server list in the system prompt
	// follows the inventory, as the skills section follows a
	// skills reload.
	sess.syncAdvertised()
	mcpTools := 0
	for _, t := range sess.registry.List() {
		if strings.HasPrefix(t.Name, "mcp__") {
			mcpTools++
		}
	}
	// The panel reads this snapshot; only a panel-driven reload used
	// to refresh it, so a server added to .mcp.json and picked up by
	// `/mcp reload` had no row and could not be excluded until some
	// other toggle healed it (pre-release review).
	sess.settings.inv = sess.mcpInv
	sess.sink.Reload("mcp", len(sess.mcpClients), mcpTools)
	if sess.sessionLog != nil {
		_ = sess.sessionLog.Log("mcp_reload", map[string]any{
			"servers": len(sess.mcpClients), "tools": mcpTools})
	}
	var b strings.Builder
	fmt.Fprintf(&b, sess.msgs.MCPReloadedFmt, len(sess.mcpClients), mcpTools)
	for _, s := range sess.mcpSummary {
		b.WriteString("  " + s + "\n")
	}
	if warn.Len() > 0 {
		b.WriteString(warn.String())
	}
	return b.String()
}

// loadMCP is /mcp load <server>: the operator advertises a server
// by hand, lifting the librarian's flags on it (ADR-0083 §6).
// Between turns, so the declarations are rebuilt here.
func (sess *liveSession) loadMCP(server string) string {
	if !sess.cfg.MCP.OnRequest() {
		return sess.msgs.MCPAllAdvertised
	}
	if err := sess.adv.LoadServerNow(server); err != nil {
		return "error: " + err.Error() + "\n"
	}
	sess.ag.RefreshTools()
	return fmt.Sprintf(sess.msgs.MCPLoadedFmt, server)
}

// The panel writes an exclusion for one server and then asks for
// this: the filter is re-derived from the files it just changed, and
// that one server is reconnected under it (ADR-0039 + ADR-0077 §3).
// Rebuilt rather than patched so the panel and the runtime read the
// same three files in the same order the next start will. One
// server, not the set: the edit names one, and reconnecting all of
// them killed and respawned every process on an arrow key, inside
// the TUI's event loop, so the keys typed meanwhile queued behind it
// (post-release field report).
func (sess *liveSession) reconnectServer(server string) (mcpfilter.Filter, mcpInventory, string) {
	f, err := mcpfilter.Build(sess.cfg.MCP.Exclude, policyScope(sess.policyFile), sess.projectCfg.MCP.Exclude)
	if err != nil {
		// Saved but unusable: keep the running set and say so
		// rather than dropping every exclusion on the floor.
		return sess.mcpFilter, sess.mcpInv, "not applied: " + err.Error()
	}
	sess.mcpFilter = f
	var running *mcp.Client
	others := make([]*mcp.Client, 0, len(sess.mcpClients))
	for _, c := range sess.mcpClients {
		if c.Name() == server {
			running = c
			continue
		}
		others = append(others, c)
	}
	// Only what went wrong goes back to the panel: an arrow key is
	// not a command that asked for an inventory, and the warnings
	// are still the operator's answer to "why did nothing appear"
	// (pre-release review).
	var warn bytes.Buffer
	timeout := time.Duration(sess.cfg.MCP.CallTimeoutSec) * time.Second
	startupTimeout := time.Duration(sess.cfg.MCP.StartupTimeoutSec) * time.Second
	start := func() mcpServer {
		return mcp.NewStdio(server, sess.mcpInv.configs[server], timeout, startupTimeout, sess.version, sess.registry.WorkDir())
	}
	// The interface hides the type; a nil *mcp.Client inside it
	// would not compare equal to nil, so the running client is
	// passed only when there is one.
	var runningServer mcpServer
	if running != nil {
		runningServer = running
	}
	kept := reconnectMCPServer(sess.ctx, server, runningServer, start, startupTimeout, sess.registry, &warn, sess.mcpFilter, &sess.mcpInv)
	if kept != nil {
		others = append(others, kept.(*mcp.Client))
	}
	sess.mcpClients = others
	sess.mcpSummary = sess.mcpInv.summaryLines()
	// The panel's per-server reconnect changes the inventory as
	// /mcp reload does; the advertiser must follow, or the tools it
	// re-registered are undeclarable for the rest of the session
	// (implementation review).
	sess.syncAdvertised()
	sess.settings.inv = sess.mcpInv
	mcpTools := mcpToolCount(sess.registry)
	sess.sink.Reload("mcp", len(sess.mcpClients), mcpTools)
	if sess.sessionLog != nil {
		_ = sess.sessionLog.Log("mcp_reload", map[string]any{
			"servers": len(sess.mcpClients), "tools": mcpTools, "server": server})
	}
	return sess.mcpFilter, sess.mcpInv, reloadWarnings(warn.String())
}

func (sess *liveSession) reloadSkills() string {
	var pinNotes []string
	sess.grant.excluded, pinNotes = checkPins(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, false, nil, io.Discard, sess.msgs)
	notes := append([]string{}, pinNotes...)
	list, discoverNotes := discoverSkills(sess.projectDir, sess.grant)
	notes = append(notes, discoverNotes...)
	skills.CloseAll(sess.skillsList) // the roots of the list being replaced
	sess.skillsList = list
	// The skill descriptions ride the system prompt; rebuild it so
	// the model sees the new set (the implicit-cache prefix re-warms
	// — the deliberate cost of a reload).
	sess.ag.SetSystem(sess.composeSystem())
	sess.sink.Reload("skills", 0, len(sess.skillsList))
	if sess.sessionLog != nil {
		_ = sess.sessionLog.Log("skills_reload", map[string]any{"count": len(sess.skillsList)})
	}
	var b strings.Builder
	fmt.Fprintf(&b, sess.msgs.SkillsReloadedFmt, len(sess.skillsList))
	for _, n := range notes {
		b.WriteString("  ⚠ " + n + "\n")
	}
	b.WriteString(skillsListing(sess.skillsList))
	return b.String()
}

// resolveWindow settles the model's input token limit and hands it to
// everyone who needs it: the footer displays it, and auto-compaction
// (ADR-0006) measures against it. It runs in the background — a
// metadata lookup must never delay the first prompt — and it runs in
// every mode, one-shot included: a long one-shot tool loop fills the
// window exactly like an interactive one, and with no window known
// compaction silently never fires (measured).
func (sess *liveSession) resolveWindow() {
	tokens, assumed := sess.cfg.Model.ContextWindow, false
	if tokens <= 0 {
		mctx, cancel := context.WithTimeout(sess.ctx, 15*time.Second)
		defer cancel()
		if w, err := sess.backend.ContextWindow(mctx); err == nil && w > 0 {
			tokens = w
		} else {
			// Vertex publisher metadata omits inputTokenLimit
			// (measured 2026-08: Models.Get succeeds with 0). The
			// Gemini 2.5/3 family is uniformly 1M input tokens, so
			// use that as an explicit estimate (~) rather than a
			// permanent unknown; [model].context_window overrides.
			tokens, assumed = 1_048_576, true
		}
	}
	sess.ag.SetContextWindow(tokens)
	sess.surf.contextWindow(tokens, assumed)
}

// --- session-start hooks (ADR-0069): startup / resume, and /clear ---
// The output rides the next turn's user message as a data
// attachment (ADR-0055's lane): never the system prompt, so the
// cached request prefix (ADR-0018) is untouched, and never the
// typed input, which the risk evaluator trusts (ADR-0038/0054).
func (sess *liveSession) sessionStartHooks(source string) {
	if sess.hookRunner == nil || !sess.hookRunner.HasSessionStart() {
		return
	}
	out := sess.hookRunner.SessionStart(sess.ctx, sess.hookSession, source)
	if out == "" {
		return
	}
	sess.ag.AttachData("session_start", agent.HookAttachmentKind, out)
	sess.surf.hookNotice(fmt.Sprintf("session_start hook (%s) attached %d bytes of context as data for the next turn", source, len(out)))
}

// Session-end hooks (ADR-0071 §4) run when the session ends —
// whatever ended it — and when /clear closes the old session.
func (sess *liveSession) sessionEndHooks(reason string) {
	if sess.hookRunner == nil || !sess.hookRunner.HasSessionEnd() {
		return
	}
	sess.hookRunner.SessionEnd(sess.ctx, sess.hookSession, reason)
}

// /clear starts a new session (ADR-0071 §2): the old transcript is
// closed where the conversation ended and stays resumable by its
// id; a new id, transcript and work directory take over, exported
// to children like the first; the hooks see an end and a start —
// the same sequence Claude Code produces. If a new transcript
// cannot be opened the conversation is cleared in place, as before,
// and the operator is told.
func (sess *liveSession) clear() string {
	var notes []string
	defer sess.surf.collectHookNotices(&notes)()
	note := func(format string, a ...any) { notes = append(notes, fmt.Sprintf(format, a...)) }
	// The output is the slash command's: warnings first, then the
	// MCP reconnection report (the same text /mcp reload prints).
	var extra strings.Builder
	render := func() string {
		var b strings.Builder
		for _, n := range notes {
			fmt.Fprintf(&b, "[⚠ %s]\n", n)
		}
		b.WriteString(extra.String())
		return b.String()
	}
	// The new transcript is opened before anything ends, so a
	// failure leaves the session it found: cleared in place, the
	// operator told, no session_end for an id that continues
	// (review round 4). An unresolved state root must not be
	// handed to Open — it resolved the project subdirectory
	// against the working directory, the operator's project.
	if sess.sessionDirErr != nil {
		sess.ag.Reset()
		// The history is gone, so "the conversation above began on"
		// would name nothing: the dates start over (ADR-0097).
		sess.dates = sessionDates{Start: time.Now()}
		sess.ag.SetSystem(sess.composeSystem())
		note("history cleared; a new session could not be started (%v) — the conversation continues in this session", sess.sessionDirErr)
		sess.sessionStartHooks("clear")
		return render()
	}
	newLog, err := openSessionLog(sess.sessionDir, "", sess.projectDir, sess.cfg.Model.Name, sess.cfg.GCP.Location, sess.version)
	if err != nil {
		sess.ag.Reset()
		sess.dates = sessionDates{Start: time.Now()}
		sess.ag.SetSystem(sess.composeSystem())
		note("history cleared; a new session could not be started (%v) — the conversation continues in this session", err)
		sess.sessionStartHooks("clear")
		return render()
	}
	// From here to the runner's rebuild the session's identity, its
	// transcript and its work directory change: a grant — a late one
	// from an abandoned call included — waits, and one from the old
	// session is dropped by the epoch advanced now (ADR-0099 §12;
	// review of step 2, #2).
	sess.granter.beginClear()
	// Hook first, then the audit event — the order ADR-0071 §4a
	// states and external consumers observe; the trail closes under
	// the old id before the sink is re-resourced for the new one.
	sess.sessionEndHooks("clear")
	sess.reportPersistent("clear", recordTo(sess.curLog))
	sess.watch.report("clear", recordTo(sess.curLog))
	sess.sink.SessionEnd()
	sess.ag.Restart(newLog)
	// A new session: a new start day, and no longer a resumed one
	// (ADR-0097); the prompt is rebuilt below with the work directory.
	sess.dates = sessionDates{Start: time.Now()}
	// The new session re-checks the pins (ADR-0074): a file that
	// changed during the old one is left out, and the system prompt
	// is composed from what the grant allows.
	var pinNotes []string
	sess.grant.excluded, pinNotes = checkPins(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, false, nil, io.Discard, sess.msgs)
	notes = append(notes, pinNotes...)
	sess.projectContext, sess.contextLabels, sess.contextNotes = loadInstructions(sess.projectDir, sess.grant)
	_ = sess.contextLabels
	notes = append(notes, sess.contextNotes...)
	// The project skills follow the same grant (review F8): one
	// that changed during the old session is left out with the
	// instruction files, not carried over.
	if list, discoverNotes := discoverSkills(sess.projectDir, sess.grant); true {
		notes = append(notes, discoverNotes...)
		skills.CloseAll(sess.skillsList)
		sess.skillsList = list
	}
	if sess.curLog != nil {
		_ = sess.curLog.Close()
	}
	sess.curLog = newLog
	sess.sessionLog = newLog
	sess.sessionPath, sess.sessionID = newLog.Path(), newLog.ID()
	if len(sess.registry.AddedDirs()) > 0 {
		// The roots stay in force across /clear — startup, slash and
		// requested alike — so the new transcript records them too
		// (ADR-0099 §12).
		_ = newLog.Log("added_directories", sess.granter.record())
	}
	if err := session.Export(sess.sessionID); err != nil {
		note("cannot set %s: %v", session.EnvVar, err)
	}
	sess.hookSession = hooks.Session{ID: sess.sessionID, TranscriptPath: sess.sessionPath, CWD: sess.projectDir}
	if sess.workDir != "" {
		workdir.RemoveIfEmpty(sess.workDir)
	}
	sess.workDir = ""
	if dir, err := workdir.Ensure(sess.projectDir, sess.sessionID); err != nil {
		note("session work directory unavailable: %v", err)
	} else {
		sess.workDir = dir
	}
	// Exported or UNSET: the MCP servers reconnected below inherit
	// the environment, and the old directory must not survive in
	// it (review after v0.68.0).
	if err := exportWorkDir(sess.workDir); err != nil {
		note("cannot set %s: %v", workdir.EnvVar, err)
	}
	// Every consumer of the work directory follows it (review round
	// 4): the file tools' second root, the sandbox profile, the MCP
	// intake (it reads the registry), and the system prompt — a
	// cleared conversation has no cached prefix to protect.
	// A cleared session starts unloaded, preloads aside (ADR-0083
	// §8); the rebuilt system prompt below carries the same list.
	sess.adv.Reset()
	sess.ag.RefreshTools()
	// Still under the grant lock taken above: a late grant cannot
	// swap in a runner built against the old work directory.
	notes = append(notes, rotateWorkDir(sess.registry, sess.shellExec, sess.sandboxOn, sess.projectDir, sess.workDir, sess.cfg.Sandbox.ReadLaneDenyExec, sess.cfg.Sandbox.Caches(), sess.cfg.Sandbox.ReadLanePrompts, trustpin.Parents(sess.projectDir, sess.persistentSnap), sess.registry.AddedDirs(), func() { sess.ag.SetSystem(sess.composeSystem()) }, sess.granter.forget)...)
	sess.granter.endClear()
	// A cleared session restarts what carries its identity (ADR-0071
	// addendum): telemetry is re-resourced with the new id, and the
	// MCP servers — spawned at startup with the old id in their
	// environment and arguments — are reconnected the way /mcp
	// reload does, so a server keeping per-session state (the
	// board) sees the session the hooks report. Measured: without
	// this the board's child kept --session <old id> across /clear.
	if err := sess.sink.Restart(sess.ctx, sess.sessionID); err != nil {
		note("audit events for this session continue under the previous session id (%v)", err)
	}
	if sess.cfg.MCP.Enabled {
		extra.WriteString(sess.reconnectMCP(false))
	}
	sess.sink.SessionStart(sess.cfg.Model.Name, sess.sandboxOn, sess.ag.AutoApprove(), len(sess.mcpClients))
	sess.sessionStartHooks("clear")
	return render()
}

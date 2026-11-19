package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/approve"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/hooks"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/mcpfilter"
	"github.com/nlink-jp/gem-agent/internal/mediastore"
	"github.com/nlink-jp/gem-agent/internal/memory"
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/repl"
	"github.com/nlink-jp/gem-agent/internal/riskbook"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
	"github.com/nlink-jp/gem-agent/internal/workdir"
)

// The startup phases (ADR-0124 §2.2). Each takes what it needs from the
// phases before it as arguments and returns what the phases after it
// read; the order runREPL calls them in is today's order, so every
// warning prints where it printed before.

// runSettings is what the run resolved before anything of the project is
// read: where the config came from, the session's shape and its chrome.
type runSettings struct {
	cfgPath      string
	oneShot      bool
	autoOn       bool
	uiLang       uitext.Lang
	ceiling      sandbox.Ceiling
	initialInput string
	ladder       *interruptLadder
}

// admission is what the project was admitted with: its trust, its
// policy, and the note a `!` command's output carries.
type admission struct {
	home             string
	approvalPolicy   policy.Policy
	projectMayLoosen bool
	policyNotes      []policy.Note
	afterShell       func() string
}

// opened is the transcript openTranscript put in place, and the history
// a resume restores. prepareWorkDir takes it: the work directory is keyed
// by the session id, so it cannot be made before the transcript is open.
type opened struct {
	tr          *transcript
	resumedID   string
	resumedFrom time.Time
	restored    []llm.Message
}

// confinement is what the lanes were measured to enforce here.
type confinement struct {
	enforcement sandbox.Enforcement
}

// loaded is what loadProject read beyond the project-content and MCP
// holders: the memories and the operator's screen.
type loaded struct {
	memBase  string
	memories []memory.Memory
	screen   *tui.Screen
}

// models is the model tier: the slots derived from the session's client,
// the usage tally and the media route.
type models struct {
	riskBackend    llm.Backend
	riskModel      string
	sideLog        liveLog
	summaryModel   string
	summaryBackend modelClient
	tally          *usageTally
	mediaUpload    func(callCtx context.Context, f *os.File, name, mime string) (string, error)
}

// frontEnd is the session's face: TUI or not, and what goes with it.
type frontEnd struct {
	useTUI  bool
	images  termimg.Protocol
	gate    agent.Approver
	tuiGate *tui.Gate
	reader  *repl.Reader
}

// registered is what registerTools returns, and only it: assembleSession
// takes it, because the agent caches the tool declarations when it is
// built and every tool must be registered by then.
type toolsRegistered struct {
	askOperator  askFunc
	onRoundLimit func(ctx context.Context, info agent.RoundLimitInfo) bool
}

// assembled is what the front ends need of the assembled session.
type assembled struct {
	rbRunner     *riskbookRunner
	reloads      slashReloads
	settingsData tui.SettingsData
}

func resolveRun(sess *liveSession, cmd *cobra.Command, fl runFlags, args []string) (runSettings, error) {
	var err error
	// --- config ---
	cfgPath := fl.config
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return runSettings{}, err
		}
		cfgPath = p
	}
	oneShot := fl.prompt != ""
	roFlag, roAutoFlag, err := readOnlyOverride(fl.writable, fl.readOnly, fl.autoRO, oneShot)
	if err != nil {
		return runSettings{}, err
	}
	sess.cfg, err = config.LoadWithOverrides(cfgPath, config.Overrides{
		Model: fl.model, Thinking: fl.thinking, MCP: fl.mcp, Auto: fl.auto,
		ReadOnly: roFlag, ReadOnlyAuto: roAutoFlag})
	if err != nil {
		return runSettings{}, err
	}
	// The first interactive turn, from the positional argument
	// (ADR-0064). Never combined with -p: the two select different
	// session shapes, and ambiguity is refused, not resolved.
	initialInput, err := firstMessage(args, oneShot)
	if err != nil {
		return runSettings{}, err
	}
	// Everything downstream — the agent, telemetry — reads this one
	// effective value, never the raw config field.
	autoOn := effectiveAuto(sess.cfg.Agent.AutoApprove, oneShot, fl.auto)
	// The ceiling this run starts with, resolved once for the same
	// reason (ADR-0080 §1).
	ceiling := effectiveCeiling(sess.cfg.Agent, oneShot)
	// UI language, resolved once (ADR-0029): the chrome that follows —
	// prompts, TUI, slash output — is built with it.
	uiLang := uitext.Resolve(sess.cfg.TUI.Language, os.Getenv)
	sess.msgs = uitext.For(uiLang)
	// The escape ladder for turns outside the TUI (ADR-0065 §3): the
	// TUI has its own three-press exit; the plain REPL and one-shot
	// mode get the same one here. The quit skips the deferred flushes
	// on purpose — the transcript is per event, and the warning that
	// preceded it says so.
	ladder := &interruptLadder{
		Interrupting: func() { fmt.Fprintln(cmd.ErrOrStderr(), sess.msgs.StatusInterrupting) },
		Warn:         func() { fmt.Fprintln(cmd.ErrOrStderr(), sess.msgs.InterruptStuckWarn) },
		Quit: func() {
			fmt.Fprintln(cmd.ErrOrStderr(), sess.msgs.Bye)
			os.Exit(130)
		},
	}

	return runSettings{cfgPath: cfgPath, oneShot: oneShot, autoOn: autoOn, uiLang: uiLang, ceiling: ceiling, initialInput: initialInput, ladder: ladder}, nil
}

func admitProject(sess *liveSession, cmd *cobra.Command, fl runFlags, rs runSettings) (admission, error) {
	var err error
	// --- project directory ---
	sess.projectDir, err = currentProject()
	if err != nil {
		return admission{}, err
	}

	// --- startup safety (ADR-0023) ---
	// Both gates read one unbuffered line from stdin before the REPL/TUI
	// takes over. One-shot mode counts as non-interactive even on a TTY:
	// a scripted -p must behave deterministically.
	interactive := !rs.oneShot && term.IsTerminal(int(os.Stdin.Fd()))
	home, _ := os.UserHomeDir()
	if reason := broadRoot(sess.projectDir, home); reason != "" {
		if err := confirmBroadRoot(reason, sess.projectDir, interactive, os.Stdin, cmd.ErrOrStderr(), sess.msgs); err != nil {
			return admission{}, err
		}
	}

	sess.policyPath = config.PolicyPath(rs.cfgPath)
	sess.policyFile, err = config.LoadPolicyFile(sess.policyPath)
	if err != nil {
		return admission{}, err
	}

	// --- first-run project trust (ADR-0023): does this project's own
	// instruction files / .mcp.json / skills get loaded at all? ---
	var trustNote string
	sess.projectTrusted, trustNote = resolveProjectTrust(
		sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, interactive, os.Stdin, cmd.ErrOrStderr(), sess.msgs)
	if trustNote != "" {
		fmt.Fprintf(sess.stderr, "%s\n", trustNote)
	}
	// Content pins (ADR-0074): trust was given to a directory; what is
	// loaded is content, and content that changed since asks again.
	// Decided before anything of the project is read — .gem-agent.toml
	// below included.
	sess.grant = projectGrant{trusted: sess.projectTrusted}
	var pinNotes []string
	sess.grant.excluded, pinNotes = checkPins(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, interactive, os.Stdin, cmd.ErrOrStderr(), sess.msgs)
	for _, n := range pinNotes {
		fmt.Fprintf(sess.stderr, "%s\n", n)
	}

	// --- per-tool approval policy (ADR-0008) ---
	// The project half may tighten freely and may only loosen where the
	// operator trusted this directory and its content is what they
	// trusted: a checked-out repository must not be able to switch the
	// gate off.
	var projectMayLoosen bool
	sess.projectCfg, projectMayLoosen, err = loadProjectConfig(sess.cfg, sess.projectDir, sess.grant)
	if err != nil {
		return admission{}, err
	}
	// The MCP tool filter (ADR-0077): what this session does not have.
	// Built once — the reload re-derives what it removes, but the files
	// it is built from are config, and config changes need a restart
	// like every other key. A malformed entry refuses to start: an
	// exclusion that does not do what it says is worse than none, and
	// falling back to "exclude nothing" would silently hand back the
	// write functions the operator took away.
	sess.mcpFilter, err = mcpfilter.Build(sess.cfg.MCP.Exclude, policyScope(sess.policyFile), sess.projectCfg.MCP.Exclude)
	if err != nil {
		return admission{}, err
	}
	mergedTools := map[string]string{}
	for k, v := range sess.cfg.Approval.Tools {
		mergedTools[k] = v
	}
	// The machine-owned file wins: a change made through /settings must
	// not be silently overridden by the hand-written config (ADR-0009).
	for k, v := range sess.policyFile.ForProject(sess.projectDir) {
		mergedTools[k] = v
	}
	// --allow entries sit above both files (flags > machine-owned >
	// hand-written, the order the config system already declares) and
	// compile into the same policy build — so the project tighten, the
	// Block floor, hooks, and the bare-"*" ban all hold without knowing
	// the flag exists (ADR-0053 §2).
	if err := applyAllowFlag(mergedTools, fl.allow); err != nil {
		return admission{}, err
	}
	// Learned command rules are parsed but NOT applied (ADR-0049 §3):
	// /learn is withdrawn, nothing displays or manages those entries, and
	// invisible standing permissions are the state the withdrawal exists
	// to end. Ignoring them only ever tightens. The note tells the
	// operator where the entries live.
	approvalPolicy, policyNotes, err := policy.Build(
		mergedTools, sess.projectCfg.Approval.Tools, nil, projectMayLoosen)
	if err != nil {
		return admission{}, err
	}
	if n := len(sess.policyFile.CommandsFor(sess.projectDir)); n > 0 {
		fmt.Fprintf(sess.stderr, "note: %s has %d obsolete [projects.<dir>.commands] entries — ignored; delete them to silence this note\n", sess.policyPath, n)
	}

	// The persistent-file snapshot (ADR-0074 §3/§4): what this session
	// starts from, compared with what the previous session left, and
	// compared again at the end so what the session added or changed
	// is said — the parent directories it names are denied by name in
	// the write lane.
	var persistentCut bool
	sess.persistentSnap, persistentCut = trustpin.Snapshot(sess.projectDir)
	if persistentCut {
		fmt.Fprintf(sess.stderr, "note: change detection covers the first %d files under the project only\n", trustpin.WalkEntries)
	}
	sess.persistentPath, sess.persistentPathErr = persistentStateFile(sess.projectDir)
	if sess.persistentPathErr == nil {
		if prev := loadPersistentSnapshot(sess.persistentPath); prev != nil {
			if a, c, r := trustpin.SnapshotDiff(prev, sess.persistentSnap); len(a)+len(c)+len(r) > 0 {
				fmt.Fprintf(sess.stderr, sess.msgs.PersistentSinceLastFmt+"\n", describeSnapshotDiff(a, c, r))
			}
		}
		_ = savePersistentSnapshot(sess.persistentPath, sess.persistentSnap)
	}
	// What a `!` command changed in the files later sessions trust is
	// said after its output.
	afterShell := func() string {
		return pinChangesNote(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, sess.msgs)
	}

	return admission{home: home, approvalPolicy: approvalPolicy, projectMayLoosen: projectMayLoosen, policyNotes: policyNotes, afterShell: afterShell}, nil
}

func openTranscript(sess *liveSession, cl *cleanups, fl runFlags) (opened, error) {
	// --- session transcript: the log, and the resume source (ADR-0005) ---
	if fl.continueLast && fl.resume != "" {
		return opened{}, fmt.Errorf("--continue and --resume name different sessions; use one")
	}
	sess.sessionDir, sess.sessionDirErr = session.DefaultDir()

	var restored []llm.Message
	resumedID := ""
	var resumedFrom time.Time // when a resumed conversation began (ADR-0097)
	if fl.continueLast || fl.resume != "" {
		if sess.sessionDirErr != nil {
			return opened{}, fmt.Errorf("cannot resume: %w", sess.sessionDirErr)
		}
		meta, err := resolveResume(sess.sessionDir, sess.projectDir, sess.cfg.Model.Name, fl.resume)
		if err != nil {
			return opened{}, err
		}
		resumedID = meta.ID
		resumedFrom = meta.Started
		// restored is loaded below, AFTER Reopen holds the flock.
	}

	// A broken log warns; a session that cannot be recorded is still a
	// session, and degrading beats refusing to start. A broken *resume*
	// is fatal, though — the operator asked for that history, and
	// continuing without it silently would be worse than stopping.

	sess.sessionPath = "(disabled)"
	sess.sessionID = ""
	// curLog is the transcript in use; /clear replaces it (ADR-0071 §2),
	// so the exit-time close reads the holder, not a snapshot.
	cl.push("close the transcript", func() {
		if sess.curLog != nil {
			_ = sess.curLog.Close()
		}
	})
	if sess.sessionDirErr != nil {
		fmt.Fprintf(sess.stderr, "warning: session log disabled: %v\n", sess.sessionDirErr)
	} else {
		lg, err := openSessionLog(sess.sessionDir, resumedID, sess.projectDir, sess.cfg.Model.Name, sess.cfg.GCP.Location, sess.version)
		switch {
		case err != nil && resumedID != "":
			return opened{}, fmt.Errorf("cannot append to session %s: %w", resumedID, err)
		case err != nil:
			fmt.Fprintf(sess.stderr, "warning: session log disabled: %v\n", err)
		default:
			sess.curLog = lg
			sess.sessionLog = lg
			sess.sessionPath = lg.Path()
			sess.sessionID = lg.ID()
			// Exported before any MCP server starts, so a registration
			// line can hand the session to a server that keeps
			// per-session state (ADR-0069 addendum 2).
			if err := session.Export(sess.sessionID); err != nil {
				fmt.Fprintf(sess.stderr, "warning: cannot set %s: %v\n", session.EnvVar, err)
			}
			if resumedID != "" {
				// Under the flock (Reopen holds it): the file we read
				// is exactly the file we will append to, with no
				// window for another process's tail (review round 2).
				history, resumeNotes, err := loadResumedHistory(lg, resumedID)
				if err != nil {
					return opened{}, err
				}
				for _, n := range resumeNotes {
					fmt.Fprintf(sess.stderr, "warning: %s\n", n)
				}
				restored = history
			}
		}
	}

	return opened{tr: sess.transcript, resumedID: resumedID, resumedFrom: resumedFrom, restored: restored}, nil
}

func prepareWorkDir(sess *liveSession, cl *cleanups, op opened) *workArea {
	// --- session work directory (ADR-0058) ---
	// Everything the session produces outside the project lands here: an
	// MCP result too large to hold in context, binary a server returned,
	// scratch a shell command wrote. It has to exist BEFORE the sandbox
	// profile is built (it is a writable root) and before the MCP
	// servers start, because ${GEMAGENT_WORK_DIR} in an mcp.json args
	// entry is expanded at load time from the process environment.
	//
	// That ordering is why the session block above was moved ahead of
	// this one: the directory is keyed by session id, so a resume lands
	// back in the directory its earlier self used.
	// The project directory is the third fact a child sees (ADR-0071
	// §3), beside the session id and the work directory.
	if err := os.Setenv(workdir.ProjectEnvVar, sess.projectDir); err != nil {
		fmt.Fprintf(sess.stderr, "warning: cannot set %s: %v\n", workdir.ProjectEnvVar, err)
	}
	sess.workDir = ""
	cl.push("remove the fallback scratch", removeFallbackScratch)
	cl.push("remove the work directory if empty", func() {
		if sess.workDir != "" {
			workdir.RemoveIfEmpty(sess.workDir)
		}
	})
	if op.tr.sessionID != "" {
		if dir, err := workdir.Ensure(sess.projectDir, op.tr.sessionID); err != nil {
			// A missing work directory degrades the session (oversized
			// MCP results get truncated with a note); it does not stop
			// the session from starting.
			fmt.Fprintf(sess.stderr, "warning: session work directory unavailable: %v\n", err)
			// An inherited value (a nested launch) must not stand in.
			_ = os.Unsetenv(workdir.EnvVar)
		} else {
			sess.workDir = dir
			// Exported, not passed: this is what puts the path in front
			// of shell_exec's child, every MCP server (internal/mcp
			// inherits os.Environ), and every hook, without any of them
			// needing to know gem-agent's layout.
			if err := exportWorkDir(sess.workDir); err != nil {
				fmt.Fprintf(sess.stderr, "warning: cannot set %s: %v\n", workdir.EnvVar, err)
			}
			// Gated on bytes, not on the count (ADR-0078 §4): two empty
			// leftovers are not an accumulation, and a line reading "0B"
			// asks the operator to look at nothing.
			// `more` means the scan was cut, so bytes is a lower bound:
			// a truncated sweep whose partial sum is under the floor
			// would suppress the note the floor exists to make
			// meaningful (pre-release review).
			if dirs, bytes, more, err := workdir.Sweep(sess.projectDir, op.tr.sessionID); err == nil &&
				(bytes >= workDirNoteFloor || (more && bytes > 0)) {
				plus := ""
				if more {
					plus = "+" // the startup scan was cut: a lower bound
				}
				fmt.Fprintf(sess.stderr, "note: %d%s earlier session work dir(s) hold %s%s — delete them with 'gem-agent workdirs clean'\n",
					dirs, plus, humanBytes(bytes), plus)
			}
		}
	}

	return sess.workArea
}

func buildConfinement(sess *liveSession, fl runFlags, rs runSettings, adm admission, wa *workArea) (confinement, error) {
	var err error
	// --- shell execution strategy (ADR-0001 defense-in-depth) ---
	sess.sandboxOn = sess.cfg.Sandbox.Enabled && !fl.noSandbox
	if sess.sandboxOn {
		// Fail closed (ADR-0073 §5): a sandbox that cannot apply here
		// (a nested Seatbelt) must not degrade silently into unconfined
		// execution — the operator says --no-sandbox, or nothing runs.
		if err := sandbox.Available(); err != nil {
			return confinement{}, fmt.Errorf("the sandbox cannot be applied here (%v); every shell command would run unconfined — pass --no-sandbox to accept that explicitly", err)
		}
	}
	// The operator's added directories (ADR-0099 §3): judged by the one
	// rule before anything uses them, and a refusal stops the start —
	// the operator typed the path and must hear it did not apply. A
	// configured one that does not fit this session is told and skipped
	// (ADR-0114 §1).
	added, skippedDirs, err := resolveAddedDirs(sess.cfg, sess.projectDir, wa.workDir, rs.cfgPath, fl.addDir, fl.readDir)
	if err != nil {
		return confinement{}, err
	}
	for _, s := range skippedDirs {
		_, _ = fmt.Fprintf(sess.stderr, sess.msgs.AddedSkippedFmt+"\n", s)
	}
	if len(added) > 0 {
		_ = sess.log("added_directories", addedDirsRecord(added))
	}
	// Persistent files in the write roots are watched (ADR-0099 §10):
	// what changed since the previous session is said now, what this
	// session changes at /clear and at exit.
	sess.watch = newAddedWatch(added, sess.projectDir, sess.stderr, sess.msgs)
	execFn, enforcement, laneNotes, err := buildExecFn(sess.sandboxOn, sess.projectDir, wa.workDir, sess.cfg.Sandbox.ReadLaneDenyExec, sess.cfg.Sandbox.Caches(), trustpin.Parents(sess.projectDir, sess.persistentSnap), rootsOf(added))
	if err != nil {
		return confinement{}, err
	}
	if sess.cfg.Sandbox.ReadLanePrompts {
		// The operator's opt-out (ADR-0073 §5): read-lane commands keep
		// their cage and their prompt.
		enforcement.ReadLane = false
	}
	for _, n := range laneNotes {
		fmt.Fprintf(sess.stderr, "warning: %s\n", n)
	}
	// The strategy is swapped when /clear rotates the work directory
	// (ADR-0071 §2): the sandbox profile names the directory, so a
	// profile built at startup denied every write to the new one.
	sess.shellExec = &liveExec{fn: execFn}

	sess.registry, err = tools.New(sess.projectDir, nil, time.Duration(sess.cfg.Agent.ShellTimeoutSec)*time.Second)
	if err != nil {
		return confinement{}, err
	}
	// The lanes (ADR-0073): a read-lane command runs without approval
	// only where the read lane's denials were verified on this machine;
	// with the sandbox off every shell call is the operator's alone.
	sess.registry.SetLaneExec(sess.shellExec.run, enforcement)
	if wa.workDir != "" {
		// The file tools get the work directory as a second root, so a
		// result the intake saved is one read_file can read back. Without
		// it the model sees paths it cannot open and routes around the
		// built-ins with shell redirection, which is less reviewable.
		if err := sess.registry.UseWorkDir(wa.workDir); err != nil {
			fmt.Fprintf(sess.stderr, "warning: session work directory not readable by the file tools: %v\n", err)
		}
	}
	if err := sess.registry.AddDirs(rootsOf(added)); err != nil {
		return confinement{}, err
	}
	// Mid-session grants (ADR-0099 §12): the operator's /add-dir and
	// /read-dir and the model's request_directory share this one path.
	sess.granter = &dirGranter{
		registry: sess.registry, shellExec: sess.shellExec, sandboxOn: sess.sandboxOn,
		projectDir: realOrSelf(sess.projectDir), workDir: func() string { return wa.workDir },
		denyExec: sess.cfg.Sandbox.ReadLaneDenyExec, caches: sess.cfg.Sandbox.Caches(), readLanePrompts: sess.cfg.Sandbox.ReadLanePrompts,
		parents: func() []string { return trustpin.Parents(sess.projectDir, sess.persistentSnap) },
		base:    rootBaseContext(rs.cfgPath), watch: sess.watch, sources: map[string]string{}, msgs: sess.msgs,
		log: sess.log,
	}
	for _, a := range added {
		sess.granter.sources[a.Dir] = a.Source
	}
	// The file tools' reads run in a child under a profile that denies
	// the credential list at the kernel (ADR-0086), proven on this
	// machine before it is trusted. Where it cannot be proven the reads
	// stay in process with the matcher as the boundary, and the note
	// says so rather than letting the banner imply a cage.
	if probeDir, doneProbe := fileProbeDir(wa.workDir); probeDir != "" {
		note := installFileChild(sess.registry, adm.home, probeDir)
		doneProbe()
		if note != "" {
			fmt.Fprintf(sess.stderr, "warning: %s\n", note)
		}
	} else {
		fmt.Fprintf(sess.stderr, "warning: file reads are not sandboxed (no probe directory)\n")
	}

	return confinement{enforcement: enforcement}, nil
}

func loadProject(sess *liveSession, cmd *cobra.Command, cl *cleanups, fl runFlags, op opened) (loaded, error) {
	// --- project instruction files (drop-in: AGENTS.md and friends,
	// including ancestor directories, exactly as other agents read them)
	// Read here, with the skills, before any process the project names
	// (an MCP server) starts: the pins were digested moments ago, and
	// the gap between digest and read is kept to this (ADR-0074).
	sess.projectContext, sess.contextLabels, sess.contextNotes = loadInstructions(sess.projectDir, sess.grant)
	for _, n := range sess.contextNotes {
		fmt.Fprintf(sess.stderr, "warning: instruction file %s\n", n)
	}

	// --- skills: Claude Code's skill library, read as-is (ADR-0010) ---
	// skillsList and mcpSummary/mcpClients below are reassigned by the
	// /skills reload and /mcp reload closures (ADR-0039); every consumer
	// reads the variables through a closure, so updates propagate. The
	// single-writer discipline holds because a slash command cannot run
	// while a turn is in flight.
	var skillNotes []string
	sess.skillsList, skillNotes = discoverSkills(sess.projectDir, sess.grant)
	cl.push("close the skills' roots", func() { skills.CloseAll(sess.skillsList) }) // reads the variable: reloads replace it
	for _, n := range skillNotes {
		fmt.Fprintf(sess.stderr, "warning: %s\n", n)
	}
	if err := registerSkillTool(sess.registry, func() []skills.Skill { return sess.skillsList }); err != nil {
		return loaded{}, err
	}

	// --- MCP servers from the project's .mcp.json (drop-in) ---
	// What of them the model is shown is the advertiser's (ADR-0083):
	// everything under [mcp].advertise = "all", loaded tools only under
	// "on-request". It records committed loads to the transcript in use.
	sess.adv = newMCPAdvertiser(sess.cfg.MCP.OnRequest(), sess.cfg.MCP.Preload, fl.allow, liveLog{get: func() agent.SessionLog { return sess.sessionLog }})
	// screen is the route to the operator's screen, late-bound because
	// the program does not exist yet (ADR-0090 §1's plumbing, which
	// ADR-0091 keeps while withdrawing the intake as its source). What
	// sends through it now is show_image, which the model calls, and
	// /show, which the operator types. Unbound it is inert.
	screen := tui.NewScreen()
	sess.mcpClients, sess.mcpSummary, sess.mcpInv = connectMCPServers(sess.ctx, sess.cfg, sess.projectDir, cmd.Root().Version, sess.registry, sess.stderr, sess.grant, sess.mcpFilter)
	sess.adv.setInventory(sess.mcpInv, registeredIn(sess.registry))
	if op.resumedID != "" && sess.cfg.MCP.OnRequest() {
		// A resumed session sees what it saw (ADR-0083 §8): the loads
		// its transcript recorded, re-advertised; what is gone, named.
		if _, missing, err := replayAdvertised(sess.sessionPath, sess.adv); err != nil {
			fmt.Fprintf(sess.stderr, "warning: MCP loads of the resumed session were not replayed: %v\n", err)
		} else if len(missing) > 0 {
			fmt.Fprintf(sess.stderr, "warning: the resumed session had loaded %s, which this session does not have\n", strings.Join(missing, ", "))
		}
	}
	cl.push("close the MCP clients", func() {
		for _, c := range sess.mcpClients {
			c.Close()
		}
	})

	// --- agent memory: facts persisted across sessions (ADR-0020) ---
	// A missing home disables memory with a warning; the runtime must
	// not refuse to start over its least essential feature.
	memBase := ""
	var memories []memory.Memory
	if base, err := memory.DefaultDir(); err == nil {
		memBase = base
		var memNotes []string
		memories, memNotes = memory.Load(memBase, sess.projectDir, memory.DefaultLimits())
		for _, n := range memNotes {
			fmt.Fprintf(sess.stderr, "warning: %s\n", n)
		}
		if err := registerMemoryTools(sess.registry, memBase, sess.projectDir); err != nil {
			return loaded{}, err
		}
	} else {
		fmt.Fprintf(sess.stderr, "warning: memory disabled: %v\n", err)
	}
	sess.memorySection = ""
	if memBase != "" {
		sess.memorySection = memory.PromptSection(memories)
	}

	return loaded{memBase: memBase, memories: memories, screen: screen}, nil
}

func buildModels(sess *liveSession, cmd *cobra.Command, cl *cleanups, rs runSettings) (models, error) {
	var err error
	// --- telemetry: audit events to the operator's collector (ADR-0035) ---
	// Default off. Failures never hurt the session: creation errors
	// warn and fall back to the no-op sink.
	sess.sink = telemetry.Nop()
	// Where operator-facing output goes (ADR-0124 §2.4): the TUI while
	// prog runs, stderr before, after and without one. Built here, ahead
	// of the telemetry exporter, whose warnings it routes; the plain
	// REPL's answer reader joins it with stdin (assembleSession).
	sess.surf = &surface{w: sess.stderr, term: cmd.ErrOrStderr(), msgs: sess.msgs, oneShot: rs.oneShot}
	if sess.cfg.Telemetry.Enabled {
		if s, err := telemetry.New(sess.ctx, telemetry.Config{
			Enabled: true, Backend: sess.cfg.Telemetry.Backend,
			Endpoint: sess.cfg.Telemetry.Endpoint, Insecure: sess.cfg.Telemetry.Insecure,
			HeadersFile: sess.cfg.Telemetry.HeadersFile,
		}, sess.cfg.GCP.Project, cmd.Root().Version, sess.sessionID, sess.projectDir, sess.surf.backgroundWarning); err != nil {
			fmt.Fprintf(sess.stderr, "warning: telemetry disabled: %v\n", err)
		} else {
			sess.sink = s
			cl.push("flush telemetry", func() {
				// The flush is bounded (3s) but was silent, and a
				// silent wait after the last Ctrl+C reads as a hang
				// (ADR-0065 §4). One-shot mode stays quiet for
				// pipelines, like the exit receipt.
				if !rs.oneShot {
					fmt.Fprintln(cmd.ErrOrStderr(), sess.msgs.ExitFlushing)
				}
				sess.sink.Shutdown()
			})
		}
	}
	// The effective auto state, not the raw config field: one-shot mode
	// disarms auto-approve, and an audit event claiming it was armed in
	// a run where it never could be is a false record (ADR-0053 §4).
	sess.sink.SessionStart(sess.cfg.Model.Name, sess.sandboxOn, rs.autoOn, len(sess.mcpClients))
	cl.push("end the telemetry session", sess.sink.SessionEnd) // last-first: runs before Shutdown's flush

	// --- LLM backend ---
	sess.backend, err = newModelClient(sess.ctx, sess.cfg)
	if err != nil {
		return models{}, err
	}
	// Retries reach the transcript in use, whichever /clear made it
	// (ADR-0071 §2), from the main loop and from every slot below.
	recordRetries(sess.backend, liveLog{get: func() agent.SessionLog { return sess.sessionLog }})

	// Per-category token accounting for /usage (ADR-0019).
	tally := newUsageTally()

	// Media uploads (ADR-0027): with [gcp].bucket set, audio/video
	// attachments route through the operator's bucket as gs:// URIs.
	// The client is created lazily on first use — most sessions attach
	// no media.
	var mediaUpload func(callCtx context.Context, f *os.File, name, mime string) (string, error)
	if sess.cfg.GCP.Bucket != "" {
		// Lazy, but retryable: sync.Once pinned the FIRST construction
		// error (a transient DNS/ADC hiccup) for the whole session
		// (review round 2). Only success is cached. The client rides
		// the session context; each upload rides the TURN context so
		// Ctrl+C reaches it.
		var (
			upMu sync.Mutex
			up   *mediastore.Uploader
		)
		mediaUpload = func(callCtx context.Context, f *os.File, name, mime string) (string, error) {
			upMu.Lock()
			if up == nil {
				u, err := mediastore.New(sess.ctx, sess.cfg.GCP.Project, sess.cfg.GCP.Bucket)
				if err != nil {
					upMu.Unlock()
					return "", err
				}
				up = u
			}
			u := up
			upMu.Unlock()
			uri, err := u.UploadFile(callCtx, f, filepath.Ext(name), mime)
			if err == nil {
				var size int64
				if fi, statErr := f.Stat(); statErr == nil {
					size = fi.Size()
				}
				sess.sink.MediaUpload(size, uri)
			}
			return uri, err
		}
	}

	// --- summarize_file: the summary model shares the client (ADR-0014) ---
	summaryModel := sess.cfg.Model.Summary
	summaryBackend := sess.backend
	if summaryModel == "" {
		summaryModel = sess.cfg.Model.Name
	} else {
		summaryBackend = sess.backend.WithModel(summaryModel)
	}
	// --- the model tier: risk evaluation, progress review, and the
	// read-only watcher run on their own slot once the operator names
	// one (ADR-0082). Same client; a nil backend means "ride the main
	// backend", which is what the agent does with it.
	riskModel := sess.cfg.Model.RiskModel()
	var riskBackend llm.Backend
	if sess.cfg.Model.RiskSlot() {
		riskBackend = sess.backend.WithModel(riskModel).WithThinking(sess.cfg.Model.RiskThinking, "[model].risk_thinking")
	}
	// sideLog forwards to the transcript in use: /clear swaps it
	// (ADR-0071 §2), and a tool that captured the value at startup wrote
	// its usage records to the closed file (review round 4).
	sideLog := liveLog{get: func() agent.SessionLog { return sess.sessionLog }}
	if err := registerSummarizeTool(sess.registry, summaryBackend, summaryModel, sideLog, tally); err != nil {
		return models{}, err
	}
	// Web access (ADR-0017): grounded search on the main model, digested
	// fetch on the lightweight one. Both egress-gated by default.
	if err := registerWebTools(sess.registry, sess.backend, summaryBackend, sess.cfg.Model.Name, summaryModel, sideLog, tally); err != nil {
		return models{}, err
	}
	// --- on-request MCP (ADR-0083): the two loading tools, and the
	// librarian's slot — its own when named, otherwise the backend the
	// model tier resolves to (§2). Registered only under on-request, so
	// an operator who did not opt in has today's tool list exactly.
	librarianModel, librarianBackend := riskModel, riskBackend
	if librarianBackend == nil {
		librarianBackend = sess.backend
	}
	if sess.cfg.Model.LibrarianSlot() {
		librarianModel = sess.cfg.Model.LibrarianModel()
		librarianBackend = sess.backend.WithModel(librarianModel).WithThinking(sess.cfg.Model.LibrarianThinking, "[model].librarian_thinking")
	}
	if sess.cfg.MCP.OnRequest() {
		if err := registerMCPLoadTool(sess.registry, sess.adv); err != nil {
			return models{}, err
		}
		if err := registerFindToolsTool(sess.registry, sess.adv, librarianBackend, librarianModel, sideLog, tally, librarianLanguage(rs.uiLang)); err != nil {
			return models{}, err
		}
	}

	return models{riskBackend: riskBackend, riskModel: riskModel, sideLog: sideLog, summaryModel: summaryModel, summaryBackend: summaryBackend, tally: tally, mediaUpload: mediaUpload}, nil
}

func chooseFrontEnd(sess *liveSession, cmd *cobra.Command, rs runSettings) frontEnd {
	// The TUI needs a real terminal on both ends (ADR-0002); piped use
	// falls back to the plain line REPL so scripts and smoke pipelines
	// keep working.
	useTUI := !rs.oneShot &&
		term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

	// One shared buffered reader for the plain REPL and its approval
	// gate: bufio.NewReader returns an existing *bufio.Reader unchanged,
	// so both components drain the same buffer and no typed-ahead input
	// is stranded in a second one. (The TUI reads the terminal itself.)
	stdin := bufio.NewReader(cmd.InOrStdin())
	// The inline-image capability is resolved ONCE, here: before
	// tea.NewProgram (ADR-0089 §7), and only for a session that will have
	// a UI — the probe writes escapes to the terminal and a one-shot or a
	// piped run has none to write to. The settings panel answers with it
	// too, so it cannot be resolved twice or answered differently in the
	// two places.
	images := termimg.None
	if useTUI {
		images = resolveImages(sess.cfg.TUI.Images)
	}
	var gate agent.Approver
	var tuiGate *tui.Gate
	switch {
	case rs.oneShot:
		// One-shot runs non-interactively (stdin may be a pipe): a
		// blocking approval prompt would hang, so mutating tools are
		// denied outright. Read-only pipelines still work.
		gate = denyGate{out: sess.stderr}
	case useTUI:
		tuiGate = tui.NewGate()
		gate = tuiGate
	default:
		gate = approve.New(stdin, sess.stderr)
	}
	reader := repl.NewReader(stdin, sess.stderr)

	// The plain REPL's answer reader shares stdin with the line reader
	// (the surface itself was built in buildModels).
	sess.surf.plainAsk = plainAsk(stdin, cmd.ErrOrStderr())
	if !useTUI {
		// No program will replay what the terminal already showed.
		sess.surf.withoutTUI()
	}
	// Turn observability (ADR-0033): stream heartbeat, retries, and
	// thought summaries reach the TUI when one is running; the plain
	// REPL and one-shot mode stay quiet — their output goes to pipes.
	sess.backend.SetObserver(sess.surf.stream)

	return frontEnd{useTUI: useTUI, images: images, gate: gate, tuiGate: tuiGate, reader: reader}
}

func registerTools(sess *liveSession, cmd *cobra.Command, rs runSettings, cf confinement, ld loaded, md models) (toolsRegistered, error) {
	// --- ask_user: a structured mid-turn choice (ADR-0036) ---
	// Registered before agent.New (declarations are cached there). The
	// asker picks its mode at call time: one-shot has nobody to ask,
	// the TUI shows a dialog, the plain REPL reads a number. The same
	// asker carries the round-limit dialog (ADR-0040).
	// The plain-REPL asker reads the SHARED stdin reader (surface.ask).
	askOperator := sess.surf.ask
	if err := registerAskTool(sess.registry, askOperator); err != nil {
		return toolsRegistered{}, err
	}

	// --- round-limit intervention dialog (ADR-0040) ---
	// nil in one-shot mode: nobody to ask, so the progress review alone
	// decides there (fail-closed inside the agent).
	var onRoundLimit func(ctx context.Context, info agent.RoundLimitInfo) bool
	if !rs.oneShot {
		onRoundLimit = func(rctx context.Context, info agent.RoundLimitInfo) bool {
			verdict := ""
			switch {
			case info.ReviewErr != "":
				verdict = fmt.Sprintf(sess.msgs.RoundVerdictErrFmt, clipRunes(info.ReviewErr, 80))
			case info.Progressing:
				verdict = fmt.Sprintf(sess.msgs.RoundVerdictProgressFmt, clipRunes(info.Reason, 100))
			default:
				verdict = fmt.Sprintf(sess.msgs.RoundVerdictStuckFmt, clipRunes(info.Reason, 100))
			}
			var q string
			if info.Trigger == "loop" {
				q = fmt.Sprintf(sess.msgs.RoundLoopAskFmt, clipRunes(info.Detail, 80), verdict)
			} else {
				q = fmt.Sprintf(sess.msgs.RoundLimitAskFmt, info.Rounds, info.Cap, verdict)
			}
			idx, err := askOperator(rctx, q, []string{sess.msgs.RoundContinue, sess.msgs.RoundStop})
			return err == nil && idx == 0
		}
	}

	if !rs.oneShot {
		// Only where a human answers (ADR-0099 §4): under -p it could
		// only be denied.
		if err := registerRequestDirectory(sess.registry, sess.granter); err != nil {
			return toolsRegistered{}, err
		}
	}
	// --- agent_info: the model's view of its own runtime (ADR-0030) ---
	// Registered before agent.New (which caches the declarations); the
	// snapshot closure reads `ag` lazily — the tool can only run inside
	// ag.Run, so the pointer is always set by then.
	if err := registerInfoTool(sess.registry, func() infoSnapshot {
		mcpRegistered, mcpAdvertised, mcpWithheld := sess.adv.Counts()
		return infoSnapshot{
			Version:        cmd.Root().Version,
			OSVersion:      macOSVersion(),
			Model:          sess.cfg.Model.Name,
			SummaryModel:   md.summaryModel,
			Thinking:       sess.backend.Thinking(),
			RiskModel:      md.riskModel,
			RiskThinking:   sess.cfg.Model.RiskThinking,
			RiskSlot:       sess.cfg.Model.RiskSlot(),
			MCPOnRequest:   sess.cfg.MCP.OnRequest(),
			MCPRegistered:  mcpRegistered,
			MCPAdvertised:  mcpAdvertised,
			MCPWithheld:    mcpWithheld,
			Usage:          sess.ag.Usage(),
			MaxTurns:       sess.cfg.Agent.MaxTurns,
			ShellTimeout:   sess.cfg.Agent.ShellTimeoutSec,
			AutoApprove:    sess.ag.AutoApprove(),
			AutoCompact:    sess.ag.AutoCompact(),
			CompactAtPct:   sess.cfg.Agent.CompactAtPct,
			SandboxOn:      cf.enforcement.Confined,
			ProjectDir:     sess.projectDir,
			SessionID:      sess.sessionID,
			WorkDir:        sess.workDir,
			MCPServers:     sess.mcpSummary,
			SkillCount:     len(sess.skillsList),
			MemoryOn:       ld.memBase != "",
			MediaBucket:    sess.cfg.GCP.Bucket != "",
			ProjectTrusted: sess.grant.trusted,
		}
	}); err != nil {
		return toolsRegistered{}, err
	}

	// --- agentic_file_search: delegated project search (ADR-0037) ---
	// Registered before agent.New (which caches the declarations), and
	// after summarize_file (the child's tool subset includes it). The
	// child explores in its own context; only its report enters this
	// conversation. Its tool calls render as "↳ tool" so the operator
	// watches the delegation happen instead of a silent pause.
	if err := registerAgenticSearch(sess.registry, agenticSearchOptions{
		backend:     sess.backend,
		modelName:   sess.cfg.Model.Name,
		riskBackend: md.riskBackend,
		riskModel:   md.riskModel,
		log:         md.sideLog,
		tally:       md.tally,
		sink:        sess.sink,
		onToolCall:  func(tc llm.ToolCall) { sess.surf.childToolCall(tc.Name, agent.CallDetail(tc)) },
		onToolDone:  func(tc llm.ToolCall) { sess.surf.childToolDone(tc.Name) },
	}); err != nil {
		return toolsRegistered{}, err
	}

	return toolsRegistered{askOperator: askOperator, onRoundLimit: onRoundLimit}, nil
}

// assembleHooks builds the operator's hooks and the two the agent calls.
func assembleHooks(sess *liveSession) (preToolHook func(ctx context.Context, name string, args map[string]any) (bool, string), promptHook agent.PromptHook) {
	// Operator hooks (ADR-0044 / ADR-0069): global config only. Pre-tool
	// hooks are evaluated ahead of the approval ladder in every mode —
	// the guards exist for the model's calls regardless of surface;
	// context hooks run at session start and on every prompt. nil when
	// none are configured, so the common case pays nothing.
	// Hook notices are collected while a slash command runs the hooks
	// (surface.hookNotice says why).
	hookNotify := sess.surf.hookNotice

	if len(sess.cfg.Hooks.PreToolUse)+len(sess.cfg.Hooks.SessionStart)+len(sess.cfg.Hooks.UserPromptSubmit)+len(sess.cfg.Hooks.SessionEnd) > 0 {
		sess.hookRunner = hooks.New(hooks.Hooks{
			PreToolUse:       hookEntries(sess.cfg.Hooks.PreToolUse),
			SessionStart:     hookEntries(sess.cfg.Hooks.SessionStart),
			UserPromptSubmit: hookEntries(sess.cfg.Hooks.UserPromptSubmit),
			SessionEnd:       hookEntries(sess.cfg.Hooks.SessionEnd),
		}, hookNotify)
	}
	// What a context hook learns about the session (ADR-0069 §2): the
	// transcript path only while a log is actually being written.
	sess.hookSession = hooks.Session{ID: sess.sessionID, CWD: sess.projectDir}
	if sess.sessionID != "" {
		sess.hookSession.TranscriptPath = sess.sessionPath
	}
	if sess.hookRunner != nil && len(sess.cfg.Hooks.PreToolUse) > 0 {
		preToolHook = func(ctx context.Context, name string, args map[string]any) (bool, string) {
			return sess.hookRunner.Pre(ctx, sess.hookSession, name, args)
		}
	}
	if sess.hookRunner != nil && sess.hookRunner.HasPromptSubmit() {
		promptHook = func(ctx context.Context, input string) (string, bool, string) {
			return sess.hookRunner.PromptSubmit(ctx, sess.hookSession, input)
		}
	}
	return preToolHook, promptHook
}

// assembleAgent builds the agent over everything startup assembled, and
// gives it the history a resume restores.
func assembleAgent(sess *liveSession, rs runSettings, adm admission, op opened, md models, fe frontEnd, rg toolsRegistered, preToolHook func(ctx context.Context, name string, args map[string]any) (bool, string), promptHook agent.PromptHook) {
	// The system prompt is composed in ONE place so a skills reload
	// rebuilds exactly what startup built (ADR-0039). It says nothing
	// about diagrams on any surface (ADR-0063): fence rendering is a
	// view-layer concern the model is never told about, and both a
	// prohibition and a format instruction were measured steering the
	// model away from the behavior its own prior already had.
	// The session's dates are captured once (ADR-0097): every rebuild of
	// the prompt states the same day, and /clear captures a new one.
	sess.dates = sessionDates{Start: time.Now(), ResumedFrom: op.resumedFrom}
	// writes pairs the agent's before/after hooks around an
	// operator-approved write (ADR-0074 §1).
	writes := &writeGuard{}
	// notice puts a one-line warning in front of the operator wherever
	// they are: the TUI's note strip when it runs, stderr otherwise.
	notice := sess.surf.notice
	sess.granter.notice = notice
	sess.ag = agent.New(agent.Options{
		// The operator's language for the notices the agent writes
		// mid-turn (ADR-0029 §3: they are chrome, not error chains).
		Msgs: sess.msgs,
		// Accounting only (ADR-0057): the model name that goes into
		// this session's usage records.
		Model:   sess.cfg.Model.Name,
		Backend: sess.backend,
		// The model tier's slot (ADR-0082); nil rides Backend.
		RiskBackend: md.riskBackend,
		RiskModel:   md.riskModel,
		// Registered is not advertised under on-request (ADR-0083);
		// nil for both keeps today's behaviour.
		Advertise: advertisePredicate(sess.cfg, sess.adv),
		AfterTool: afterToolHook(sess.cfg, sess.adv),
		// A one-shot run has nobody to ask (ADR-0065); its denials name
		// the route instead (ADR-0084 §2).
		Unattended: rs.oneShot,
		Registry:   sess.registry,
		Gate:       fe.gate,
		Log:        sess.sessionLog,
		System:     sess.composeSystem(),
		MaxTurns:   sess.cfg.Agent.MaxTurns,
		Policy:     adm.approvalPolicy,
		// load_skill results are operator-authored instructions, not
		// data; its reads are confined to skill directories (ADR-0010).
		InstructionTools: []string{skills.ToolName},
		Telemetry:        sess.sink,
		ClipboardImage:   clipboardImage,
		MediaUpload:      md.mediaUpload,
		BeforeOperatorWrite: func(tc llm.ToolCall) {
			if name := pinNameForWrite(sess.projectDir, tc); name != "" {
				writes.begin(tc, name, repinnable(sess.policyFile, sess.policyPath, sess.projectDir, name, sess.grant.excluded))
			}
		},
		OnOperatorWrite: func(tc llm.ToolCall) {
			// The operator approved a write into the files later
			// sessions trust: that content — the one file they saw
			// written — is now what they trust (ADR-0074 §1), provided
			// the file was still what its pin records when the write
			// began; a file that had drifted before is left for the
			// next start to ask about. An operator-lane command shows
			// its text, not its effect on those files, so it re-pins
			// nothing; what now differs is named and the next start
			// asks.
			if name := pinNameForWrite(sess.projectDir, tc); name != "" {
				if !writes.end(tc, name) {
					notice(fmt.Sprintf(sess.msgs.PinStaleWriteFmt, name))
					return
				}
				if err := repinName(sess.policyPath, sess.projectDir, name, sess.policyFile, sess.grant.excluded); err != nil {
					notice(fmt.Sprintf("could not refresh the trust pin of %s: %v", name, err))
				}
				return
			}
			if n := pinChangesNote(sess.cfg, sess.policyFile, sess.policyPath, sess.projectDir, sess.projectTrusted, sess.msgs); n != "" {
				notice(n)
			}
		},
		OnToolCall: func(tc llm.ToolCall) {
			// Describe, not CallDetail+CallPurpose: only the agent knows
			// which tools it added the purpose field to, and a tool that
			// publishes an argument of that name must keep it visible
			// among the arguments (ADR-0047 §2).
			detail, purpose := sess.ag.Describe(tc)
			sess.surf.toolCall(tc.Name, detail, purpose)
		},
		// The tool-finished signal (review round 3): the TUI leaves its
		// tool state on this, never on stream chunks — a risk or
		// progress review's side-stream used to look like "the tool
		// returned".
		OnToolDone: func(tc llm.ToolCall) { sess.surf.toolDone(tc.Name) },
		OnAttach: func(atts []mention.Attachment, problems []mention.Problem) {
			lines := make([]string, 0, len(atts))
			for _, a := range atts {
				lines = append(lines, fmt.Sprintf("attached %s: %s (%d bytes)", a.Kind, a.Ref, a.Bytes))
			}
			notes := make([]string, 0, len(problems))
			for _, p := range problems {
				notes = append(notes, fmt.Sprintf("@%s: %s", p.Ref, p.Reason))
			}
			sess.surf.attached(lines, notes)
		},
		OnNotice:    notice,
		PreToolHook: preToolHook,
		PromptHook:  promptHook,
		OnUsage: func(u llm.Usage) {
			sess.sink.Usage(u.Prompt, u.Output, u.Thoughts, u.Cached, u.ToolPrompt, u.Total)
			sess.surf.usage(u)
		},
		AutoCompact:  sess.cfg.Agent.AutoCompact,
		CompactAtPct: sess.cfg.Agent.CompactAtPct,
		// The round limit is an intervention ladder on the main loop
		// (ADR-0040); the file-search child keeps its plain hard bound.
		RoundReview:  true,
		OnRoundLimit: rg.onRoundLimit,
		AutoApprove:  rs.autoOn,
		Ceiling:      rs.ceiling,
		OnAutoDecision: func(tc llm.ToolCall, d agent.AutoDecision) {
			if !d.Approved {
				return // the escalation shows up in the approval prompt
			}
			sess.surf.autoApproved(tc.Name, d.Reason, d.Tier.String())
		},
	})
	if len(op.restored) > 0 {
		sess.ag.SetHistory(op.restored)
		// The resume fact where the work resumes, not only in the system
		// prompt (ADR-0097 §4): against a conversation full of its own
		// dates, the model took the conversation's date every time the
		// fact sat only in the system prompt.
		if note := resumeNote(sess.dates); note != "" {
			sess.ag.AddContext(note)
		}
	}
}

func assembleSession(sess *liveSession, cmd *cobra.Command, cl *cleanups, rs runSettings, adm admission, op opened, ld loaded, md models, fe frontEnd, rg toolsRegistered) assembled {
	preToolHook, promptHook := assembleHooks(sess)
	assembleAgent(sess, rs, adm, op, md, fe, rg, preToolHook, promptHook)

	// --- risk rulebook (ADR-0050): read both layers into the judge.
	// A standing influence is never silent (the ADR-0049 lesson):
	// `/riskbook` shows which layers are in force. ADR-0078 took the
	// banner line out — a line that names the command superseding it has
	// conceded the point — so the invariant now rests on the command. ---
	rbBook, rbErr := riskbook.Load(rs.cfgPath, sess.projectDir)
	if rbErr != nil {
		fmt.Fprintf(sess.stderr, "warning: risk rules: %v\n", rbErr)
	} else {
		sess.ag.SetRulebook(rbBook.Compose())
	}

	// The /riskbook runner (ADR-0050 §6). apply is the one path every
	// mutation ends on: disk → compose → judge, so they cannot drift.
	rbLangName := "English"
	if rs.uiLang == uitext.JA {
		rbLangName = "Japanese"
	}
	rbRunner := &riskbookRunner{
		cfgPath:     rs.cfgPath,
		sessionsDir: sess.sessionDir,
		projectDir:  sess.projectDir,
		backend:     md.summaryBackend,
		modelName:   md.summaryModel,
		langName:    rbLangName,
		msgs:        sess.msgs,
		apply:       func() (riskbook.Book, error) { return applyRulebook(rs.cfgPath, sess.projectDir, sess.ag) },
		ask: func(askCtx context.Context, question, accept, discard string) (bool, error) {
			idx, err := rg.askOperator(askCtx, question, []string{accept, discard})
			return err == nil && idx == 0, err
		},
		emit:   sess.surf.output,
		record: func(kind string, data any) { _ = sess.log(kind, data) },
		tally:  md.tally,
		now:    time.Now,
	}

	// showImage is the one door to the operator's screen (ADR-0091). Both
	// routes use it — show_image, which the model calls, and /show, which
	// the operator types — so a refusal reads the same either way, and
	// the reason is returned rather than printed: the screen stays silent
	// on refusal (ADR-0089), and the caller is the one who can say
	// something useful about it.
	showImage := func(data []byte, mime string) error {
		if fe.images == termimg.None {
			return fmt.Errorf("this terminal cannot draw inline images (see [tui].images and /settings)")
		}
		if _, _, ok := termimg.Measure(data); !ok {
			return fmt.Errorf("not a drawable image, or larger than %d bytes decoded", termimg.MaxBytes)
		}
		if !ld.screen.Image(data, mime) {
			return fmt.Errorf("this session has no screen (one-shot or non-interactive)")
		}
		return nil
	}
	sess.registry.SetShowImage(showImage)

	// showPath is /show: the OPERATOR names the file, so it is admitted
	// under the rules of an `@<image>` attachment — a project path, or an
	// absolute or ~ path for an image (ADR-0012) — rather than under the
	// tools' project confinement. The rules, not the grammar: see newShowPath.
	showPath := newShowPath(sess.projectDir, showImage)

	sess.settings = &settingsStore{
		cfg: sess.cfg, projectCfg: sess.projectCfg, mayLoosen: adm.projectMayLoosen, policyFile: sess.policyFile,
		policyPath: sess.policyPath, projectDir: sess.projectDir,
		registry: sess.registry, ag: sess.ag, current: adm.approvalPolicy,
		filter: sess.mcpFilter, inv: sess.mcpInv, images: fe.images,
		thinking: sess.backend,
	}
	settingsData := sess.settings.data()
	// /thinking turns the same dial the panel's model.thinking row does
	// (ADR-0098 §2).
	thinkingSlash := func(args []string) (string, bool) {
		return thinkingCmd(args, sess.backend, sess.cfg, sess.msgs)
	}

	reloadMCP := func() string { return sess.reconnectMCP(true) }
	sess.settings.reloadMCP = sess.reconnectServer

	// One value for both surfaces: the TUI and the plain REPL each built
	// this literal, so a closure added to one could be missed in the
	// other and its command read as unknown there (pre-release review).
	addDirSlash := func(rest string, write bool) (string, bool) {
		note, err := sess.granter.grant(context.Background(), rest, write, "slash", sess.granter.currentEpoch(), nil)
		if err != nil {
			return err.Error() + "\n", true
		}
		return note + "\n", false
	}
	reloads := slashReloads{mcp: reloadMCP, skills: sess.reloadSkills, mcpLoad: sess.loadMCP, mcpStatus: sess.adv.Status, mcpWithheld: sess.adv.Withheld, show: showPath, thinking: thinkingSlash, addDir: addDirSlash, dirs: sess.granter.listing}

	if op.resumedID != "" {
		sess.sessionStartHooks("resume")
	} else {
		sess.sessionStartHooks("startup")
	}
	// Exit summary (operator request): every interactive exit route —
	// /quit, Ctrl+C, Ctrl+D — ends with the resume hint and the cost
	// line as the last thing in the scrollback. A final cleanup: after
	// everything else, the audit flush and its notice (ADR-0065 §4)
	// included. One-shot mode stays clean for pipelines.
	if !rs.oneShot {
		out := cmd.ErrOrStderr()
		cl.pushFinal("print the exit summary", func() {
			for _, l := range exitSummary(sess.ag.Usage(), sess.sessionID, sess.msgs) {
				fmt.Fprintln(out, l)
			}
		})
	}
	cl.push("run the session-end hooks", func() { sess.sessionEndHooks("exit") })
	cl.push("report the project's persistent files", func() {
		sess.reportPersistent("exit", sess.stderr, sess.log)
	})
	// Cleanups run last-first: at exit the roots are reported before the
	// project and before the session-end hooks, so a change a hook makes
	// shows at the next start — as the project's own report already
	// does. /clear runs the hooks first.
	cl.push("report the added roots' persistent files", func() {
		sess.watch.report("exit", sess.stderr, sess.log)
	})

	return assembled{rbRunner: rbRunner, reloads: reloads, settingsData: settingsData}
}

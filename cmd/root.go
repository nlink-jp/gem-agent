package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/approve"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/hooks"
	"github.com/nlink-jp/gem-agent/internal/llm"
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
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
	"github.com/nlink-jp/gem-agent/internal/workdir"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	flagConfig    string
	flagModel     string
	flagThinking  string
	flagMCP       string
	flagNoSandbox bool
	flagPrompt    string
	flagContinue  bool
	flagResume    string
	flagAuto      bool
	flagAllow     []string
)

// appVersion mirrors rootCmd.Version for use inside rootCmd's own run
// closures — reading rootCmd there is an initialization cycle.
var appVersion = "dev"

// Execute runs the root command.
func Execute(version string) {
	appVersion = version
	rootCmd.Version = version
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "gem-agent [first message]",
	Short: "Minimal, sandboxed CLI agent runtime backed by Vertex AI Gemini",
	Long: `gem-agent is an independent, deliberately minimal CLI agent runtime
backed by Vertex AI Gemini 3.x: an auditable loop of file read/write,
sandboxed command execution, and MCP server connectivity, with mutating
calls gated behind per-call approval. It is drop-in compatible with the
agent ecosystem's conventions — AGENTS.md / CLAUDE.md instruction
files, Claude Code-format .mcp.json and skills — so a project needs no
gem-agent-specific setup.

The current working directory is the project: file access is confined to
it, and shell file-writes are restricted to it by macOS sandbox-exec.

macOS only. See docs/ for the RFP and design records.`,
	// One optional positional argument: the first interactive turn
	// (ADR-0064). It runs through the same path a typed message takes,
	// then the session is ordinary interactive gem-agent.
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runREPL,
}

func init() {
	rootCmd.Flags().StringVar(&flagConfig, "config", "", "config file path (default ~/.config/gem-agent/config.toml)")
	rootCmd.Flags().StringVar(&flagModel, "model", "", "override the configured model name")
	rootCmd.Flags().StringVar(&flagThinking, "thinking", "", "override [model].thinking for this run: minimal|low|medium|high, or 'default' to clear a configured level (supported levels are model-dependent — ADR-0025)")
	rootCmd.Flags().StringVar(&flagMCP, "mcp", "", "override [mcp].enabled for this run: on|off (off skips every MCP server spawn — useful for -p pipelines; ADR-0039)")
	rootCmd.Flags().BoolVar(&flagNoSandbox, "no-sandbox", false, "disable the sandbox-exec wrapper for shell_exec (debugging only, unsafe)")
	rootCmd.Flags().StringVarP(&flagPrompt, "prompt", "p", "", "one-shot mode: run this prompt and exit (mutating tools are denied unless granted with --allow or armed with --auto)")
	rootCmd.Flags().BoolVar(&flagAuto, "auto", false, "start in auto-approve mode (ADR-0004); the only way to arm it in one-shot -p, where [agent].auto_approve is ignored (ADR-0053)")
	rootCmd.Flags().StringSliceVar(&flagAllow, "allow", nil, `per-run approval grants: tool names or mcp__server__* prefixes that never ask this run (repeatable or comma-separated; the Block floor still applies — ADR-0053)`)
	rootCmd.Flags().BoolVarP(&flagContinue, "continue", "c", false, "resume this project's most recent session")
	rootCmd.Flags().StringVar(&flagResume, "resume", "", "resume a specific session id (see `gem-agent sessions`)")
}

const shell = "/bin/bash"

func runREPL(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	// Startup warnings are teed: they hit stderr immediately (plain
	// REPL, one-shot, early failures), but the TUI's first ClearScreen
	// wipes that copy — a broken skill or unreadable memory flashed for
	// milliseconds and vanished (ADR-0021) — so the recorded lines ride
	// the banner too.
	notes := &startupNotes{w: cmd.ErrOrStderr()}
	var stderr io.Writer = notes

	// --- config ---
	cfgPath := flagConfig
	if cfgPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		cfgPath = p
	}
	cfg, err := config.LoadWithOverrides(cfgPath, config.Overrides{Model: flagModel, Thinking: flagThinking, MCP: flagMCP, Auto: flagAuto})
	if err != nil {
		return err
	}
	oneShot := flagPrompt != ""
	// The first interactive turn, from the positional argument
	// (ADR-0064). Never combined with -p: the two select different
	// session shapes, and ambiguity is refused, not resolved.
	initialInput, err := firstMessage(args, oneShot)
	if err != nil {
		return err
	}
	// Everything downstream — the agent, telemetry — reads this one
	// effective value, never the raw config field.
	autoOn := effectiveAuto(cfg.Agent.AutoApprove, oneShot, flagAuto)
	// UI language, resolved once (ADR-0029): the chrome that follows —
	// prompts, TUI, slash output — is built with it.
	uiLang := uitext.Resolve(cfg.TUI.Language, os.Getenv)
	msgs := uitext.For(uiLang)
	// The escape ladder for turns outside the TUI (ADR-0065 §3): the
	// TUI has its own three-press exit; the plain REPL and one-shot
	// mode get the same one here. The quit skips the deferred flushes
	// on purpose — the transcript is per event, and the warning that
	// preceded it says so.
	ladder := &interruptLadder{
		Interrupting: func() { fmt.Fprintln(cmd.ErrOrStderr(), msgs.StatusInterrupting) },
		Warn:         func() { fmt.Fprintln(cmd.ErrOrStderr(), msgs.InterruptStuckWarn) },
		Quit: func() {
			fmt.Fprintln(cmd.ErrOrStderr(), msgs.Bye)
			os.Exit(130)
		},
	}

	// --- project directory ---
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectDir, err := sandbox.ResolveWriteDir(cwd)
	if err != nil {
		return err
	}

	// --- startup safety (ADR-0023) ---
	// Both gates read one unbuffered line from stdin before the REPL/TUI
	// takes over. One-shot mode counts as non-interactive even on a TTY:
	// a scripted -p must behave deterministically.
	interactive := !oneShot && term.IsTerminal(int(os.Stdin.Fd()))
	home, _ := os.UserHomeDir()
	if reason := broadRoot(projectDir, home); reason != "" {
		if err := confirmBroadRoot(reason, projectDir, interactive, os.Stdin, cmd.ErrOrStderr(), msgs); err != nil {
			return err
		}
	}

	// --- per-tool approval policy (ADR-0008) ---
	// The project half may tighten freely and may only loosen where the
	// operator trusted this directory: a checked-out repository must not
	// be able to switch the gate off.
	projectCfg, err := config.LoadProject(projectDir)
	if err != nil {
		return err
	}
	policyPath := config.PolicyPath(cfgPath)
	policyFile, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		return err
	}
	mergedTools := map[string]string{}
	for k, v := range cfg.Approval.Tools {
		mergedTools[k] = v
	}
	// The machine-owned file wins: a change made through /settings must
	// not be silently overridden by the hand-written config (ADR-0009).
	for k, v := range policyFile.ForProject(projectDir) {
		mergedTools[k] = v
	}
	// --allow entries sit above both files (flags > machine-owned >
	// hand-written, the order the config system already declares) and
	// compile into the same policy build — so the project tighten, the
	// Block floor, hooks, and the bare-"*" ban all hold without knowing
	// the flag exists (ADR-0053 §2).
	if err := applyAllowFlag(mergedTools, flagAllow); err != nil {
		return err
	}
	// Learned command rules are parsed but NOT applied (ADR-0049 §3):
	// /learn is withdrawn, nothing displays or manages those entries, and
	// invisible standing permissions are the state the withdrawal exists
	// to end. Ignoring them only ever tightens. The note tells the
	// operator where the entries live.
	approvalPolicy, policyNotes, err := policy.Build(
		mergedTools, projectCfg.Approval.Tools, nil, cfg.TrustsProject(projectDir))
	if err != nil {
		return err
	}
	if n := len(policyFile.CommandsFor(projectDir)); n > 0 {
		fmt.Fprintf(stderr, "note: %d learned command rule(s) in %s are not applied — /learn was withdrawn (ADR-0049); delete the [projects...commands] entries or keep them for a future version\n", n, policyPath)
	}

	// --- first-run project trust (ADR-0023): does this project's own
	// instruction files / .mcp.json / skills get loaded at all? ---
	projectTrusted, trustNote := resolveProjectTrust(
		cfg, policyFile, policyPath, projectDir, interactive, os.Stdin, cmd.ErrOrStderr(), msgs)
	if trustNote != "" {
		fmt.Fprintf(stderr, "%s\n", trustNote)
	}

	// --- session transcript: the log, and the resume source (ADR-0005) ---
	if flagContinue && flagResume != "" {
		return fmt.Errorf("--continue and --resume name different sessions; use one")
	}
	sessionDir, sessionDirErr := session.DefaultDir()

	var restored []llm.Message
	resumedID := ""
	if flagContinue || flagResume != "" {
		if sessionDirErr != nil {
			return fmt.Errorf("cannot resume: %w", sessionDirErr)
		}
		meta, err := resolveResume(sessionDir, projectDir, cfg.Model.Name, flagResume)
		if err != nil {
			return err
		}
		resumedID = meta.ID
		// restored is loaded below, AFTER Reopen holds the flock.
	}

	// A broken log warns; a session that cannot be recorded is still a
	// session, and degrading beats refusing to start. A broken *resume*
	// is fatal, though — the operator asked for that history, and
	// continuing without it silently would be worse than stopping.
	var sessionLog agent.SessionLog
	sessionPath := "(disabled)"
	sessionID := ""
	// curLog is the transcript in use; /clear replaces it (ADR-0071 §2),
	// so the exit-time close reads the variable, not a snapshot.
	var curLog *session.Logger
	defer func() {
		if curLog != nil {
			_ = curLog.Close()
		}
	}()
	if sessionDirErr != nil {
		fmt.Fprintf(stderr, "warning: session log disabled: %v\n", sessionDirErr)
	} else {
		lg, err := openSessionLog(sessionDir, resumedID, projectDir, cfg.Model.Name, cfg.GCP.Location, cmd.Root().Version)
		switch {
		case err != nil && resumedID != "":
			return fmt.Errorf("cannot append to session %s: %w", resumedID, err)
		case err != nil:
			fmt.Fprintf(stderr, "warning: session log disabled: %v\n", err)
		default:
			curLog = lg
			sessionLog = lg
			sessionPath = lg.Path()
			sessionID = lg.ID()
			// Exported before any MCP server starts, so a registration
			// line can hand the session to a server that keeps
			// per-session state (ADR-0069 addendum 2).
			if err := session.Export(sessionID); err != nil {
				fmt.Fprintf(stderr, "warning: cannot export %s: %v\n", session.EnvVar, err)
			}
			if resumedID != "" {
				// Under the flock (Reopen holds it): the file we read
				// is exactly the file we will append to, with no
				// window for another process's tail (review round 2).
				history, resumeNotes, err := loadResumedHistory(lg, resumedID)
				if err != nil {
					return err
				}
				for _, n := range resumeNotes {
					fmt.Fprintf(stderr, "warning: %s\n", n)
				}
				restored = history
			}
		}
	}

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
	if err := os.Setenv(workdir.ProjectEnvVar, projectDir); err != nil {
		fmt.Fprintf(stderr, "warning: cannot export %s: %v\n", workdir.ProjectEnvVar, err)
	}
	workDir := ""
	defer func() {
		if workDir != "" {
			workdir.RemoveIfEmpty(workDir)
		}
	}()
	if sessionID != "" {
		if dir, err := workdir.Ensure(projectDir, sessionID); err != nil {
			// A missing work directory degrades the session (oversized
			// MCP results get truncated with a note); it does not stop
			// the session from starting.
			fmt.Fprintf(stderr, "warning: session work directory unavailable: %v\n", err)
			// An inherited value (a nested launch) must not stand in.
			_ = os.Unsetenv(workdir.EnvVar)
		} else {
			workDir = dir
			// Exported, not passed: this is what puts the path in front
			// of shell_exec's child, every MCP server (internal/mcp
			// inherits os.Environ), and every hook, without any of them
			// needing to know gem-agent's layout.
			if err := exportWorkDir(workDir); err != nil {
				fmt.Fprintf(stderr, "warning: cannot export %s: %v\n", workdir.EnvVar, err)
			}
			if dirs, bytes, err := workdir.Sweep(projectDir, sessionID); err == nil && dirs > 0 {
				verb := "hold"
				if dirs == 1 {
					verb = "holds"
				}
				fmt.Fprintf(stderr, "note: %d earlier session work director%s %s %s here — review with 'gem-agent workdirs' (nothing is deleted automatically)\n",
					dirs, plural(dirs, "y", "ies"), verb, humanBytes(bytes))
			}
		}
	}

	// --- shell execution strategy (ADR-0001 defense-in-depth) ---
	sandboxOn := cfg.Sandbox.Enabled && !flagNoSandbox
	execFn, err := buildExecFn(sandboxOn, projectDir, workDir)
	if err != nil {
		return err
	}
	// The strategy is swapped when /clear rotates the work directory
	// (ADR-0071 §2): the sandbox profile names the directory, so a
	// profile built at startup denied every write to the new one.
	shellExec := &liveExec{fn: execFn}

	registry, err := tools.New(projectDir, shellExec.run, time.Duration(cfg.Agent.ShellTimeoutSec)*time.Second)
	if err != nil {
		return err
	}
	if workDir != "" {
		// The file tools get the work directory as a second root, so a
		// result the intake saved is one read_file can read back. Without
		// it the model sees paths it cannot open and routes around the
		// built-ins with shell redirection, which is less reviewable.
		if err := registry.UseWorkDir(workDir); err != nil {
			fmt.Fprintf(stderr, "warning: work directory not readable by the file tools: %v\n", err)
		}
	}

	// --- MCP servers from the project's .mcp.json (drop-in) ---
	mcpClients, mcpSummary, _ := connectMCPServers(ctx, cfg, projectDir, cmd.Root().Version, registry, stderr, projectTrusted)
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// --- skills: Claude Code's skill library, read as-is (ADR-0010) ---
	// skillsList and mcpSummary/mcpClients above are reassigned by the
	// /skills reload and /mcp reload closures (ADR-0039); every consumer
	// reads the variables through a closure, so updates propagate. The
	// single-writer discipline holds because a slash command cannot run
	// while a turn is in flight.
	skillsList, skillNotes := discoverSkills(projectDir, projectTrusted)
	for _, n := range skillNotes {
		fmt.Fprintf(stderr, "warning: %s\n", n)
	}
	if err := registerSkillTool(registry, func() []skills.Skill { return skillsList }); err != nil {
		return err
	}

	// --- agent memory: facts persisted across sessions (ADR-0020) ---
	// A missing home disables memory with a warning; the runtime must
	// not refuse to start over its least essential feature.
	memBase := ""
	var memories []memory.Memory
	if base, err := memory.DefaultDir(); err == nil {
		memBase = base
		var memNotes []string
		memories, memNotes = memory.Load(memBase, projectDir, memory.DefaultLimits())
		for _, n := range memNotes {
			fmt.Fprintf(stderr, "warning: %s\n", n)
		}
		if err := registerMemoryTools(registry, memBase, projectDir); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stderr, "warning: memory disabled: %v\n", err)
	}
	memorySection := ""
	if memBase != "" {
		memorySection = memory.PromptSection(memories)
	}

	// --- telemetry: audit events to the operator's collector (ADR-0035) ---
	// Default off. Failures never hurt the session: creation errors
	// warn and fall back to the no-op sink.
	sink := telemetry.Nop()
	if cfg.Telemetry.Enabled {
		if s, err := telemetry.New(ctx, telemetry.Config{
			Enabled: true, Backend: cfg.Telemetry.Backend,
			Endpoint: cfg.Telemetry.Endpoint, Insecure: cfg.Telemetry.Insecure,
			HeadersFile: cfg.Telemetry.HeadersFile,
		}, cfg.GCP.Project, cmd.Root().Version, sessionID, projectDir); err != nil {
			fmt.Fprintf(stderr, "warning: telemetry disabled: %v\n", err)
		} else {
			sink = s
			defer func() {
				// The flush is bounded (3s) but was silent, and a
				// silent wait after the last Ctrl+C reads as a hang
				// (ADR-0065 §4). One-shot mode stays quiet for
				// pipelines, like the exit receipt.
				if !oneShot {
					fmt.Fprintln(cmd.ErrOrStderr(), msgs.ExitFlushing)
				}
				sink.Shutdown()
			}()
		}
	}
	// The effective auto state, not the raw config field: one-shot mode
	// disarms auto-approve, and an audit event claiming it was armed in
	// a run where it never could be is a false record (ADR-0053 §4).
	sink.SessionStart(cfg.Model.Name, sandboxOn, autoOn, len(mcpClients))
	defer sink.SessionEnd() // LIFO: runs before Shutdown's flush

	// --- LLM backend ---
	backend, err := llm.NewVertex(ctx, cfg.GCP.Project, cfg.GCP.Location, cfg.Model.Name, cfg.Model.Safety, cfg.Model.Thinking, cfg.TUI.ShowThoughts)
	if err != nil {
		return err
	}

	// Per-category token accounting for /usage (ADR-0019).
	tally := newUsageTally()

	// Media uploads (ADR-0027): with [gcp].bucket set, audio/video
	// attachments route through the operator's bucket as gs:// URIs.
	// The client is created lazily on first use — most sessions attach
	// no media.
	var mediaUpload func(callCtx context.Context, path, mime string) (string, error)
	if cfg.GCP.Bucket != "" {
		// Lazy, but retryable: sync.Once pinned the FIRST construction
		// error (a transient DNS/ADC hiccup) for the whole session
		// (review round 2). Only success is cached. The client rides
		// the session context; each upload rides the TURN context so
		// Ctrl+C reaches it.
		var (
			upMu sync.Mutex
			up   *mediastore.Uploader
		)
		mediaUpload = func(callCtx context.Context, path, mime string) (string, error) {
			upMu.Lock()
			if up == nil {
				u, err := mediastore.New(ctx, cfg.GCP.Project, cfg.GCP.Bucket)
				if err != nil {
					upMu.Unlock()
					return "", err
				}
				up = u
			}
			u := up
			upMu.Unlock()
			uri, err := u.Upload(callCtx, path, mime)
			if err == nil {
				var size int64
				if fi, statErr := os.Stat(path); statErr == nil {
					size = fi.Size()
				}
				sink.MediaUpload(size, uri)
			}
			return uri, err
		}
	}

	// --- summarize_file: the summary model shares the client (ADR-0014) ---
	summaryModel := cfg.Model.Summary
	summaryBackend := backend
	if summaryModel == "" {
		summaryModel = cfg.Model.Name
	} else {
		summaryBackend = backend.WithModel(summaryModel)
	}
	// sideLog forwards to the transcript in use: /clear swaps it
	// (ADR-0071 §2), and a tool that captured the value at startup wrote
	// its usage records to the closed file (review round 4).
	sideLog := liveLog{get: func() agent.SessionLog { return sessionLog }}
	if err := registerSummarizeTool(registry, summaryBackend, summaryModel, sideLog, tally); err != nil {
		return err
	}
	// Web access (ADR-0017): grounded search on the main model, digested
	// fetch on the lightweight one. Both egress-gated by default.
	if err := registerWebTools(registry, backend, summaryBackend, cfg.Model.Name, summaryModel, sideLog, tally); err != nil {
		return err
	}

	// --- project instruction files (drop-in: AGENTS.md and friends,
	// including ancestor directories, exactly as other agents read them)
	projectContext, contextLabels, contextNotes := loadInstructions(projectDir, projectTrusted)
	for _, n := range contextNotes {
		fmt.Fprintf(stderr, "warning: instructions %s\n", n)
	}

	// The TUI needs a real terminal on both ends (ADR-0002); piped use
	// falls back to the plain line REPL so scripts and smoke pipelines
	// keep working.
	useTUI := !oneShot &&
		term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))

	// One shared buffered reader for the plain REPL and its approval
	// gate: bufio.NewReader returns an existing *bufio.Reader unchanged,
	// so both components drain the same buffer and no typed-ahead input
	// is stranded in a second one. (The TUI reads the terminal itself.)
	stdin := bufio.NewReader(cmd.InOrStdin())
	var gate agent.Approver
	var tuiGate *tui.Gate
	switch {
	case oneShot:
		// One-shot runs non-interactively (stdin may be a pipe): a
		// blocking approval prompt would hang, so mutating tools are
		// denied outright. Read-only pipelines still work.
		gate = denyGate{out: stderr}
	case useTUI:
		tuiGate = tui.NewGate()
		gate = tuiGate
	default:
		gate = approve.New(stdin, stderr)
	}
	reader := repl.NewReader(stdin, stderr)

	// prog is assigned before the TUI runs; the agent only executes
	// inside prog.Run, so the callbacks below never see it half-set.
	var prog *tea.Program
	// Turn observability (ADR-0033): stream heartbeat, retries, and
	// thought summaries reach the TUI when one is running; the plain
	// REPL and one-shot mode stay quiet — their output goes to pipes.
	backend.SetObserver(func(ev llm.StreamEvent) {
		if prog != nil {
			prog.Send(tui.StreamUpdate{Kind: ev.Kind, Thought: ev.Thought,
				Attempt: ev.Attempt, Max: ev.Max, Cause: ev.Cause, DelayMS: ev.DelayMS})
		}
	})

	// --- ask_user: a structured mid-turn choice (ADR-0036) ---
	// Registered before agent.New (declarations are cached there). The
	// asker picks its mode at call time: one-shot has nobody to ask,
	// the TUI shows a dialog, the plain REPL reads a number. The same
	// asker carries the round-limit dialog (ADR-0040).
	// The plain-REPL asker reads the SHARED stdin reader: a second
	// bufio.Reader over the same fd strands typed-ahead input in one
	// buffer while the other blocks (AGENTS.md gotcha; review round 3).
	askOperator := func(askCtx context.Context, question string, options []string) (int, error) {
		if flagPrompt != "" {
			return oneShotAsk(askCtx, question, options)
		}
		if prog != nil {
			resp := make(chan int, 1)
			prog.Send(tui.AskRequest{Question: question, Options: options, Resp: resp})
			idx := <-resp
			if idx < 0 {
				return 0, errAskDeclined
			}
			return idx, nil
		}
		return plainAsk(stdin, cmd.ErrOrStderr())(askCtx, question, options)
	}
	if err := registerAskTool(registry, askOperator); err != nil {
		return err
	}

	// --- round-limit intervention dialog (ADR-0040) ---
	// nil in one-shot mode: nobody to ask, so the progress review alone
	// decides there (fail-closed inside the agent).
	var onRoundLimit func(ctx context.Context, info agent.RoundLimitInfo) bool
	if !oneShot {
		onRoundLimit = func(rctx context.Context, info agent.RoundLimitInfo) bool {
			verdict := ""
			switch {
			case info.ReviewErr != "":
				verdict = fmt.Sprintf(msgs.RoundVerdictErrFmt, clipRunes(info.ReviewErr, 80))
			case info.Progressing:
				verdict = fmt.Sprintf(msgs.RoundVerdictProgressFmt, clipRunes(info.Reason, 100))
			default:
				verdict = fmt.Sprintf(msgs.RoundVerdictStuckFmt, clipRunes(info.Reason, 100))
			}
			var q string
			if info.Trigger == "loop" {
				q = fmt.Sprintf(msgs.RoundLoopAskFmt, clipRunes(info.Detail, 80), verdict)
			} else {
				q = fmt.Sprintf(msgs.RoundLimitAskFmt, info.Rounds, info.Cap, verdict)
			}
			idx, err := askOperator(rctx, q, []string{msgs.RoundContinue, msgs.RoundStop})
			return err == nil && idx == 0
		}
	}

	// --- agent_info: the model's view of its own runtime (ADR-0030) ---
	// Registered before agent.New (which caches the declarations); the
	// snapshot closure reads `ag` lazily — the tool can only run inside
	// Operator hooks (ADR-0044 / ADR-0069): global config only. Pre-tool
	// hooks are evaluated ahead of the approval ladder in every mode —
	// the guards exist for the model's calls regardless of surface;
	// context hooks run at session start and on every prompt. nil when
	// none are configured, so the common case pays nothing.
	// uiNotes is non-nil while a slash command runs the hooks (/clear,
	// ADR-0071): the slash handler executes inside the TUI's Update, and
	// Program.Send from there never returns — Bubble Tea's message
	// channel is unbuffered and Update is its only consumer (review
	// round 4: /clear with a session_start hook froze the TUI). Notes
	// raised there ride back in the slash output instead.
	var uiNotes *[]string
	hookNotify := func(warn string) {
		if uiNotes != nil {
			*uiNotes = append(*uiNotes, warn)
			return
		}
		if prog != nil {
			prog.Send(tui.Attached{Notes: []string{warn}})
			return
		}
		fmt.Fprintf(stderr, "[⚠ %s]\n", warn)
	}
	var hookRunner *hooks.Runner
	if len(cfg.Hooks.PreToolUse)+len(cfg.Hooks.SessionStart)+len(cfg.Hooks.UserPromptSubmit)+len(cfg.Hooks.SessionEnd) > 0 {
		hookRunner = hooks.New(hooks.Hooks{
			PreToolUse:       hookEntries(cfg.Hooks.PreToolUse),
			SessionStart:     hookEntries(cfg.Hooks.SessionStart),
			UserPromptSubmit: hookEntries(cfg.Hooks.UserPromptSubmit),
			SessionEnd:       hookEntries(cfg.Hooks.SessionEnd),
		}, hookNotify)
	}
	// What a context hook learns about the session (ADR-0069 §2): the
	// transcript path only while a log is actually being written.
	hookSession := hooks.Session{ID: sessionID, CWD: projectDir}
	if sessionID != "" {
		hookSession.TranscriptPath = sessionPath
	}
	var preToolHook func(ctx context.Context, name string, args map[string]any) (bool, string)
	if hookRunner != nil && len(cfg.Hooks.PreToolUse) > 0 {
		preToolHook = func(ctx context.Context, name string, args map[string]any) (bool, string) {
			return hookRunner.Pre(ctx, hookSession, name, args)
		}
	}
	var promptHook agent.PromptHook
	if hookRunner != nil && hookRunner.HasPromptSubmit() {
		promptHook = func(ctx context.Context, input string) (string, bool, string) {
			return hookRunner.PromptSubmit(ctx, hookSession, input)
		}
	}

	// ag.Run, so the pointer is always set by then.
	var ag *agent.Agent
	if err := registerInfoTool(registry, func() infoSnapshot {
		return infoSnapshot{
			Version:        cmd.Root().Version,
			OSVersion:      macOSVersion(),
			Model:          cfg.Model.Name,
			SummaryModel:   summaryModel,
			Thinking:       cfg.Model.Thinking,
			Usage:          ag.Usage(),
			MaxTurns:       cfg.Agent.MaxTurns,
			ShellTimeout:   cfg.Agent.ShellTimeoutSec,
			AutoApprove:    ag.AutoApprove(),
			AutoCompact:    ag.AutoCompact(),
			CompactAtPct:   cfg.Agent.CompactAtPct,
			SandboxOn:      sandboxOn,
			ProjectDir:     projectDir,
			SessionID:      sessionID,
			WorkDir:        workDir,
			MCPServers:     mcpSummary,
			SkillCount:     len(skillsList),
			MemoryOn:       memBase != "",
			MediaBucket:    cfg.GCP.Bucket != "",
			ProjectTrusted: projectTrusted,
		}
	}); err != nil {
		return err
	}

	// --- agentic_file_search: delegated project search (ADR-0037) ---
	// Registered before agent.New (which caches the declarations), and
	// after summarize_file (the child's tool subset includes it). The
	// child explores in its own context; only its report enters this
	// conversation. Its tool calls render as "↳ tool" so the operator
	// watches the delegation happen instead of a silent pause.
	if err := registerAgenticSearch(registry, agenticSearchOptions{
		backend:   backend,
		modelName: cfg.Model.Name,
		log:       sideLog,
		tally:     tally,
		sink:      sink,
		onToolCall: func(tc llm.ToolCall) {
			if prog != nil {
				prog.Send(tui.ToolCall{Name: "↳ " + tc.Name, Detail: agent.CallDetail(tc)})
				return
			}
			fmt.Fprintf(stderr, "\n[tool ↳] %s %s\n", tc.Name, agent.CallDetail(tc))
		},
		onToolDone: func(tc llm.ToolCall) {
			if prog != nil {
				prog.Send(tui.ToolDone{Name: "↳ " + tc.Name})
			}
		},
	}); err != nil {
		return err
	}

	// The system prompt is composed in ONE place so a skills reload
	// rebuilds exactly what startup built (ADR-0039). It says nothing
	// about diagrams on any surface (ADR-0063): fence rendering is a
	// view-layer concern the model is never told about, and both a
	// prohibition and a format instruction were measured steering the
	// model away from the behavior its own prior already had.
	composeSystem := func() string {
		return buildSystemPrompt(projectDir, workDir, projectContext) + skills.PromptSection(skillsList) + memorySection
	}
	ag = agent.New(agent.Options{
		// Accounting only (ADR-0057): the model name that goes into
		// this session's usage records.
		Model:    cfg.Model.Name,
		Backend:  backend,
		Registry: registry,
		Gate:     gate,
		Log:      sessionLog,
		System:   composeSystem(),
		MaxTurns: cfg.Agent.MaxTurns,
		Policy:   approvalPolicy,
		// load_skill results are operator-authored instructions, not
		// data; its reads are confined to skill directories (ADR-0010).
		InstructionTools: []string{skills.ToolName},
		Telemetry:        sink,
		ClipboardImage:   clipboardImage,
		MediaUpload:      mediaUpload,
		OnToolCall: func(tc llm.ToolCall) {
			// Describe, not CallDetail+CallPurpose: only the agent knows
			// which tools it added the purpose field to, and a tool that
			// publishes an argument of that name must keep it visible
			// among the arguments (ADR-0047 §2).
			detail, purpose := ag.Describe(tc)
			if prog != nil {
				prog.Send(tui.ToolCall{Name: tc.Name, Detail: detail, Purpose: purpose})
				return
			}
			fmt.Fprintf(stderr, "\n[tool] %s %s\n", tc.Name, detail)
			// Headless runs (-p, piped stdin) get the declared purpose
			// on its own line too (ADR-0047 §5): the transcript of a
			// scripted run is the only record it leaves behind.
			if purpose != "" {
				fmt.Fprintf(stderr, "[tool] ↪ %s\n", purpose)
			}
		},
		// The tool-finished signal (review round 3): the TUI's stall
		// detector re-arms on this, never on stream chunks — a risk or
		// progress review's side-stream used to look like "the tool
		// returned" and produced false stall warnings.
		OnToolDone: func(tc llm.ToolCall) {
			if prog != nil {
				prog.Send(tui.ToolDone{Name: tc.Name})
			}
		},
		OnAttach: func(atts []mention.Attachment, problems []mention.Problem) {
			lines := make([]string, 0, len(atts))
			for _, a := range atts {
				lines = append(lines, fmt.Sprintf("attached %s: %s (%d bytes)", a.Kind, a.Ref, a.Bytes))
			}
			notes := make([]string, 0, len(problems))
			for _, p := range problems {
				notes = append(notes, fmt.Sprintf("@%s: %s", p.Ref, p.Reason))
			}
			if prog != nil {
				prog.Send(tui.Attached{Lines: lines, Notes: notes})
				return
			}
			for _, l := range lines {
				fmt.Fprintln(stderr, "[📎 "+l+"]")
			}
			for _, n := range notes {
				fmt.Fprintln(stderr, "[⚠ "+n+"]")
			}
		},
		OnNotice: func(msg string) {
			if prog != nil {
				prog.Send(tui.Attached{Notes: []string{msg}})
				return
			}
			fmt.Fprintf(stderr, "[⚠ %s]\n", msg)
		},
		PreToolHook: preToolHook,
		PromptHook:  promptHook,
		OnUsage: func(u llm.Usage) {
			sink.Usage(u.Prompt, u.Output, u.Thoughts, u.Cached, u.ToolPrompt, u.Total)
			if prog != nil {
				prog.Send(tui.Usage{Prompt: u.Prompt, Output: u.Output, Cached: u.Cached})
			}
		},
		AutoCompact:  cfg.Agent.AutoCompact,
		CompactAtPct: cfg.Agent.CompactAtPct,
		// The round limit is an intervention ladder on the main loop
		// (ADR-0040); the file-search child keeps its plain hard bound.
		RoundReview:  true,
		OnRoundLimit: onRoundLimit,
		AutoApprove:  autoOn,
		OnAutoDecision: func(tc llm.ToolCall, d agent.AutoDecision) {
			if !d.Approved {
				return // the escalation shows up in the approval prompt
			}
			if prog != nil {
				prog.Send(tui.AutoApproved{Tool: tc.Name, Reason: d.Reason, Tier: d.Tier.String()})
				return
			}
			fmt.Fprintf(stderr, "[%s]\n", fmt.Sprintf(strings.TrimSpace(msgs.AutoApprovedFmt), d.Tier, tc.Name+": "+d.Reason))
		},
	})
	if len(restored) > 0 {
		ag.SetHistory(restored)
	}

	// --- risk rulebook (ADR-0050): read both layers into the judge.
	// A standing influence is never silent (the ADR-0049 lesson): the
	// banner line below announces it while in force. ---
	rbBook, rbErr := riskbook.Load(cfgPath, projectDir)
	if rbErr != nil {
		fmt.Fprintf(stderr, "warning: risk rulebook: %v\n", rbErr)
	} else {
		ag.SetRulebook(rbBook.Compose())
	}

	// The /riskbook runner (ADR-0050 §6). apply is the one path every
	// mutation ends on: disk → compose → judge, so they cannot drift.
	rbLangName := "English"
	if uiLang == uitext.JA {
		rbLangName = "Japanese"
	}
	rbRunner := &riskbookRunner{
		cfgPath:     cfgPath,
		sessionsDir: sessionDir,
		projectDir:  projectDir,
		backend:     summaryBackend,
		modelName:   summaryModel,
		langName:    rbLangName,
		msgs:        msgs,
		apply:       func() (riskbook.Book, error) { return applyRulebook(cfgPath, projectDir, ag) },
		ask: func(askCtx context.Context, question, accept, discard string) (bool, error) {
			idx, err := askOperator(askCtx, question, []string{accept, discard})
			return err == nil && idx == 0, err
		},
		emit: func(lines []string) {
			if prog != nil {
				prog.Send(tui.Output{Lines: lines})
				return
			}
			for _, l := range lines {
				fmt.Fprintln(stderr, l)
			}
		},
		record: func(kind string, data any) {
			if sessionLog != nil {
				_ = sessionLog.Log(kind, data)
			}
		},
		tally: tally,
		now:   time.Now,
	}

	// Exit summary (operator request): every interactive exit route —
	// /quit, Ctrl+C, Ctrl+D — ends with the resume hint and the cost
	// line as the last thing in the scrollback. Deferred so no route
	// can forget it; one-shot mode stays clean for pipelines.
	if !oneShot {
		out := cmd.ErrOrStderr()
		defer func() {
			for _, l := range exitSummary(ag.Usage(), sessionID, msgs) {
				fmt.Fprintln(out, l)
			}
		}()
	}

	settings := &settingsStore{
		cfg: cfg, projectCfg: projectCfg, policyFile: policyFile,
		policyPath: policyPath, projectDir: projectDir,
		registry: registry, ag: ag, current: approvalPolicy,
	}
	settingsData := settings.data()

	// --- in-session integration reload (ADR-0039) ---
	// Both closures reuse the startup code paths and the startup trust
	// verdict — a reload can never widen what the trust gate allowed.
	// They run only between turns (slash commands cannot be queued), so
	// reassigning the captured variables is single-writer safe.
	reloadMCP := func() string {
		if !cfg.MCP.Enabled {
			return msgs.MCPDisabled
		}
		registry.RemoveByPrefix("mcp__")
		for _, c := range mcpClients {
			c.Close()
		}
		// Warnings go into the command output: under the TUI a stderr
		// write would corrupt the display.
		var warn bytes.Buffer
		mcpClients, mcpSummary, _ = connectMCPServers(ctx, cfg, projectDir, cmd.Root().Version, registry, &warn, projectTrusted)
		ag.RefreshTools()
		mcpTools := 0
		for _, t := range registry.List() {
			if strings.HasPrefix(t.Name, "mcp__") {
				mcpTools++
			}
		}
		sink.Reload("mcp", len(mcpClients), mcpTools)
		if sessionLog != nil {
			_ = sessionLog.Log("mcp_reload", map[string]any{
				"servers": len(mcpClients), "tools": mcpTools})
		}
		var b strings.Builder
		fmt.Fprintf(&b, msgs.MCPReloadedFmt, len(mcpClients), mcpTools)
		for _, s := range mcpSummary {
			b.WriteString("  " + s + "\n")
		}
		if warn.Len() > 0 {
			b.WriteString(warn.String())
		}
		return b.String()
	}
	reloadSkills := func() string {
		list, notes := discoverSkills(projectDir, projectTrusted)
		skillsList = list
		// The skill descriptions ride the system prompt; rebuild it so
		// the model sees the new set (the implicit-cache prefix re-warms
		// — the deliberate cost of a reload).
		ag.SetSystem(composeSystem())
		sink.Reload("skills", 0, len(skillsList))
		if sessionLog != nil {
			_ = sessionLog.Log("skills_reload", map[string]any{"count": len(skillsList)})
		}
		var b strings.Builder
		fmt.Fprintf(&b, msgs.SkillsReloadedFmt, len(skillsList))
		for _, n := range notes {
			b.WriteString("  ⚠ " + n + "\n")
		}
		b.WriteString(skillsListing(skillsList))
		return b.String()
	}

	// resolveWindow settles the model's input token limit and hands it to
	// everyone who needs it: the footer displays it, and auto-compaction
	// (ADR-0006) measures against it. It runs in the background — a
	// metadata lookup must never delay the first prompt — and it runs in
	// every mode, one-shot included: a long one-shot tool loop fills the
	// window exactly like an interactive one, and with no window known
	// compaction silently never fires (measured).
	resolveWindow := func() {
		tokens, assumed := cfg.Model.ContextWindow, false
		if tokens <= 0 {
			mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if w, err := backend.ContextWindow(mctx); err == nil && w > 0 {
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
		ag.SetContextWindow(tokens)
		if prog != nil {
			prog.Send(tui.ContextWindow{Tokens: tokens, Assumed: assumed})
		}
	}

	// --- session-start hooks (ADR-0069): startup / resume, and /clear ---
	// The output rides the next turn's user message as a data
	// attachment (ADR-0055's lane): never the system prompt, so the
	// cached request prefix (ADR-0018) is untouched, and never the
	// typed input, which the risk evaluator trusts (ADR-0038/0054).
	sessionHooks := func(source string) {
		if hookRunner == nil || !hookRunner.HasSessionStart() {
			return
		}
		out := hookRunner.SessionStart(ctx, hookSession, source)
		if out == "" {
			return
		}
		ag.AttachData("session_start", agent.HookAttachmentKind, out)
		hookNotify(fmt.Sprintf("session_start hook (%s) attached %d bytes of context as data for the next turn", source, len(out)))
	}
	if resumedID != "" {
		sessionHooks("resume")
	} else {
		sessionHooks("startup")
	}
	// Session-end hooks (ADR-0071 §4) run when the session ends —
	// whatever ended it — and when /clear closes the old session.
	sessionEndHooks := func(reason string) {
		if hookRunner == nil || !hookRunner.HasSessionEnd() {
			return
		}
		hookRunner.SessionEnd(ctx, hookSession, reason)
	}
	defer sessionEndHooks("exit")

	// /clear starts a new session (ADR-0071 §2): the old transcript is
	// closed where the conversation ended and stays resumable by its
	// id; a new id, transcript and work directory take over, exported
	// to children like the first; the hooks see an end and a start —
	// the same sequence Claude Code produces. If a new transcript
	// cannot be opened the conversation is cleared in place, as before,
	// and the operator is told.
	onClear := func() string {
		var notes []string
		uiNotes = &notes
		defer func() { uiNotes = nil }()
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
		if sessionDirErr != nil {
			ag.Reset()
			note("cleared in place — could not start a new session: %v", sessionDirErr)
			sessionHooks("clear")
			return render()
		}
		newLog, err := openSessionLog(sessionDir, "", projectDir, cfg.Model.Name, cfg.GCP.Location, cmd.Root().Version)
		if err != nil {
			ag.Reset()
			note("cleared in place — could not start a new session: %v", err)
			sessionHooks("clear")
			return render()
		}
		// Hook first, then the audit event — the order ADR-0071 §4a
		// states and external consumers observe; the trail closes under
		// the old id before the sink is re-resourced for the new one.
		sessionEndHooks("clear")
		sink.SessionEnd()
		ag.Restart(newLog)
		if curLog != nil {
			_ = curLog.Close()
		}
		curLog = newLog
		sessionLog = newLog
		sessionPath, sessionID = newLog.Path(), newLog.ID()
		if err := session.Export(sessionID); err != nil {
			note("cannot export %s: %v", session.EnvVar, err)
		}
		hookSession = hooks.Session{ID: sessionID, TranscriptPath: sessionPath, CWD: projectDir}
		if workDir != "" {
			workdir.RemoveIfEmpty(workDir)
		}
		workDir = ""
		if dir, err := workdir.Ensure(projectDir, sessionID); err != nil {
			note("session work directory unavailable: %v", err)
		} else {
			workDir = dir
		}
		// Exported or UNSET: the MCP servers reconnected below inherit
		// the environment, and the old directory must not survive in
		// it (review after v0.68.0).
		if err := exportWorkDir(workDir); err != nil {
			note("cannot export %s: %v", workdir.EnvVar, err)
		}
		// Every consumer of the work directory follows it (review round
		// 4): the file tools' second root, the sandbox profile, the MCP
		// intake (it reads the registry), and the system prompt — a
		// cleared conversation has no cached prefix to protect.
		notes = append(notes, rotateWorkDir(registry, shellExec, sandboxOn, projectDir, workDir, func() { ag.SetSystem(composeSystem()) })...)
		// A cleared session restarts what carries its identity (ADR-0071
		// addendum): telemetry is re-resourced with the new id, and the
		// MCP servers — spawned at startup with the old id in their
		// environment and arguments — are reconnected the way /mcp
		// reload does, so a server keeping per-session state (the
		// board) sees the session the hooks report. Measured: without
		// this the board's child kept --session <old id> across /clear.
		if err := sink.Restart(ctx, sessionID); err != nil {
			note("telemetry keeps the previous session id: %v", err)
		}
		if cfg.MCP.Enabled {
			extra.WriteString(reloadMCP())
		}
		sink.SessionStart(cfg.Model.Name, sandboxOn, ag.AutoApprove(), len(mcpClients))
		sessionHooks("clear")
		return render()
	}

	// --- one-shot mode: single turn, quiet stderr, exit ---
	if oneShot {
		for _, n := range policyNotes {
			fmt.Fprintf(stderr, "warning: %s\n", n)
		}
		go resolveWindow()
		if !sandboxOn {
			fmt.Fprintln(stderr, "sandbox: DISABLED — shell commands run unconfined")
		}
		// Piped stdin becomes a nonce-wrapped data attachment
		// (ADR-0055) — never prompt text: the -p string alone is the
		// instruction the risk evaluator sees (ADR-0038/0054). A
		// terminal stdin is never read, so an interactive `-p` cannot
		// hang waiting for input. A non-terminal stdin that never
		// closes (an idle pipe inherited from a scheduler or harness)
		// is read to EOF like any other, but the wait is announced
		// after a short grace so it cannot pass for a hang (ADR-0067).
		if f, ok := cmd.InOrStdin().(*os.File); !ok || !term.IsTerminal(int(f.Fd())) {
			waited := false
			content, warning := readPipedStdinNoticing(cmd.InOrStdin(), stdinWaitNotice, func() {
				waited = true
				fmt.Fprintln(stderr, stdinWaitMessage)
			})
			if warning != "" {
				fmt.Fprintf(stderr, "warning: %s\n", warning)
			}
			if content != "" {
				ag.AttachData("-", "stdin", content)
			}
			if line := stdinOutcomeLine(content, warning, waited); line != "" {
				fmt.Fprintln(stderr, line)
			}
		}
		wrote := false
		runErr := runTurnWith(ctx, ladder, func(turnCtx context.Context) error {
			_, err := ag.Run(turnCtx, flagPrompt, func(s string) {
				wrote = wrote || s != ""
				fmt.Fprint(cmd.OutOrStdout(), s)
			})
			return err
		})
		// stdout is model text only: a turn that produced none (blocked
		// prompt, interrupt, backend error) leaves it empty rather than
		// a bare newline (review round 4).
		if wrote {
			fmt.Fprintln(cmd.OutOrStdout())
		}
		if errors.Is(runErr, errInterrupted) {
			return fmt.Errorf("interrupted")
		}
		return runErr
	}

	// --- banner ---
	sandboxLine := "sandbox: enabled (shell writes confined to the project directory)"
	if !sandboxOn {
		sandboxLine = "sandbox: DISABLED — shell commands run unconfined"
	}
	bannerLines := []string{
		fmt.Sprintf("gem-agent %s — %s @ %s/%s", cmd.Root().Version, cfg.Model.Name, cfg.GCP.Project, cfg.GCP.Location),
		"project: " + projectDir,
		sandboxLine,
		"session log: " + sessionPath,
	}
	if len(mcpSummary) > 0 {
		bannerLines = append(bannerLines, "mcp: "+strings.Join(mcpSummary, ", "))
	}
	if len(contextLabels) > 0 {
		bannerLines = append(bannerLines, "instructions: "+strings.Join(contextLabels, ", "))
	}
	if line := skillBannerLine(skillsList); line != "" {
		bannerLines = append(bannerLines, line)
	}
	if line := memory.BannerLine(memories); line != "" {
		bannerLines = append(bannerLines, line)
	}
	if resumedID != "" {
		bannerLines = append(bannerLines,
			fmt.Sprintf("resumed: session %s (%d messages restored)", resumedID, len(restored)))
	}
	if approvalPolicy.Configured() {
		bannerLines = append(bannerLines, policyBannerLine(approvalPolicy.Describe()))
	}
	if rbErr == nil && rbBook.InForce() {
		bannerLines = append(bannerLines, riskbookBannerLine(rbBook))
	}
	for _, n := range policyNotes {
		bannerLines = append(bannerLines, "warning: "+string(n))
	}

	// --- interactive TUI (ADR-0002/0003) ---
	if useTUI {
		// The banner goes through the TUI (not stderr): bottom pinning
		// counts every printed line, and the startup clear would wipe a
		// pre-printed banner anyway. Startup warnings join it for the
		// same reason (ADR-0021).
		bannerLines = append(bannerLines, notes.lines...)
		// Nothing reads the tee after the banner; without this the
		// stderr stream accumulated every line for the whole session
		// (review round 3).
		notes.freeze()
		model := tui.New(tui.Options{
			BaseCtx: ctx,
			// Msgs is the wiring ADR-0029 shipped without: the catalog
			// was resolved here but never handed to the TUI, so the
			// whole chrome fell back to English (review round 2).
			Msgs:         msgs,
			Theme:        resolveTheme(cfg.TUI.Theme),
			ModelName:    cfg.Model.Name,
			ProjectDir:   abbreviateHome(projectDir),
			Banner:       bannerLines,
			InitialInput: initialInput,
			AutoMode:     ag.AutoApprove(),
			ToggleAuto: func() bool {
				ag.SetAutoApprove(!ag.AutoApprove())
				return ag.AutoApprove()
			},
			CompletePath: func(prefix string) []string {
				return mention.Complete(projectDir, prefix, 24)
			},
			CompleteSlash: slashCompletions(func() []skills.Skill { return skillsList }),
			Settings:      &settingsData,
			ApplySetting:  settings.Apply,
			ExpandInput: func(in string) (string, bool, string) {
				return expandSkillInput(in, skillsList)
			},
			Shell: func(shellCtx context.Context, command string) {
				go func() {
					out := runDirectShell(shellCtx, registry, ag, command)
					// Interrupted runs hand a queued message back instead
					// of auto-sending it (ADR-0007 via ADR-0021).
					prog.Send(tui.ShellDone{Output: out, Interrupted: shellCtx.Err() != nil})
				}()
			},
			Compact: func(compactCtx context.Context) {
				go func() {
					note, err := compactNow(compactCtx, ag, msgs, sink)
					if err == nil {
						prog.Send(tui.Attached{Notes: []string{note}})
					}
					if err != nil && compactCtx.Err() != nil {
						// Mirror StartTurn's mapping: a cancellation-caused
						// failure is an interrupt, not a backend error.
						err = context.Canceled
					}
					prog.Send(tui.TurnDone{Err: err})
				}()
			},
			StartTurn: func(turnCtx context.Context, input string) {
				go func() {
					_, err := ag.Run(turnCtx, input, func(s string) { prog.Send(tui.TextDelta(s)) })
					if err != nil && turnCtx.Err() != nil {
						// Mirror runTurn's mapping: a cancellation-caused
						// failure is an interrupt, not a backend error.
						err = context.Canceled
					}
					prog.Send(tui.TurnDone{Err: err})
				}()
			},
			Riskbook: func(learnCtx context.Context) {
				go func() {
					rbRunner.Learn(learnCtx)
					prog.Send(tui.TurnDone{})
				}()
			},
			Slash: func(in string) (string, bool, bool) {
				return slashOutput(in, ag, registry, mcpSummary, skillsList,
					slashReloads{mcp: reloadMCP, skills: reloadSkills},
					func() string { return usageReport(ag, tally, cfg.Model.Name, summaryModel) },
					func() string { return memoryListing(memBase, projectDir) },
					rbRunner.Command, appVersion, msgs, onClear)
			},
		})
		prog = tea.NewProgram(model)
		tuiGate.SetProgram(prog)
		go resolveWindow()
		_, err := prog.Run()
		return err
	}
	go resolveWindow()

	// Plain REPL / one-shot: the banner is printed, the tee is done.
	notes.freeze()
	for _, line := range bannerLines {
		fmt.Fprintln(stderr, line)
	}
	fmt.Fprintf(stderr, "/help for commands, Ctrl+D to quit\n")

	// --- plain REPL loop (non-TTY fallback) ---
	// The argv first message (ADR-0064) runs before the first read,
	// through the same handling a read line gets; the echoed "> line"
	// keeps the terminal record showing what ran. Piped stdin lines
	// follow as they always have (ADR-0055's boundary is untouched).
	pending := initialInput
	for {
		var input string
		if pending != "" {
			input, pending = pending, ""
			fmt.Fprintf(stderr, "\n> %s\n", input)
		} else {
			line, err := reader.Read("\n> ")
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(stderr, msgs.Bye)
				return nil
			}
			if err != nil {
				return err
			}
			input = line
		}
		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if strings.HasPrefix(input, "!") {
			command := strings.TrimSpace(strings.TrimPrefix(input, "!"))
			if command == "" {
				continue
			}
			// runTurn's signal handling: Ctrl+C interrupts the command,
			// not the process (ADR-0021 — outside it, the default action
			// killed the whole session).
			_ = runTurnWith(ctx, ladder, func(shellCtx context.Context) error {
				fmt.Fprintln(cmd.OutOrStdout(), runDirectShell(shellCtx, registry, ag, command))
				return nil
			})
			continue
		}
		if input == "/settings" {
			writeSettingsTable(stderr, settings.data())
			continue
		}
		if turn, handled, errMsg := expandSkillInput(input, skillsList); handled {
			if errMsg != "" {
				fmt.Fprintln(stderr, errMsg)
				continue
			}
			runErr := runTurnWith(ctx, ladder, func(turnCtx context.Context) error {
				_, err := ag.Run(turnCtx, turn, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
				return err
			})
			fmt.Fprintln(cmd.OutOrStdout())
			if runErr != nil && !errors.Is(runErr, errInterrupted) {
				fmt.Fprintf(stderr, "%s%v\n", msgs.ErrorPrefix, runErr)
			}
			continue
		}
		// The plain REPL owns stdin on this goroutine, so the learn pass
		// runs inline: there is no event loop to deadlock, and Ctrl+C
		// reaches it through the shared context.
		if input == "/riskbook learn" {
			rbRunner.Learn(ctx)
			continue
		}
		if input == "/compact" {
			// Synchronous here: the plain REPL has no phase machine, and
			// nothing else can be happening while it waits for a line.
			// runTurn makes Ctrl+C interrupt the summariser call rather
			// than kill the process (ADR-0021).
			runErr := runTurnWith(ctx, ladder, func(compactCtx context.Context) error {
				note, err := compactNow(compactCtx, ag, msgs, sink)
				if err == nil {
					fmt.Fprintln(stderr, note)
				}
				return err
			})
			if errors.Is(runErr, errInterrupted) {
				fmt.Fprintln(stderr, msgs.Interrupted)
			} else if runErr != nil {
				fmt.Fprintf(stderr, "%s%v\n", msgs.ErrorPrefix, runErr)
			}
			continue
		}
		if strings.HasPrefix(input, "/") {
			out, _, quit := slashOutput(input, ag, registry, mcpSummary, skillsList,
				slashReloads{mcp: reloadMCP, skills: reloadSkills},
				func() string { return usageReport(ag, tally, cfg.Model.Name, summaryModel) },
				func() string { return memoryListing(memBase, projectDir) },
				rbRunner.Command, appVersion, msgs, onClear)
			fmt.Fprint(stderr, out)
			if quit {
				return nil
			}
			continue
		}

		// SIGINT cancels the in-flight turn, not the process.
		runErr := runTurnWith(ctx, ladder, func(turnCtx context.Context) error {
			_, err := ag.Run(turnCtx, input, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
			return err
		})
		fmt.Fprintln(cmd.OutOrStdout())
		if runErr != nil {
			if errors.Is(runErr, errInterrupted) {
				fmt.Fprintln(stderr, msgs.Interrupted)
				continue
			}
			fmt.Fprintf(stderr, "%s%v\n", msgs.ErrorPrefix, runErr)
		}
	}
}

var errInterrupted = errors.New("interrupted")

// startupNotes tees startup-time stderr lines so the TUI can replay
// them in the banner after its first ClearScreen (ADR-0021).
type startupNotes struct {
	w      io.Writer
	lines  []string
	frozen bool
}

// freeze stops recording: the banner has been built, and a session-long
// tee would only grow.
func (s *startupNotes) freeze() { s.frozen = true; s.lines = nil }

func (s *startupNotes) Write(p []byte) (int, error) {
	if !s.frozen {
		for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
			if strings.TrimSpace(line) != "" {
				s.lines = append(s.lines, line)
			}
		}
	}
	return s.w.Write(p)
}

// stdinCap bounds a piped stdin read (ADR-0055 §2): history is resent
// every round, so an unbounded pipe would burn the context window
// before the first response. The clip is disclosed inside the
// attachment so a part cannot masquerade as the whole (ADR-0014).
const stdinCap = 256 * 1024

// readPipedStdin reads bounded piped stdin for one-shot mode
// (ADR-0055). It returns the attachment content ("" when stdin is
// empty) and a warning ("" when none): binary input is skipped with the
// warning naming why.
func readPipedStdin(r io.Reader) (content, warning string) {
	buf, err := io.ReadAll(io.LimitReader(r, stdinCap+1))
	if err != nil {
		return "", "stdin read failed, nothing attached: " + err.Error()
	}
	clipped := false
	if len(buf) > stdinCap {
		buf, clipped = buf[:stdinCap], true
	}
	if len(buf) == 0 {
		return "", ""
	}
	if clipped {
		// The cap may have split a multi-byte rune; dropping at most
		// three tail bytes repairs that without masking real garbage.
		for i := 0; i < 3 && len(buf) > 0 && !utf8.Valid(buf); i++ {
			buf = buf[:len(buf)-1]
		}
	}
	if bytes.IndexByte(buf, 0) >= 0 || !utf8.Valid(buf) {
		return "", "piped stdin is not UTF-8 text, nothing attached — pass binary files by path (@ reference) instead"
	}
	s := string(buf)
	if clipped {
		s += "\n[stdin clipped at 256 KiB — the rest of the pipe was not read]"
	}
	return s, ""
}

// stdinWaitNotice is the grace before a still-open piped stdin is
// announced (ADR-0067 §1): long enough that `< /dev/null`, here-strings
// and `echo … |` stay silent, short enough that an idle inherited pipe
// is named before anyone reads the silence as a hang. The TUI
// heartbeat (ADR-0033 §1) never runs in -p, so this is the only wait
// notice in play here.
const stdinWaitNotice = 2 * time.Second

// stdinOutcomeLine is the stderr line that closes a piped-stdin read
// (ADR-0067 §2): the byte count when content was attached; the "ended
// empty" line only when the wait was announced and no warning already
// said nothing was attached; nothing otherwise, so the silent fast
// path stays silent.
func stdinOutcomeLine(content, warning string, waited bool) string {
	switch {
	case content != "":
		return fmt.Sprintf("[stdin: %d bytes attached as data]", len(content))
	case waited && warning == "":
		return "[stdin: ended empty — nothing attached]"
	}
	return ""
}

// stdinWaitMessage names both remedies, because the operator reading
// it cannot tell a slow producer from an idle pipe.
const stdinWaitMessage = "[stdin: waiting for piped input to end (no EOF after 2s) — close the pipe, or run with < /dev/null if nothing should be attached]"

// readPipedStdinNoticing is readPipedStdin with the wait announced:
// if the read has not finished within `after`, notify is called once,
// then the read continues to EOF unchanged (ADR-0067). The reader is
// never abandoned — a slow producer must not be cut off — so a truly
// endless pipe still blocks, but no longer silently.
func readPipedStdinNoticing(r io.Reader, after time.Duration, notify func()) (content, warning string) {
	type result struct{ content, warning string }
	done := make(chan result, 1)
	go func() {
		c, w := readPipedStdin(r)
		done <- result{c, w}
	}()
	select {
	case res := <-done:
		return res.content, res.warning
	case <-time.After(after):
		if notify != nil {
			notify()
		}
		res := <-done
		return res.content, res.warning
	}
}

// applyAllowFlag merges --allow entries into the global policy scope as
// "never" values (ADR-0053 §2). It carries the [approval.tools]
// vocabulary and validation exactly; entries are trimmed because the
// comma-separated form invites "a, b".
func applyAllowFlag(merged map[string]string, entries []string) error {
	for _, pattern := range entries {
		pattern = strings.TrimSpace(pattern)
		if err := policy.ValidateEntry("--allow", pattern); err != nil {
			return err
		}
		merged[pattern] = "never"
	}
	return nil
}

// firstMessage resolves the positional argument into the first
// interactive turn (ADR-0064). Whitespace-only counts as absent. -p
// beside it is an error, because the two select different session
// shapes — one answers and exits, the other starts and stays — and
// ambiguity is refused, not resolved by precedence.
func firstMessage(args []string, oneShot bool) (string, error) {
	if len(args) == 0 {
		return "", nil
	}
	msg := strings.TrimSpace(args[0])
	if msg == "" {
		return "", nil
	}
	if oneShot {
		return "", fmt.Errorf("cannot combine -p with a first-message argument: -p runs one turn and exits, the argument starts an interactive session (ADR-0064)")
	}
	return msg, nil
}

// effectiveAuto derives the session's auto-approve state (ADR-0053):
// the config key arms interactive sessions only — an unattended run's
// grant must be visible on the invocation itself — and --auto arms any
// mode.
func effectiveAuto(cfgAuto, oneShot, flagAuto bool) bool {
	return flagAuto || (cfgAuto && !oneShot)
}

// denyGate is the one-shot approver: it denies every mutating call with
// a visible reason instead of blocking on an approval prompt that
// nothing will answer.
type denyGate struct{ out io.Writer }

func (d denyGate) Approve(toolName, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	why := "mutating tools are disabled in one-shot mode; approve interactively, grant with --allow, or arm the risk ladder with --auto (ADR-0053)"
	if reason != "" {
		// The ladder or the rule tier said why this call needs a human;
		// with no human here, that reason is the denial's story
		// (ADR-0053 §3).
		why = reason + " — nobody to ask in one-shot mode"
	}
	fmt.Fprintf(d.out, "[denied: %s %s — %s]\n", toolName, detail, why)
	// What it wanted, on the record: a one-shot run that ends in denials
	// is exactly the case where the operator has to reconstruct the
	// agent's plan afterwards (ADR-0047 §5).
	if purpose != "" {
		fmt.Fprintf(d.out, "[denied: ↪ %s]\n", purpose)
	}
	// No operator, no typed reason (ADR-0060 §1): the model keeps the
	// standing "ask the user" denial text.
	return false, false, ""
}

// interruptLadder is the plain REPL / one-shot counterpart of the
// TUI's three-press exit (ADR-0034 §3, extended by ADR-0065 §3). The
// first Ctrl+C of a turn cancels it; the second calls Warn; the third
// calls Quit. Before ADR-0065, signal.NotifyContext swallowed every
// SIGINT after the first, so a wedged turn outside the TUI could only
// be killed from another terminal. Armed is a test hook: it fires once
// the signal handler is registered, so a test can raise SIGINT
// without racing the registration.
type interruptLadder struct {
	// Interrupting fires on the first press, so the operator sees the
	// press land before the turn returns (the TUI shows "interrupting…"
	// for the same reason).
	Interrupting func()
	Warn         func()
	Quit         func()
	Armed        func()
}

// ladderStep names what the n-th SIGINT of one turn does. Pure, so the
// ladder's shape is pinned without raising signals.
func ladderStep(n int) string {
	switch {
	case n <= 1:
		return "cancel"
	case n == 2:
		return "warn"
	default:
		return "quit"
	}
}

// runTurn runs fn under a SIGINT-cancellable context with no ladder:
// extra presses do nothing, which is the pre-ADR-0065 behaviour the
// tests pin.
func runTurn(parent context.Context, fn func(ctx context.Context) error) error {
	return runTurnWith(parent, nil, fn)
}

// runTurnWith runs fn under a SIGINT-cancellable context, climbing the
// ladder on repeated presses, and maps a cancellation-caused failure
// to errInterrupted. The context error MUST be captured before the
// deferred cancel — consulting it afterwards would misreport every
// error (404s included) as a user interrupt (regression test in
// turn_test.go).
func runTurnWith(parent context.Context, ladder *interruptLadder, fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Buffered generously: signal.Notify drops a signal the channel
	// cannot take, and a press arriving while the ladder goroutine is
	// writing the previous warning must still count (review finding).
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, os.Interrupt)
	defer signal.Stop(sigs)
	finished := make(chan struct{})
	go func() {
		presses := 0
		for {
			select {
			case <-finished:
				return
			case <-sigs:
				presses++
				switch ladderStep(presses) {
				case "cancel":
					cancel()
					if ladder != nil && ladder.Interrupting != nil {
						ladder.Interrupting()
					}
				case "warn":
					if ladder != nil && ladder.Warn != nil {
						ladder.Warn()
					}
				default:
					if ladder != nil && ladder.Quit != nil {
						ladder.Quit()
					}
				}
			}
		}
	}()
	if ladder != nil && ladder.Armed != nil {
		ladder.Armed()
	}
	err := fn(ctx)
	canceled := ctx.Err() != nil
	close(finished)
	if err != nil && canceled {
		return errInterrupted
	}
	return err
}

// buildExecFn returns the shell execution strategy: sandbox-exec-wrapped
// when the sandbox is on, direct bash otherwise.
func buildExecFn(sandboxOn bool, projectDir, workDir string) (tools.ExecFunc, error) {
	if !sandboxOn {
		return func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, shell, "-c", command)
		}, nil
	}
	if _, err := os.Stat(sandbox.Executable); err != nil {
		return nil, fmt.Errorf("%s not found (gem-agent is macOS-only); use --no-sandbox to bypass at your own risk", sandbox.Executable)
	}
	writeDirs := []string{projectDir}
	if workDir != "" {
		// The session work directory (ADR-0058). A shell command told to
		// put its output in $GEMAGENT_WORK_DIR has to be able to.
		if resolved, err := sandbox.ResolveWriteDir(workDir); err == nil {
			writeDirs = append(writeDirs, resolved)
		}
	}
	// Scratch locations shell tools legitimately write to — the one
	// list the rule tier reads as well (ADR-0070 §2), so what it calls
	// "outside the writable roots" is what Seatbelt will deny.
	writeDirs = append(writeDirs, sandbox.ScratchDirs()...)
	profile, err := sandbox.Profile(writeDirs)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, command string) *exec.Cmd {
		argv := sandbox.Wrap(profile, shell, command)
		return exec.CommandContext(ctx, argv[0], argv[1:]...)
	}, nil
}

// compactNow runs a manual /compact and renders the outcome. "Nothing
// to compact" is a normal answer, not an error: the operator asked a
// reasonable question and the answer is no.
func compactNow(ctx context.Context, ag *agent.Agent, msgs *uitext.Messages, sink *telemetry.Sink) (string, error) {
	res, err := ag.Compact(ctx)
	switch {
	case errors.Is(err, agent.ErrNothingToCompact):
		return msgs.NothingToCompact, nil
	case err != nil:
		return "", err
	}
	return fmt.Sprintf(msgs.CompactedFmt, res.Replaced, res.After-1), nil
}

// runDirectShell executes a !-prefixed command through the same
// sandboxed shell_exec tool the agent uses (same timeout, output cap,
// exit-status surfacing — no approval prompt: the user typed it), and
// feeds command + output into the agent history so the next turn can
// refer to what happened.
func runDirectShell(ctx context.Context, registry *tools.Registry, ag *agent.Agent, command string) string {
	tool, ok := registry.Get("shell_exec")
	if !ok {
		return "error: shell_exec is unavailable"
	}
	out, err := tool.Run(ctx, map[string]any{"command": command})
	if err != nil {
		out = "error: " + err.Error()
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	// The prefix is a shared constant: the session listing uses it to
	// tell an injected message from one the operator typed.
	ag.AddContext(session.ShellContextPrefix + "\n$ " + command + "\n\nOutput:\n" + out)
	return out
}

// slashCompletions returns the Tab-completion source for "/"-prefixed
// input: command names, and skill names after "/skill ". The command
// list mirrors what slashOutput and the REPL loop actually accept.
// slashCompletions reads the skill list through the getter on every
// call: /skills reload swaps the list mid-session (ADR-0039).
func slashCompletions(getSkills func() []skills.Skill) func(string) []string {
	commands := []string{
		"/auto", "/clear", "/compact", "/exit", "/help", "/mcp", "/memory",
		"/quit", "/riskbook", "/settings", "/skill", "/skills", "/tools",
		"/usage", "/version",
	}
	return func(prefix string) []string {
		if rest, ok := strings.CutPrefix(prefix, "/skill "); ok {
			var out []string
			for _, s := range getSkills() {
				if strings.HasPrefix(s.Name, rest) {
					out = append(out, "/skill "+s.Name)
				}
			}
			return out
		}
		var out []string
		for _, c := range commands {
			if strings.HasPrefix(c, prefix) {
				out = append(out, c)
			}
		}
		return out
	}
}

// policyBannerLine summarises the approval policy for the banner. A
// full dump grows one entry per 'p' answer and MCP wildcard until the
// line is a wall of rules nobody reads (operator report) — the banner
// is a glance; /tools and /settings hold the statement.
func policyBannerLine(rules []string) string {
	const show = 3
	if len(rules) <= show {
		return "approval policy: " + strings.Join(rules, ", ")
	}
	return fmt.Sprintf("approval policy: %s, … %d rules total (/tools shows each tool's effective gate)",
		strings.Join(rules[:show], ", "), len(rules))
}

// abbreviateHome shortens the home-directory prefix to "~" for display.
func abbreviateHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if strings.HasPrefix(path, home+string(os.PathSeparator)) {
		return "~" + path[len(home):]
	}
	return path
}

// resolveTheme maps [tui].theme to the TUI's theme value. "auto" runs
// background detection, which sends an OSC query and reads the reply —
// it must happen HERE, before Bubble Tea puts the terminal in raw mode,
// or the reply leaks into the input box as phantom keys.
func resolveTheme(configured string) string {
	switch configured {
	case "dark", "light":
		return configured
	case "plain":
		return "notty"
	default: // "auto"
		if lipgloss.HasDarkBackground() {
			return "dark"
		}
		return "light"
	}
}

// slashOutput executes a /command and returns its output text — shared
// by the TUI (which prints it into scrollback, errors highlighted) and
// the plain REPL (which writes it to stderr).
// slashReloads carries the ADR-0039 reload closures into slashOutput;
// either may be nil (tests), which reads as "not available here".
type slashReloads struct {
	mcp    func() string
	skills func() string
}

func slashOutput(input string, ag *agent.Agent, registry *tools.Registry, mcpSummary []string, skillsList []skills.Skill, reload slashReloads, usage func() string, memoryInfo func() string, riskbookCmd func(args []string) (string, bool), version string, msgs *uitext.Messages, onClear func() string) (output string, isErr bool, quit bool) {
	var b strings.Builder
	fields := strings.Fields(input)
	// The one supported subcommand shape: "/mcp reload" and
	// "/skills reload" (ADR-0039). Anything else after the command is a
	// typo and says so, instead of silently showing the listing.
	sub := ""
	if len(fields) > 1 {
		sub = fields[1]
	}
	if (fields[0] == "/mcp" || fields[0] == "/skills") && sub != "" {
		var fn func() string
		if sub == "reload" {
			if fields[0] == "/mcp" {
				fn = reload.mcp
			} else {
				fn = reload.skills
			}
		}
		if fn == nil {
			fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
			return b.String(), true, false
		}
		return fn(), false, false
	}
	switch fields[0] {
	case "/help":
		// The text lives in uitext (ADR-0029) — both languages in full,
		// pinned to the command set by TestHelpListsEveryCommand.
		b.WriteString(msgs.Help)
	case "/tools":
		// The LIVE policy: a /settings edit or a 'p' answer mid-session
		// must show here, or the display the operator audits gating with
		// no longer reflects the gate (ADR-0021).
		pol := ag.Policy()
		for _, t := range registry.List() {
			marker := "read-only"
			if t.Mutating {
				marker = "requires approval"
			}
			// The effective policy, not the default, is what the
			// operator needs to see here.
			switch pol.For(t.Name) {
			case policy.AlwaysAsk:
				marker = "always asks (policy)"
			case policy.NeverAsk:
				marker = "never asks (policy)"
				if t.Mutating {
					marker = "never asks (policy; blocked commands still ask)"
				}
			}
			fmt.Fprintf(&b, "  %-12s %s (%s)\n", t.Name, firstSentence(t.Description), marker)
		}
	case "/riskbook":
		// `learn` is intercepted upstream on both surfaces (it drafts
		// with a model and drives dialogs); the synchronous subcommands
		// answer here.
		out, isErr := riskbookCmd(fields[1:])
		b.WriteString(out)
		if isErr {
			return b.String(), true, false
		}
	case "/auto":
		ag.SetAutoApprove(!ag.AutoApprove())
		if ag.AutoApprove() {
			b.WriteString(msgs.AutoOn)
		} else {
			b.WriteString(msgs.AutoOff)
		}
	case "/usage":
		b.WriteString(usage())
	case "/version":
		b.WriteString(versionLine(version))
	case "/memory":
		b.WriteString(memoryInfo())
	case "/skills":
		b.WriteString(skillsListing(skillsList))
	case "/clear":
		// A cleared conversation is a new session (ADR-0071 §2): onClear
		// closes the old transcript where the conversation ended — no
		// clear record, so it stays resumable — and starts the next.
		// Without it (tests), the history is cleared in place.
		if onClear != nil {
			b.WriteString(onClear())
		} else {
			ag.Reset()
		}
		b.WriteString(msgs.HistoryCleared)
	case "/quit", "/exit":
		return "bye\n", false, true
	case "/mcp":
		if len(mcpSummary) == 0 {
			b.WriteString(msgs.MCPNone)
		} else {
			for _, s := range mcpSummary {
				b.WriteString("  " + s + "\n")
			}
		}
	default:
		fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
		return b.String(), true, false
	}
	return b.String(), false, false
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// exportWorkDir publishes the session work directory to children, or
// removes the variable when the session has none — a stale value
// would be inherited by every process spawned afterwards.
func exportWorkDir(dir string) error {
	if dir == "" {
		return os.Unsetenv(workdir.EnvVar)
	}
	return os.Setenv(workdir.EnvVar, dir)
}

// liveExec is the shell strategy the registry calls through, so /clear
// can swap the sandbox profile for the new work directory (ADR-0071
// §2) without rebuilding the registry. Guarded: an abandoned shell call
// (ADR-0065) may still hold the old strategy while the next turn
// starts on the new one.
type liveExec struct {
	mu sync.RWMutex
	fn tools.ExecFunc
}

func (e *liveExec) run(ctx context.Context, command string) *exec.Cmd {
	e.mu.RLock()
	fn := e.fn
	e.mu.RUnlock()
	return fn(ctx, command)
}

func (e *liveExec) set(fn tools.ExecFunc) {
	e.mu.Lock()
	e.fn = fn
	e.mu.Unlock()
}

// liveLog is a SessionLog that forwards to whichever transcript is in
// use — the value /clear reassigns (ADR-0071 §2) — for the side-call
// tools registered at startup. nil (session log disabled) fails the
// write, which every side call already treats as best-effort.
type liveLog struct{ get func() agent.SessionLog }

func (l liveLog) Log(kind string, data any) error {
	if lg := l.get(); lg != nil {
		return lg.Log(kind, data)
	}
	return errors.New("session log disabled")
}

// rotateWorkDir points every consumer of the session work directory at
// dir ("" for none): the file tools' second root, the sandbox profile,
// and the system prompt. The MCP intake reads the registry, so it
// follows on its own. Returns operator notes for what could not follow.
func rotateWorkDir(registry *tools.Registry, shellExec *liveExec, sandboxOn bool, projectDir, dir string, setSystem func()) []string {
	var notes []string
	if err := registry.UseWorkDir(dir); err != nil {
		notes = append(notes, fmt.Sprintf("file tools keep the previous work directory: %v", err))
	}
	if fn, err := buildExecFn(sandboxOn, projectDir, dir); err != nil {
		notes = append(notes, fmt.Sprintf("shell commands keep the previous sandbox profile: %v", err))
	} else {
		shellExec.set(fn)
	}
	if setSystem != nil {
		setSystem()
	}
	return notes
}

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

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/approve"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/diagram"
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
	Use:   "gem-agent",
	Short: "Interactive CLI agent backed by Vertex AI Gemini (Claude Code fallback)",
	Long: `gem-agent is an interactive CLI agent backed by Vertex AI Gemini 3.x,
built as a continuity tool for development work when Claude Code is
unavailable. It provides file read/write, sandboxed command execution,
and (from Phase 2) MCP server connectivity, with write/exec gated behind
per-call approval.

The current working directory is the project: file access is confined to
it, and shell file-writes are restricted to it by macOS sandbox-exec.

macOS only. See docs/ for the RFP and design records.`,
	Args:         cobra.NoArgs,
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
	// Everything downstream — the agent, telemetry — reads this one
	// effective value, never the raw config field.
	autoOn := effectiveAuto(cfg.Agent.AutoApprove, oneShot, flagAuto)
	// UI language, resolved once (ADR-0029): the chrome that follows —
	// prompts, TUI, slash output — is built with it.
	uiLang := uitext.Resolve(cfg.TUI.Language, os.Getenv)
	msgs := uitext.For(uiLang)

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

	// --- shell execution strategy (ADR-0001 defense-in-depth) ---
	sandboxOn := cfg.Sandbox.Enabled && !flagNoSandbox
	execFn, err := buildExecFn(sandboxOn, projectDir)
	if err != nil {
		return err
	}

	registry, err := tools.New(projectDir, execFn, time.Duration(cfg.Agent.ShellTimeoutSec)*time.Second)
	if err != nil {
		return err
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
	// A missing home disables memory with a warning; a backup tool must
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

	// A broken log warns; it must not block a backup tool. A broken
	// *resume* is fatal, though — the operator asked for that history,
	// and continuing without it silently would be worse than stopping.
	var sessionLog agent.SessionLog
	sessionPath := "(disabled)"
	sessionID := ""
	if sessionDirErr != nil {
		fmt.Fprintf(stderr, "warning: session log disabled: %v\n", sessionDirErr)
	} else {
		lg, err := openSessionLog(sessionDir, resumedID, projectDir, cfg.Model.Name, cmd.Root().Version)
		switch {
		case err != nil && resumedID != "":
			return fmt.Errorf("cannot append to session %s: %w", resumedID, err)
		case err != nil:
			fmt.Fprintf(stderr, "warning: session log disabled: %v\n", err)
		default:
			defer lg.Close()
			sessionLog = lg
			sessionPath = lg.Path()
			sessionID = lg.ID()
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
			defer sink.Shutdown()
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
	if err := registerSummarizeTool(registry, summaryBackend, summaryModel, sessionLog, tally); err != nil {
		return err
	}
	// Web access (ADR-0017): grounded search on the main model, digested
	// fetch on the lightweight one. Both egress-gated by default.
	if err := registerWebTools(registry, backend, summaryBackend, cfg.Model.Name, summaryModel, sessionLog, tally); err != nil {
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
	// Operator pre-tool hooks (ADR-0044): global config only, evaluated
	// ahead of the approval ladder in every mode — the guards exist for
	// the model's calls regardless of surface. nil when none are
	// configured, so the common case pays nothing.
	var preToolHook func(ctx context.Context, name string, args map[string]any) (bool, string)
	if len(cfg.Hooks.PreToolUse) > 0 {
		hs := make([]hooks.Hook, 0, len(cfg.Hooks.PreToolUse))
		for _, e := range cfg.Hooks.PreToolUse {
			hs = append(hs, hooks.Hook{
				Matcher: e.Matcher, Command: e.Command,
				Timeout: time.Duration(e.TimeoutSec) * time.Second,
			})
		}
		runner := hooks.New(hs, func(warn string) {
			if prog != nil {
				prog.Send(tui.Attached{Notes: []string{warn}})
				return
			}
			fmt.Fprintf(stderr, "[⚠ %s]\n", warn)
		})
		preToolHook = func(ctx context.Context, name string, args map[string]any) (bool, string) {
			return runner.Pre(ctx, name, projectDir, args)
		}
	}

	// render_diagram draws into the TUI, so it exists only there
	// (ADR-0043): a surface must not advertise what it cannot do. The
	// closure captures prog, which is assigned when the program starts.
	if useTUI {
		if err := registerDiagramTool(registry, func(art string) {
			if prog != nil {
				prog.Send(tui.Diagram{Art: art})
			}
		}); err != nil {
			return err
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
			MCPServers:     mcpSummary,
			SkillCount:     len(skillsList),
			DiagramCols:    diagramCols(useTUI),
			DiagramRows:    diagramRows(useTUI),
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
		log:       sessionLog,
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
	// rebuilds exactly what startup built (ADR-0039). The terminal
	// diagram section rides only where the TUI renders it (ADR-0042):
	// the plain REPL and one-shot show source and must not advertise a
	// capability they lack.
	composeSystem := func() string {
		s := buildSystemPrompt(projectDir, projectContext) + skills.PromptSection(skillsList) + memorySection
		if useTUI {
			s += diagram.PromptSection()
		}
		return s
	}
	ag = agent.New(agent.Options{
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
		OnUsage: func(promptTokens, outputTokens, cachedTokens int) {
			sink.Usage(promptTokens, outputTokens, cachedTokens)
			if prog != nil {
				prog.Send(tui.Usage{Prompt: promptTokens, Output: outputTokens, Cached: cachedTokens})
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

	// --- one-shot mode: single turn, quiet stderr, exit ---
	if oneShot {
		for _, n := range policyNotes {
			fmt.Fprintf(stderr, "warning: %s\n", n)
		}
		go resolveWindow()
		if !sandboxOn {
			fmt.Fprintln(stderr, "sandbox: DISABLED — shell commands run unconfined")
		}
		runErr := runTurn(ctx, func(turnCtx context.Context) error {
			_, err := ag.Run(turnCtx, flagPrompt, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
			return err
		})
		fmt.Fprintln(cmd.OutOrStdout())
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
			Msgs:       msgs,
			Theme:      resolveTheme(cfg.TUI.Theme),
			ModelName:  cfg.Model.Name,
			ProjectDir: abbreviateHome(projectDir),
			Banner:     bannerLines,
			AutoMode:   ag.AutoApprove(),
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
					rbRunner.Command, appVersion, msgs)
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
	for {
		input, err := reader.Read("\n> ")
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, msgs.Bye)
			return nil
		}
		if err != nil {
			return err
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
			_ = runTurn(ctx, func(shellCtx context.Context) error {
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
			runErr := runTurn(ctx, func(turnCtx context.Context) error {
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
			runErr := runTurn(ctx, func(compactCtx context.Context) error {
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
				rbRunner.Command, appVersion, msgs)
			fmt.Fprint(stderr, out)
			if quit {
				return nil
			}
			continue
		}

		// SIGINT cancels the in-flight turn, not the process.
		runErr := runTurn(ctx, func(turnCtx context.Context) error {
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

func (d denyGate) Approve(toolName, detail, purpose, reason string, mustPrompt bool) (bool, bool) {
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
	return false, false
}

// runTurn runs fn under a SIGINT-cancellable context and maps a
// cancellation-caused failure to errInterrupted. The context error MUST
// be captured before stop() — signal.NotifyContext's stop() cancels the
// context itself, so consulting it afterwards misreports every error
// (404s included) as a user interrupt.
func runTurn(parent context.Context, fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	err := fn(ctx)
	canceled := ctx.Err() != nil
	stop()
	if err != nil && canceled {
		return errInterrupted
	}
	return err
}

// buildExecFn returns the shell execution strategy: sandbox-exec-wrapped
// when the sandbox is on, direct bash otherwise.
func buildExecFn(sandboxOn bool, projectDir string) (tools.ExecFunc, error) {
	if !sandboxOn {
		return func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, shell, "-c", command)
		}, nil
	}
	if _, err := os.Stat(sandbox.Executable); err != nil {
		return nil, fmt.Errorf("%s not found (gem-agent is macOS-only); use --no-sandbox to bypass at your own risk", sandbox.Executable)
	}
	writeDirs := []string{projectDir}
	// Scratch locations shell tools legitimately write to. Resolved to
	// real paths — Seatbelt matches post-symlink (/tmp is /private/tmp).
	for _, d := range []string{os.TempDir(), "/private/tmp", "/dev"} {
		if resolved, err := sandbox.ResolveWriteDir(d); err == nil {
			writeDirs = append(writeDirs, resolved)
		}
	}
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

func slashOutput(input string, ag *agent.Agent, registry *tools.Registry, mcpSummary []string, skillsList []skills.Skill, reload slashReloads, usage func() string, memoryInfo func() string, riskbookCmd func(args []string) (string, bool), version string, msgs *uitext.Messages) (output string, isErr bool, quit bool) {
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
		ag.Reset()
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

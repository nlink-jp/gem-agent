package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/approve"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/memory"
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/repl"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/tui"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	flagConfig    string
	flagModel     string
	flagNoSandbox bool
	flagPrompt    string
	flagContinue  bool
	flagResume    string
)

// Execute runs the root command.
func Execute(version string) {
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
	rootCmd.Flags().BoolVar(&flagNoSandbox, "no-sandbox", false, "disable the sandbox-exec wrapper for shell_exec (debugging only, unsafe)")
	rootCmd.Flags().StringVarP(&flagPrompt, "prompt", "p", "", "one-shot mode: run this prompt and exit (mutating tools are denied)")
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
	cfg, err := config.LoadWithOverrides(cfgPath, config.Overrides{Model: flagModel})
	if err != nil {
		return err
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
	interactive := flagPrompt == "" && term.IsTerminal(int(os.Stdin.Fd()))
	home, _ := os.UserHomeDir()
	if reason := broadRoot(projectDir, home); reason != "" {
		if err := confirmBroadRoot(reason, projectDir, interactive, os.Stdin, cmd.ErrOrStderr()); err != nil {
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
	approvalPolicy, policyNotes, err := policy.Build(
		mergedTools, projectCfg.Approval.Tools, cfg.TrustsProject(projectDir))
	if err != nil {
		return err
	}

	// --- first-run project trust (ADR-0023): does this project's own
	// instruction files / .mcp.json / skills get loaded at all? ---
	projectTrusted, trustNote := resolveProjectTrust(
		cfg, policyFile, policyPath, projectDir, interactive, os.Stdin, cmd.ErrOrStderr())
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
	mcpClients, mcpSummary := connectMCPServers(ctx, cfg, projectDir, cmd.Root().Version, registry, stderr, projectTrusted)
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// --- skills: Claude Code's skill library, read as-is (ADR-0010) ---
	skillsList, skillNotes := discoverSkills(projectDir, projectTrusted)
	for _, n := range skillNotes {
		fmt.Fprintf(stderr, "warning: %s\n", n)
	}
	if err := registerSkillTool(registry, skillsList); err != nil {
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
		meta, history, resumeNotes, err := resolveResume(sessionDir, projectDir, cfg.Model.Name, flagResume)
		if err != nil {
			return err
		}
		for _, n := range resumeNotes {
			fmt.Fprintf(stderr, "warning: %s\n", n)
		}
		restored, resumedID = history, meta.ID
	}

	// A broken log warns; it must not block a backup tool. A broken
	// *resume* is fatal, though — the operator asked for that history,
	// and continuing without it silently would be worse than stopping.
	var sessionLog agent.SessionLog
	sessionPath := "(disabled)"
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
		}
	}

	// --- LLM backend ---
	backend, err := llm.NewVertex(ctx, cfg.GCP.Project, cfg.GCP.Location, cfg.Model.Name, cfg.Model.Safety)
	if err != nil {
		return err
	}

	// Per-category token accounting for /usage (ADR-0019).
	tally := newUsageTally()

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

	oneShot := flagPrompt != ""
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

	ag := agent.New(agent.Options{
		Backend:  backend,
		Registry: registry,
		Gate:     gate,
		Log:      sessionLog,
		System:   buildSystemPrompt(projectDir, projectContext) + skills.PromptSection(skillsList) + memorySection,
		MaxTurns: cfg.Agent.MaxTurns,
		Policy:   approvalPolicy,
		// load_skill results are operator-authored instructions, not
		// data; its reads are confined to skill directories (ADR-0010).
		InstructionTools: []string{skills.ToolName},
		ClipboardImage:   clipboardImage,
		OnToolCall: func(tc llm.ToolCall) {
			if prog != nil {
				prog.Send(tui.ToolCall{Name: tc.Name, Detail: agent.CallDetail(tc)})
				return
			}
			fmt.Fprintf(stderr, "\n[tool] %s %s\n", tc.Name, agent.CallDetail(tc))
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
		OnUsage: func(promptTokens, outputTokens, cachedTokens int) {
			if prog != nil {
				prog.Send(tui.Usage{Prompt: promptTokens, Output: outputTokens, Cached: cachedTokens})
			}
		},
		AutoCompact:  cfg.Agent.AutoCompact,
		CompactAtPct: cfg.Agent.CompactAtPct,
		AutoApprove:  cfg.Agent.AutoApprove && !oneShot,
		OnAutoDecision: func(tc llm.ToolCall, d agent.AutoDecision) {
			if !d.Approved {
				return // the escalation shows up in the approval prompt
			}
			if prog != nil {
				prog.Send(tui.AutoApproved{Tool: tc.Name, Reason: d.Reason, Tier: d.Tier.String()})
				return
			}
			fmt.Fprintf(stderr, "[auto-approved: %s (%s) — %s]\n", tc.Name, d.Tier, d.Reason)
		},
	})
	if len(restored) > 0 {
		ag.SetHistory(restored)
	}

	settings := &settingsStore{
		cfg: cfg, projectCfg: projectCfg, policyFile: policyFile,
		policyPath: policyPath, projectDir: projectDir,
		registry: registry, ag: ag, current: approvalPolicy,
	}
	settingsData := settings.data()

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
		bannerLines = append(bannerLines,
			"approval policy: "+strings.Join(approvalPolicy.Describe(), ", "))
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
		model := tui.New(tui.Options{
			BaseCtx:    ctx,
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
			Settings:     &settingsData,
			ApplySetting: settings.Apply,
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
					note, err := compactNow(compactCtx, ag)
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
			Slash: func(in string) (string, bool, bool) {
				return slashOutput(in, ag, registry, mcpSummary, skillsList,
					func() string { return usageReport(ag, tally, cfg.Model.Name, summaryModel) },
					func() string { return memoryListing(memBase, projectDir) })
			},
		})
		prog = tea.NewProgram(model)
		tuiGate.SetProgram(prog)
		go resolveWindow()
		_, err := prog.Run()
		return err
	}
	go resolveWindow()

	for _, line := range bannerLines {
		fmt.Fprintln(stderr, line)
	}
	fmt.Fprintf(stderr, "/help for commands, Ctrl+D to quit\n")

	// --- plain REPL loop (non-TTY fallback) ---
	for {
		input, err := reader.Read("\n> ")
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, "bye")
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
				fmt.Fprintf(stderr, "error: %v\n", runErr)
			}
			continue
		}
		if input == "/compact" {
			// Synchronous here: the plain REPL has no phase machine, and
			// nothing else can be happening while it waits for a line.
			// runTurn makes Ctrl+C interrupt the summariser call rather
			// than kill the process (ADR-0021).
			runErr := runTurn(ctx, func(compactCtx context.Context) error {
				note, err := compactNow(compactCtx, ag)
				if err == nil {
					fmt.Fprintln(stderr, note)
				}
				return err
			})
			if errors.Is(runErr, errInterrupted) {
				fmt.Fprintln(stderr, "(interrupted)")
			} else if runErr != nil {
				fmt.Fprintf(stderr, "error: %v\n", runErr)
			}
			continue
		}
		if strings.HasPrefix(input, "/") {
			out, _, quit := slashOutput(input, ag, registry, mcpSummary, skillsList,
				func() string { return usageReport(ag, tally, cfg.Model.Name, summaryModel) },
				func() string { return memoryListing(memBase, projectDir) })
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
				fmt.Fprintln(stderr, "(interrupted)")
				continue
			}
			fmt.Fprintf(stderr, "error: %v\n", runErr)
		}
	}
}

var errInterrupted = errors.New("interrupted")

// startupNotes tees startup-time stderr lines so the TUI can replay
// them in the banner after its first ClearScreen (ADR-0021).
type startupNotes struct {
	w     io.Writer
	lines []string
}

func (s *startupNotes) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if strings.TrimSpace(line) != "" {
			s.lines = append(s.lines, line)
		}
	}
	return s.w.Write(p)
}

// denyGate is the one-shot approver: it denies every mutating call with
// a visible reason instead of blocking on an approval prompt that
// nothing will answer.
type denyGate struct{ out io.Writer }

func (d denyGate) Approve(toolName, detail, reason string, mustPrompt bool) bool {
	fmt.Fprintf(d.out, "[denied: %s %s — mutating tools are disabled in one-shot mode; run interactively to approve]\n", toolName, detail)
	return false
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
func compactNow(ctx context.Context, ag *agent.Agent) (string, error) {
	res, err := ag.Compact(ctx)
	switch {
	case errors.Is(err, agent.ErrNothingToCompact):
		return "nothing to compact yet — the conversation is short enough that a summary would lose more than it saves", nil
	case err != nil:
		return "", err
	}
	return fmt.Sprintf("compacted %d earlier messages into a summary; %d kept verbatim. Detail from the summarised part is now second-hand",
		res.Replaced, res.After-1), nil
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
func slashOutput(input string, ag *agent.Agent, registry *tools.Registry, mcpSummary []string, skillsList []skills.Skill, usage func() string, memoryInfo func() string) (output string, isErr bool, quit bool) {
	var b strings.Builder
	switch strings.Fields(input)[0] {
	case "/help":
		b.WriteString(`commands:
  /help    show this help
  /tools   list available tools
  /mcp     show connected MCP servers
  /auto    toggle auto-approve (shift+tab does the same, and works mid-run)
  /compact summarise the older half of the conversation to free context
  /settings show every setting with where it came from; edit policy + toggles
  /usage   token accounting: main loop, cache hit rate, side-calls, web tools
  /memory  list persisted memories (global + this project); saves are approval-gated
  /skills  list installed skills (Claude Code format, read as-is)
  /skill <name> [args]  invoke a skill directly
  /clear   reset the conversation history
  /quit    exit (Ctrl+D also works)
auto-approve: safe changes run unattended; destructive, out-of-project,
  credential-touching, or uncertain calls still ask (two-tier review)
file references:
  @<path>       attach a project file or directory to the message (Tab completes)
  @<img>.png    attach an image — absolute and ~ paths work for images
                (@~/Desktop/shot.png), because you typed them yourself
  @clipboard    attach the clipboard image (Cmd+Ctrl+Shift+4, then this)
shell:
  !<command>  run it directly (sandboxed, no approval; output is shared with the model)
keys:
  Enter 送信 · ↑↓ 履歴 · Ctrl+C 中断/クリア · Ctrl+D 終了
  実行中も入力できます: Enter で次のメッセージとして予約され、ターンが正常に
    終わった時点で送信されます（失敗・中断時は未送信のまま入力欄へ戻ります）
    ※ ! と / のコマンドは予約できません — Ctrl+C で中断してから実行します
  改行（複数行入力）: Ctrl+J もしくは 行末に \ を置いて Enter
    ※ Option+Enter は「Option を Meta として送る」設定の端末でのみ有効
      （既定では通常の Enter と同じバイトになり送信されます）
  複数行ペーストはそのまま 1 メッセージになります
  承認ダイアログ: ←→/Tab で選択 · Enter 決定（y/n/a も可）
mutating tools prompt for approval: y = once, a = always this session
  (Block-tier calls and always-policy tools keep asking — 'a' never
   covers the dangerous cases, only the routine ones)
`)
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
	case "/auto":
		ag.SetAutoApprove(!ag.AutoApprove())
		if ag.AutoApprove() {
			b.WriteString("auto-approve: ON — safe changes run unattended; risky ones still ask\n")
		} else {
			b.WriteString("auto-approve: OFF — every change asks\n")
		}
	case "/usage":
		b.WriteString(usage())
	case "/memory":
		b.WriteString(memoryInfo())
	case "/skills":
		b.WriteString(skillsListing(skillsList))
	case "/clear":
		ag.Reset()
		b.WriteString("history cleared — the next message starts a fresh conversation\n")
	case "/quit", "/exit":
		return "bye\n", false, true
	case "/mcp":
		if len(mcpSummary) == 0 {
			b.WriteString("no MCP servers connected — define them in ~/.config/gem-agent/mcp.json (global) or the project's .mcp.json (project; wins name collisions)\n")
		} else {
			for _, s := range mcpSummary {
				b.WriteString("  " + s + "\n")
			}
			b.WriteString("MCP tools appear in /tools as mcp__<server>__<tool> and always require approval\n")
		}
	default:
		fmt.Fprintf(&b, "unknown command %q — /help lists commands\n", input)
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

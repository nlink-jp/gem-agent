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
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/repl"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/session"
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
}

const shell = "/bin/bash"

func runREPL(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	stderr := cmd.ErrOrStderr()

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
	mcpClients, mcpSummary := connectMCPServers(ctx, cfg, projectDir, cmd.Root().Version, registry, stderr)
	defer func() {
		for _, c := range mcpClients {
			c.Close()
		}
	}()

	// --- session log (a broken log warns; it must not block a backup tool) ---
	var sessionLog agent.SessionLog
	sessionPath := "(disabled)"
	if dir, err := session.DefaultDir(); err == nil {
		if lg, err := session.Open(dir); err == nil {
			defer lg.Close()
			sessionLog = lg
			sessionPath = lg.Path()
		} else {
			fmt.Fprintf(stderr, "warning: session log disabled: %v\n", err)
		}
	} else {
		fmt.Fprintf(stderr, "warning: session log disabled: %v\n", err)
	}

	// --- LLM backend ---
	backend, err := llm.NewVertex(ctx, cfg.GCP.Project, cfg.GCP.Location, cfg.Model.Name, cfg.Model.Safety)
	if err != nil {
		return err
	}

	// --- project instruction files (drop-in: AGENTS.md and friends,
	// including ancestor directories, exactly as other agents read them)
	projectContext, contextLabels, contextNotes := loadInstructions(projectDir)
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
		System:   buildSystemPrompt(projectDir, projectContext),
		MaxTurns: cfg.Agent.MaxTurns,
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
		OnUsage: func(promptTokens, outputTokens int) {
			if prog != nil {
				prog.Send(tui.Usage{Prompt: promptTokens, Output: outputTokens})
			}
		},
		AutoApprove: cfg.Agent.AutoApprove && !oneShot,
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

	// --- one-shot mode: single turn, quiet stderr, exit ---
	if oneShot {
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

	// --- interactive TUI (ADR-0002/0003) ---
	if useTUI {
		// The banner goes through the TUI (not stderr): bottom pinning
		// counts every printed line, and the startup clear would wipe a
		// pre-printed banner anyway.
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
			Shell: func(shellCtx context.Context, command string) {
				go func() {
					out := runDirectShell(shellCtx, registry, ag, command)
					prog.Send(tui.ShellDone{Output: out})
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
				return slashOutput(in, ag, registry, mcpSummary)
			},
		})
		prog = tea.NewProgram(model)
		tuiGate.SetProgram(prog)
		// Resolve the context window for the footer: config override
		// first, otherwise an async model-metadata fetch (never blocks
		// startup; the footer shows "–" until known).
		go func() {
			if cw := cfg.Model.ContextWindow; cw > 0 {
				prog.Send(tui.ContextWindow{Tokens: cw})
				return
			}
			mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if w, err := backend.ContextWindow(mctx); err == nil && w > 0 {
				prog.Send(tui.ContextWindow{Tokens: w})
				return
			}
			// Vertex publisher metadata omits inputTokenLimit (measured
			// 2026-08: Models.Get succeeds with 0). The Gemini 2.5/3
			// family is uniformly 1M input tokens, so show that as an
			// explicit estimate (~) rather than a permanent unknown;
			// [model].context_window overrides with a firm value.
			prog.Send(tui.ContextWindow{Tokens: 1_048_576, Assumed: true})
		}()
		_, err := prog.Run()
		return err
	}

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
			fmt.Fprintln(cmd.OutOrStdout(), runDirectShell(ctx, registry, ag, command))
			continue
		}
		if strings.HasPrefix(input, "/") {
			out, _, quit := slashOutput(input, ag, registry, mcpSummary)
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

// denyGate is the one-shot approver: it denies every mutating call with
// a visible reason instead of blocking on an approval prompt that
// nothing will answer.
type denyGate struct{ out io.Writer }

func (d denyGate) Approve(toolName, detail, reason string) bool {
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
	ag.AddContext("I ran this shell command myself:\n$ " + command + "\n\nOutput:\n" + out)
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
func slashOutput(input string, ag *agent.Agent, registry *tools.Registry, mcpSummary []string) (output string, isErr bool, quit bool) {
	var b strings.Builder
	switch strings.Fields(input)[0] {
	case "/help":
		b.WriteString(`commands:
  /help    show this help
  /tools   list available tools
  /mcp     show connected MCP servers
  /auto    toggle auto-approve (shift+tab does the same, and works mid-run)
  /clear   reset the conversation history
  /quit    exit (Ctrl+D also works)
auto-approve: safe changes run unattended; destructive, out-of-project,
  credential-touching, or uncertain calls still ask (two-tier review)
file references:
  @<path>     attach a project file or directory to the message (Tab completes)
shell:
  !<command>  run it directly (sandboxed, no approval; output is shared with the model)
keys:
  Enter 送信 · ↑↓ 履歴 · Ctrl+C 中断/クリア · Ctrl+D 終了
  改行（複数行入力）: Ctrl+J もしくは 行末に \ を置いて Enter
    ※ Option+Enter は「Option を Meta として送る」設定の端末でのみ有効
      （既定では通常の Enter と同じバイトになり送信されます）
  複数行ペーストはそのまま 1 メッセージになります
  承認ダイアログ: ←→/Tab で選択 · Enter 決定（y/n/a も可）
mutating tools prompt for approval: y = once, a = always this session
`)
	case "/tools":
		for _, t := range registry.List() {
			marker := "read-only"
			if t.Mutating {
				marker = "requires approval"
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

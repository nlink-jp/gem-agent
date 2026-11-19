package cmd

import (
	"sort"

	"github.com/nlink-jp/gem-agent/internal/bounded"

	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/telemetry"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
	"github.com/nlink-jp/gem-agent/internal/workdir"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
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
	flagWritable  bool
	flagReadOnly  bool
	flagAutoRO    bool
	flagAllow     []string
	flagAddDir    []string
	flagReadDir   []string
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
	Long: `gem-agent is a sandboxed CLI agent backed by Vertex AI Gemini: file
read/write, sandboxed shell commands and MCP servers, with mutating calls
asking for your approval. It reads a project's AGENTS.md / CLAUDE.md,
Claude Code-format .mcp.json and skills as they are — no gem-agent-specific
setup.

The current working directory is the project: file tools stay inside it,
and sandboxed shell commands cannot write outside it. --add-dir and
--read-dir add directories outside it, under the same lanes; nothing in
them is loaded.

macOS only. Documentation: README.md.`,
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
	rootCmd.Flags().StringVar(&flagThinking, "thinking", "", "override [model].thinking for this run: minimal|low|medium|high|xhigh, or 'default' to clear a configured level (supported levels are model-dependent)")
	rootCmd.Flags().StringVar(&flagMCP, "mcp", "", "override [mcp].enabled for this run: on|off (off skips every MCP server spawn — useful for -p pipelines)")
	rootCmd.Flags().BoolVar(&flagNoSandbox, "no-sandbox", false, "disable the sandbox-exec wrapper for shell_exec (debugging only, unsafe)")
	rootCmd.Flags().StringVarP(&flagPrompt, "prompt", "p", "", "one-shot: run this prompt and exit; mutating tools are denied unless listed in --allow or --auto is set")
	rootCmd.Flags().BoolVar(&flagAuto, "auto", false, "start in auto-approve mode (required for auto-approve in -p, where [agent].auto_approve is ignored)")
	rootCmd.Flags().StringSliceVar(&flagAllow, "allow", nil, `tools that never ask this run: tool names or mcp__server__* prefixes (repeatable or comma-separated); blocked commands still ask`)
	rootCmd.Flags().BoolVar(&flagWritable, "writable", false, "no lane ceiling — the default, stated; use it to step out of a configured [agent].read_only")
	rootCmd.Flags().BoolVar(&flagReadOnly, "read-only", false, "cap the session at the read lane: nothing outside the session scratch may change")
	rootCmd.Flags().BoolVar(&flagAutoRO, "auto-read-only", false, "let the runtime tighten the ceiling to read-only from what you type; the starting ceiling is unchanged, so pair it with --writable to be sure of starting off (interactive only)")
	rootCmd.Flags().StringArrayVar(&flagAddDir, "add-dir", nil, "add a directory outside the project that the file tools and the write lane may write like the project (repeatable; its AGENTS.md, .mcp.json and skills are not loaded)")
	rootCmd.Flags().StringArrayVar(&flagReadDir, "read-dir", nil, "add a directory outside the project that the file tools and the read lane may read, and nothing may write (repeatable)")
	rootCmd.Flags().BoolVarP(&flagContinue, "continue", "c", false, "resume this project's most recent session")
	rootCmd.Flags().StringVar(&flagResume, "resume", "", "resume a specific session id (see: gem-agent sessions)")
}

const shell = "/bin/bash"

// reloadWarnings keeps a reload report's warnings and drops its
// per-server inventory. `/mcp reload` is a command that asked for the
// inventory; a toggle in the settings panel is not.
func reloadWarnings(report string) string {
	var keep []string
	for _, l := range strings.Split(report, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "warning:") || strings.HasPrefix(t, "note:") {
			keep = append(keep, t)
		}
	}
	return strings.Join(keep, "; ")
}

// workDirNoteFloor is the size a session's leftovers reach before the
// startup note is worth an operator's attention (ADR-0078 §4). Below it
// the directories exist but nothing has accumulated.
const workDirNoteFloor = 10 << 20 // 10 MiB

func runREPL(cmd *cobra.Command, args []string) error {
	// The session's state and the parts its methods call into (ADR-0124
	// §2.3): filled as startup builds them.
	sess := newLiveSession()
	sess.version = cmd.Root().Version
	sess.ctx = cmd.Context()
	if sess.ctx == nil {
		sess.ctx = context.Background()
	}
	// Text from outside the runtime reaches a terminal inert and a pipe
	// verbatim (ADR-0093 §3) — before the first print, so every one after
	// it is covered.
	inertStreams(cmd, terminalWriter)
	// The session's teardown (ADR-0124 §2.5): every part pushes what it
	// set up, and the stack runs once, last pushed first.
	cl := &cleanups{trace: cleanupTrace}
	defer cl.run()
	fl := readFlags()
	// Startup warnings are teed: they hit stderr immediately (plain
	// REPL, one-shot, early failures), but the TUI's first ClearScreen
	// wipes that copy — a broken skill or unreadable memory flashed for
	// milliseconds and vanished (ADR-0021) — so the recorded lines ride
	// the banner too.
	notes := &startupNotes{w: cmd.ErrOrStderr()}
	sess.stderr = notes

	rs, err := resolveRun(sess, cmd, fl, args)
	if err != nil {
		return err
	}
	adm, err := admitProject(sess, cmd, fl, rs)
	if err != nil {
		return err
	}
	op, err := openTranscript(sess, cl, fl)
	if err != nil {
		return err
	}
	wa := prepareWorkDir(sess, cl, op)
	cf, err := buildConfinement(sess, fl, rs, adm, wa)
	if err != nil {
		return err
	}
	ld, err := loadProject(sess, cmd, cl, fl, op)
	if err != nil {
		return err
	}
	md, err := buildModels(sess, cmd, cl, rs)
	if err != nil {
		return err
	}
	fe := chooseFrontEnd(sess, cmd, rs)
	rg, err := registerTools(sess, cmd, rs, cf, ld, md)
	if err != nil {
		return err
	}
	as := assembleSession(sess, cmd, cl, rs, adm, op, ld, md, fe, rg)

	if rs.oneShot {
		return runOneShot(sess, cmd, fl, rs, adm)
	}
	bannerLines, picture := buildBanner(sess, cmd, adm, op, ld, fe)
	if fe.useTUI {
		return runTUI(sess, cmd, cl, notes, rs, adm, ld, md, fe, as, bannerLines, picture)
	}
	return runPlain(sess, cmd, notes, rs, adm, ld, md, fe, as, bannerLines)
}

var errInterrupted = errors.New("interrupted")

// startupNotes tees startup-time stderr lines so the TUI can replay
// them in the banner after its first ClearScreen (ADR-0021). Locked: a
// goroutine the session started (the context-window lookup) can report
// through it while the start still writes — the plain REPL and one-shot
// mode raced on lines and frozen (lagent's pre-release review, measured
// with -race; the same code here).
type startupNotes struct {
	mu     sync.Mutex
	w      io.Writer
	lines  []string
	frozen bool
}

// freeze stops recording and returns what was recorded: the banner has
// been built, and a session-long tee would only grow.
func (s *startupNotes) freeze() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	lines := s.lines
	s.frozen, s.lines = true, nil
	return lines
}

func (s *startupNotes) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	buf, clipped, err := bounded.ReadAll(r, stdinCap)
	if err != nil {
		return "", "stdin read failed, nothing attached: " + err.Error()
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
		return "", fmt.Errorf("cannot combine -p (one turn, then exit) with a first message (interactive session) — pass one or the other")
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

// readOnlyOverride resolves the three ceiling flags to one state, or ""
// when none was given. They name the three states of one axis
// (ADR-0080 §1), so passing two is a contradiction rather than a
// precedence puzzle. The auto state is interactive-only: a one-shot
// run's ceiling is answered where the run is launched, never inferred
// from the prompt's wording, or a scheduled job's write access would
// become a function of that text (§2).
func readOnlyOverride(writable, readOnly, autoRO, oneShot bool) (ceiling, auto string, err error) {
	// --writable and --read-only are the two ends of one setting.
	// --auto-read-only is a different setting, so it composes with
	// either: "start writable, and watch" is a sentence.
	if writable && readOnly {
		return "", "", errors.New("--writable and --read-only are the two ends of one setting; pass at most one")
	}
	if autoRO && oneShot {
		return "", "", errors.New("--auto-read-only needs a session to watch; in -p pass --read-only, or --writable to say so")
	}
	switch {
	case readOnly:
		ceiling = "on"
	case writable:
		// Both bits. "This run is writable" has to include "and is not
		// watched", or a configured watcher re-tightens what the flag
		// just released — and in -p, where the watcher is folded into
		// the ceiling, --writable left the run read-only with no
		// command-line escape at all (independent review, 2026-09-09).
		ceiling, auto = "off", "off"
	}
	if autoRO {
		auto = "on" // explicit, so it wins over --writable's implied off
	}
	return ceiling, auto, nil
}

// effectiveCeiling is the ceiling and watcher the run actually starts
// with. They are independent settings, so they resolve independently —
// except in one-shot, where there is no watcher to arm.
//
// A configured watcher is read there as a ceiling instead. Of the two
// available readings only that one errs the way the rest of ADR-0080
// errs: dropping it would lose a restriction the operator asked for,
// silently, in the one context with nobody watching. Turning the
// ceiling on refuses the write instead and forces the run that wants it
// to say --writable on its own command line — which is where ADR-0053
// §1 requires a grant to be visible.
// It takes the whole [agent] section rather than two bools: the two are
// the same type and adjacent, so a call site could swap them and every
// test in the tree would still pass (independent review, 2026-09-09).
func effectiveCeiling(a config.AgentConfig, oneShot bool) sandbox.Ceiling {
	if oneShot {
		return sandbox.Ceiling{ReadOnly: a.ReadOnly || a.ReadOnlyAuto}
	}
	return sandbox.Ceiling{ReadOnly: a.ReadOnly, Auto: a.ReadOnlyAuto}
}

// ApproveOnce refuses like Approve: the answer set differs, the
// absence of a human does not.
func (d denyGate) ApproveOnce(toolName, detail, _, reason string) (bool, string) {
	fmt.Fprintf(d.out, "[denied: %s %s — %s]\n", toolName, agent.ClipDetail(detail), reason)
	return false, ""
}

// ApproveLift refuses in one-shot: there is nobody to ask, so the
// ceiling holds and the reason goes to stderr like every other denial
// here (ADR-0080 §4). --read-only is how a run says it meant this.
func (d denyGate) ApproveLift(toolName, detail, _, reason string) (bool, string) {
	fmt.Fprintf(d.out, "[denied: %s %s — %s]\n", toolName, agent.ClipDetail(detail), reason)
	return false, ""
}

// denyGate is the one-shot approver: it denies every mutating call with
// a visible reason instead of blocking on an approval prompt that
// nothing will answer.
type denyGate struct{ out io.Writer }

func (d denyGate) Approve(toolName, detail, purpose, reason string, mustPrompt bool) (bool, bool, string) {
	why := "mutating tools are disabled in one-shot mode; approve interactively, grant with --allow, or arm the risk ladder with --auto"
	if reason != "" {
		// The ladder or the rule tier said why this call needs a human;
		// with no human here, that reason is the denial's story
		// (ADR-0053 §3).
		why = reason + " — nobody to ask in one-shot mode"
	}
	fmt.Fprintf(d.out, "[denied: %s %s — %s]\n", toolName, agent.ClipDetail(detail), why)
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

// buildExecFn returns the shell execution strategy and what could be
// established about it (ADR-0073 §5): with the sandbox on, one profile
// per lane and a read lane enabled only when VerifyReadLane passed on
// this machine (notes say why when it did not); with the sandbox off,
// direct bash and Confined=false — the unconfined mode the agent gates
// as the operator's alone. A probe file the probes could not remove is
// a note on success and part of the error otherwise: every caller of
// this form reports to the operator. The grant path, whose error may be
// the model's to read, takes buildLanes and routes leftovers itself.
func buildExecFn(sandboxOn bool, projectDir, workDir string, denyExec []string, caches map[string]string, persistentParents []string, added []sandbox.AddedRoot) (tools.LaneExecFunc, sandbox.Enforcement, []string, error) {
	fn, enf, notes, left, err := buildLanes(sandboxOn, projectDir, workDir, denyExec, caches, persistentParents, added)
	if len(left) > 0 {
		if err != nil {
			err = fmt.Errorf("%w; %s", err, sandbox.LeftoverNote(left))
		} else {
			notes = append(notes, sandbox.LeftoverNote(left))
		}
	}
	return fn, enf, notes, err
}

// buildLanes is buildExecFn with the probes' leftovers (ADR-0107 §4)
// apart from its notes, returned whatever the result.
func buildLanes(sandboxOn bool, projectDir, workDir string, denyExec []string, caches map[string]string, persistentParents []string, added []sandbox.AddedRoot) (_ tools.LaneExecFunc, _ sandbox.Enforcement, _ []string, leftovers []string, _ error) {
	if !sandboxOn {
		return func(ctx context.Context, command string, lane sandbox.Lane) *exec.Cmd {
			cmd := exec.CommandContext(ctx, shell, "-c", command)
			// Unconfined is a mode, not an exemption (ADR-0087 §2,
			// amended): this child is one this runtime spawns, so the
			// runtime's own configuration variables do not reach it
			// either. There is no scratch here, so no temporary
			// directory and no toolchain cache to point into one.
			cmd.Env = laneEnv(lane, "", "", nil, os.Environ())
			return cmd
		}, sandbox.Enforcement{}, nil, nil, nil
	}
	if _, err := os.Stat(sandbox.Executable); err != nil {
		return nil, sandbox.Enforcement{}, nil, nil, fmt.Errorf("%s not found (gem-agent is macOS-only); use --no-sandbox to bypass at your own risk", sandbox.Executable)
	}
	// The profile's anchors must be the path the kernel reports —
	// symlinks resolved (/var → /private/var): a regex anchored on the
	// unresolved spelling protects nothing, which the verification below
	// would then report as an unconfined write lane.
	//
	// Each is the first answer its name gave in this process, never a
	// later one (ADR-0122 §2): the lanes are built more than once, a
	// command in the write lane can replace its work directory — or a
	// project it can empty — by a link, and a name resolved again here
	// anchored the profile on wherever the link led.
	if real, err := sandbox.ResolveKept(projectDir); err == nil {
		projectDir = real
	}
	spec := sandbox.Spec{ProjectDir: projectDir, DenyExec: append(append([]string{}, sandbox.DefaultDenyExec...), denyExec...), PersistentParents: persistentParents, Added: added}
	if workDir != "" {
		// The session work directory (ADR-0058). A shell command told to
		// put its output in $GEMAGENT_WORK_DIR has to be able to.
		if resolved, err := sandbox.ResolveKept(workDir); err == nil {
			spec.WorkDir = resolved
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		// The kernel's spelling, as the project and work directory above:
		// Seatbelt matches the real path, so a ~/Library denial or a
		// credential filter anchored on a symlinked home matches nothing,
		// and the re-allow check would not see a root covering it (ADR-0102).
		spec.Home = home
		if real, err := sandbox.ResolveWriteDir(home); err == nil {
			spec.Home = real
		}
	}
	var notes []string
	// The read lane's private scratch: under the work directory when
	// there is one, a fresh temporary directory otherwise. TMPDIR points
	// there for read-lane commands, so what such a command may change
	// is exactly this directory and the device sinks.
	if scratch, err := readScratchDir(spec.WorkDir); err != nil {
		notes = append(notes, fmt.Sprintf("read lane disabled: no private scratch directory (%v); read-lane commands need approval too", err))
	} else {
		spec.ReadScratch = scratch
		if approved, err := approvedCacheDir(spec.WorkDir); err != nil {
			notes = append(notes, fmt.Sprintf("the approved lanes' toolchain caches stay unredirected: %v", err))
		} else {
			spec.ApprovedCacheRoot = approved
		}
	}
	profiles := map[sandbox.Lane]string{}
	for _, lane := range []sandbox.Lane{sandbox.LaneRead, sandbox.LaneWrite, sandbox.LaneOperator} {
		p, err := sandbox.LaneProfile(lane, spec)
		if err != nil {
			return nil, sandbox.Enforcement{}, nil, nil, err
		}
		profiles[lane] = p
	}
	enf := sandbox.Enforcement{Confined: true, ReadLane: spec.ReadScratch != ""}
	if enf.ReadLane {
		// The claim is checked, not assumed: a read-lane call runs
		// unasked only where the kernel demonstrably denies what the
		// lane says it denies.
		readLeft, err := sandbox.VerifyReadLane(profiles[sandbox.LaneRead], spec)
		leftovers = append(leftovers, readLeft...)
		if err != nil {
			enf.ReadLane = false
			notes = append(notes, fmt.Sprintf("read lane disabled: %v; read-lane commands need approval too", err))
		}
	}
	// Confinement itself is measured, not assumed (ADR-0073 §7): where
	// the write lane's denials cannot be confirmed — a sandbox-exec that
	// applies no cage, a build that stubbed it — the session runs as
	// unconfined, and every shell command is the operator's to answer.
	// The commands stay wrapped: a cage that half-works is still in
	// the way.
	writeLeft, err := sandbox.VerifyWriteLane(profiles[sandbox.LaneWrite], spec)
	leftovers = append(leftovers, writeLeft...)
	if err != nil {
		enf = sandbox.Enforcement{}
		notes = append(notes, fmt.Sprintf("sandbox unverified: %v — shell commands run as unconfined: every one asks you", err))
	}
	if enf.Confined && len(added) > 0 {
		// The startup probes prove the cage, not where an added root
		// sits in it (ADR-0099 §7). A root that fails its probes is an
		// error, not a degraded mode: the operator named it.
		rootNotes, rootLeft, err := sandbox.VerifyAddedRoots(profiles, spec, enf.ReadLane)
		leftovers = append(leftovers, rootLeft...)
		if err != nil {
			return nil, sandbox.Enforcement{}, nil, leftovers, err
		}
		notes = append(notes, rootNotes...)
	}
	scratch, approved := spec.ReadScratch, spec.ApprovedCacheRoot
	return func(ctx context.Context, command string, lane sandbox.Lane) *exec.Cmd {
		argv := sandbox.Wrap(profiles[lane], shell, command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = laneEnv(lane, scratch, approved, caches, os.Environ())
		return cmd
	}, enf, notes, leftovers, nil
}

// laneEnv is the environment a shell command runs with. Every lane
// gets the parent's environment minus the runtime's own configuration
// variables (ADR-0087): the operator's exported variables are not
// filtered, because which one the program a command runs needs is not
// a question this runtime can answer. The read lane's temporary
// directory is the private scratch, and every lane's toolchain caches
// are pointed into it or, for the approved lanes, into approved
// (ADR-0084 §1, ADR-0109): no lane may write a cache under ~/Library,
// and the read lane must not write a shared cache anyway.
func laneEnv(lane sandbox.Lane, scratch, approved string, caches map[string]string, parent []string) []string {
	// The runtime's own configuration variables go to no child
	// (ADR-0087 §2); the operator's environment is not touched.
	env := sandbox.ChildEnv(parent)
	if lane == sandbox.LaneRead {
		if scratch != "" {
			env = append(env, "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
		}
	}
	if scratch != "" {
		env = append(env, toolchainCacheEnv(scratch, approved, lane, caches)...)
	}
	return env
}

// toolchainCacheEnv renders the scratch-cache table (ADR-0084 §1,
// `[sandbox].scratch_caches`) as environment assignments: each
// variable points at its directory under the lane's cache root. The
// table is the operator's; Go's build cache is the shipped row.
//
// The unasked lane and the approved lanes get separate roots. A build
// cache is content-addressed and trusted on read, so a directory an
// unasked writer can reach would let a command steered by what it read
// plant an object that an approved build later links and the operator
// then runs. The read lane's root is the session scratch; write and
// operator share approved, which the kernel denies the read lane and
// which lies outside the work directory the file tools write
// (ADR-0109). With no approved directory those lanes get no redirection.
func toolchainCacheEnv(scratch, approved string, lane sandbox.Lane, caches map[string]string) []string {
	names := make([]string, 0, len(caches))
	for name := range caches {
		names = append(names, name)
	}
	sort.Strings(names)
	root := scratch
	if lane != sandbox.LaneRead {
		root = approved
	}
	if root == "" {
		return nil
	}
	env := make([]string, 0, len(names))
	for _, name := range names {
		env = append(env, name+"="+filepath.Join(root, caches[name]))
	}
	return env
}

// readScratchDir creates the read lane's private scratch directory:
// <workDir>/scratch, or a fresh temporary directory when the session
// has no work directory.
func readScratchDir(workDir string) (string, error) {
	if workDir != "" {
		// A work directory that has become a link gets no scratch: made
		// by path, it would be made where the link leads (ADR-0122 §2).
		if fi, err := os.Lstat(workDir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%s is a link", workDir)
		}
		dir := filepath.Join(workDir, "scratch")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		// The write lane can write the work directory and a resume
		// reuses it: a link here would become the read lane's writable
		// directory, wherever it leads.
		fi, err := os.Lstat(dir)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("%s is not a directory", dir)
		}
		return sandbox.ResolveWriteDir(dir)
	}
	// One fallback per process, reused across /clear and removed at
	// exit (review F-08/A-12: a new one per rotation leaked).
	fallbackScratchMu.Lock()
	defer fallbackScratchMu.Unlock()
	if fallbackScratch != "" {
		return fallbackScratch, nil
	}
	dir, err := os.MkdirTemp("", "gem-agent-read-scratch-")
	if err != nil {
		return "", err
	}
	resolved, err := sandbox.ResolveWriteDir(dir)
	if err != nil {
		return "", err
	}
	fallbackScratch = resolved
	return resolved, nil
}

var (
	fallbackScratchMu sync.Mutex
	fallbackScratch   string
)

// approvedCacheDir creates the session's directory for the approved
// lanes' toolchain caches, beside its work directory (ADR-0109), and
// returns its real path. A session with no work directory has none.
func approvedCacheDir(workDir string) (string, error) {
	dir, err := workdir.EnsureApprovedCache(workDir)
	if err != nil || dir == "" {
		return "", err
	}
	return sandbox.ResolveWriteDir(dir)
}

// removeFallbackScratch deletes the process's fallback scratch
// directory, if one was created — a directory this process made under
// its own temporary root, named by its full path.
func removeFallbackScratch() {
	fallbackScratchMu.Lock()
	defer fallbackScratchMu.Unlock()
	if fallbackScratch != "" && strings.Contains(filepath.Base(fallbackScratch), "gem-agent-read-scratch-") {
		_ = os.RemoveAll(fallbackScratch)
		fallbackScratch = ""
	}
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
// after, when non-nil, runs after the command and returns a note for the
// output: the operator's own shell may have changed the files later
// sessions trust, and the command line does not show that — the
// difference is named, the pins are not moved (ADR-0074 §1). The session
// passes it; it was a package variable the session set and cleared
// (ADR-0124 §2.1).
func runDirectShell(ctx context.Context, registry *tools.Registry, ag *agent.Agent, command string, after func() string) string {
	tool, ok := registry.Get("shell_exec")
	if !ok {
		return "error: shell_exec is unavailable"
	}
	// The operator typed it: the operator lane (ADR-0073), as the
	// operator's own shell would be.
	out, err := tool.Run(ctx, map[string]any{"command": command, "access": sandbox.LaneOperator.String()})
	if err != nil {
		out = "error: " + err.Error()
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	if after != nil {
		if n := after(); n != "" {
			out += "\n[⚠ " + n + "]"
		}
	}
	// The prefix is a shared constant: the session listing uses it to
	// tell an injected message from one the operator typed. The command
	// is the operator's; what it printed is data (ADR-0111).
	ag.AddContextData(session.ShellContextPrefix+"\n$ "+command, "!", "shell output", out)
	return out
}

// slashCompletions returns the Tab-completion source for "/"-prefixed
// input: command names, and skill names after "/skill ". The command
// list mirrors what slashOutput and the REPL loop actually accept.
// slashCompletions reads the skill list through the getter on every
// call: /skills reload swaps the list mid-session (ADR-0039).
func slashCompletions(getSkills func() []skills.Skill) func(string) []string {
	commands := []string{
		"/add-dir", "/auto", "/clear", "/compact", "/dirs", "/exit", "/help", "/mcp", "/memory",
		"/quit", "/read-dir", "/readonly", "/riskbook", "/settings", "/show", "/skill", "/skills",
		"/thinking", "/tools", "/usage", "/version",
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

// mcpToolCount counts the MCP tools this session actually declares.
func mcpToolCount(registry *tools.Registry) int {
	n := 0
	for _, t := range registry.List() {
		if strings.HasPrefix(t.Name, "mcp__") {
			n++
		}
	}
	return n
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

// newShowPath is /show: the path the operator typed, to the screen. It takes
// the path as a PATH (mention.Image) — the first version wrapped it in "@" and
// handed it to the text grammar, which cut it at the first space.
func newShowPath(projectDir string, show func(data []byte, mime string) error) func(string) (string, bool) {
	return func(path string) (string, bool) {
		att, err := mention.Image(strings.TrimPrefix(strings.TrimSpace(path), "@"), projectDir, mention.DefaultLimits())
		if err != nil {
			return fmt.Sprintf("not shown: %v", err), true
		}
		if err := show(att.Data, att.MIME); err != nil {
			return fmt.Sprintf("not shown: %v", err), true
		}
		return "", false // the picture is the output
	}
}

// resolveImages asks the terminal what inline-image protocol it can draw
// with, ONCE, here — before tea.NewProgram (ADR-0089 §7). The reason is
// raw-mode stdin ownership: once Bubble Tea owns stdin, a terminal's reply
// to a query arrives in the input box as phantom keystrokes, which is the
// same hazard that keeps WithAutoStyle out of the Markdown renderer. The
// probe needs a terminal it can read back, so it opens /dev/tty rather
// than trusting stdin to be one; no /dev/tty means no capability, and the
// fallback is what this runtime does today, which is not to draw.
func resolveImages(configured string) termimg.Protocol {
	if configured == "off" {
		return termimg.None // never open anything for a session that will not draw
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return termimg.Resolve(configured, nil, os.Getenv, 0)
	}
	defer func() { _ = tty.Close() }()
	return termimg.Resolve(configured, tty, os.Getenv, 2*time.Second)
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
	// mcpLoad is /mcp load <server> (ADR-0083 §6, the operator's
	// override); mcpStatus and mcpWithheld decorate /mcp's listing.
	// All three may be nil.
	mcpLoad     func(server string) string
	mcpStatus   func(server string) string
	mcpWithheld func() []WithheldTool
	// show draws an image the OPERATOR named (/show <path>, ADR-0091
	// §3). It is here rather than in the tools because the operator
	// typed the path: that is the trust line ADR-0012 already draws for
	// `@<image>`, and it is why the symlink hazard that rules out a
	// runtime-chosen path does not apply.
	show func(path string) (string, bool)
	// thinking is /thinking (ADR-0098): the args after the command, the
	// output, and whether it is an error. nil reads as unknown.
	thinking func(args []string) (string, bool)
	// addDir is /add-dir and /read-dir (ADR-0099 §11): the rest of the
	// line is the path. dirs is /dirs (§8). nil reads as unknown.
	addDir func(rest string, write bool) (string, bool)
	dirs   func() string
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
	if fields[0] == "/mcp" && sub == "load" {
		if len(fields) < 3 || reload.mcpLoad == nil {
			fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
			return b.String(), true, false
		}
		return reload.mcpLoad(fields[2]), false, false
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
	if fields[0] == "/show" {
		if reload.show == nil {
			fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
			return b.String(), true, false
		}
		if len(fields) < 2 {
			return "/show <path> — the image to draw in this terminal", true, false
		}
		// Everything after the command is the path: an operator's path
		// may hold spaces, and splitting it into "unknown command" would
		// be the least useful thing this could do.
		out, isErr := reload.show(strings.TrimSpace(strings.TrimPrefix(input, fields[0])))
		return out, isErr, false
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
		// on|off as well as toggling, the same grammar /readonly uses.
		// The argument used to be ignored, so `/auto on` toggled — and
		// could turn auto OFF while the line it printed said ON
		// (operator report). In the TUI this case is unreachable: the
		// model intercepts /auto so the footer cannot go stale.
		switch sub {
		case "":
			ag.SetAutoApprove(!ag.AutoApprove())
		case "on":
			ag.SetAutoApprove(true)
		case "off":
			ag.SetAutoApprove(false)
		default:
			b.WriteString(msgs.AutoUsage)
			return b.String(), true, false
		}
		if ag.AutoApprove() {
			b.WriteString(msgs.AutoOn)
		} else {
			b.WriteString(msgs.AutoOff)
		}
	case "/readonly":
		// Two independent settings (ADR-0080 §1): the ceiling in force,
		// and the watcher that may raise it. `on`/`off` move the first,
		// `auto on`/`auto off` the second, and neither touches the
		// other — a lift that disarmed the watcher would take away
		// something the operator never asked to give up. Nothing in the
		// runtime lowers the ceiling; this is where they do.
		arg2 := ""
		if len(fields) > 2 {
			arg2 = fields[2]
		}
		// A typo is an error here exactly as it is for /auto, which
		// shares this grammar: the TUI dims an errored line and the
		// plain REPL's caller can tell a rejection from a report. The
		// two disagreed, and a test pinned the disagreement in place
		// (independent review).
		bad := false
		switch {
		case len(fields) > 3:
			// Trailing words are a typo, in every form. The `auto`
			// branch used to match on arg2 alone, so `/readonly auto on
			// nonsense` armed the watcher silently.
			b.WriteString(msgs.ReadOnlyUsage)
			bad = true
		case sub == "":
		case (sub == "on" || sub == "off") && arg2 == "":
			ag.SetReadOnly(sub == "on", "operator")
		case sub == "auto" && (arg2 == "" || arg2 == "on"):
			ag.SetReadOnlyAuto(true, "operator")
		case sub == "auto" && arg2 == "off":
			ag.SetReadOnlyAuto(false, "operator")
		default:
			b.WriteString(msgs.ReadOnlyUsage)
			bad = true
		}
		if bad {
			return b.String(), true, false
		}
		if b.Len() == 0 {
			c := ag.CeilingState()
			// Same reservation the banner makes: with the lanes off the
			// ceiling reaches less, and the state line must not promise
			// what only the lanes give (independent review). A nil
			// registry is the tests' agent-only fixture.
			confined := registry == nil || registry.Confined()
			switch {
			case c.ReadOnly && !confined:
				b.WriteString(msgs.ReadOnlyOnUnconfined)
			case c.ReadOnly:
				b.WriteString(msgs.ReadOnlyOn)
			default:
				b.WriteString(msgs.ReadOnlyOff)
			}
			// Only when armed: off is the default and the line would say
			// nothing the absence does not.
			if c.Auto {
				b.WriteString(msgs.ReadOnlyAuto)
			}
		}
	case "/add-dir", "/read-dir", "/dirs":
		if reload.addDir == nil || reload.dirs == nil {
			fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
			return b.String(), true, false
		}
		// The rest of the line is the path, so a path with spaces needs
		// no quoting; bare, each lists the roots (§11's grammar).
		rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), fields[0]))
		if fields[0] == "/dirs" || rest == "" {
			return reload.dirs(), false, false
		}
		out, isErr := reload.addDir(rest, fields[0] == "/add-dir")
		return out, isErr, false
	case "/thinking":
		if reload.thinking == nil {
			fmt.Fprintf(&b, msgs.UnknownCommandFmt, input)
			return b.String(), true, false
		}
		out, isErr := reload.thinking(fields[1:])
		return out, isErr, false
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
				// What the model can see of the server, under
				// on-request (ADR-0083); nothing to add under "all".
				if reload.mcpStatus != nil {
					if st := reload.mcpStatus(strings.Fields(s)[0]); st != "" {
						s += " — " + st
					}
				}
				b.WriteString("  " + s + "\n")
			}
			if reload.mcpWithheld != nil {
				for _, w := range reload.mcpWithheld() {
					fmt.Fprintf(&b, msgs.MCPWithheldFmt, w.Name, w.Why)
				}
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
	fn tools.LaneExecFunc
}

func (e *liveExec) run(ctx context.Context, command string, lane sandbox.Lane) *exec.Cmd {
	e.mu.RLock()
	fn := e.fn
	e.mu.RUnlock()
	return fn(ctx, command, lane)
}

func (e *liveExec) set(fn tools.LaneExecFunc) {
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
// onDropped hears the added roots the rebuild had to leave out, after
// the registry has dropped them (ADR-0114 §2).
func rotateWorkDir(registry *tools.Registry, shellExec *liveExec, sandboxOn bool, projectDir, dir string, denyExec []string, caches map[string]string, readLanePrompts bool, persistentParents []string, added []sandbox.AddedRoot, setSystem func(), onDropped func(dirs []string)) []string {
	var notes []string
	if err := registry.UseWorkDir(dir); err != nil {
		notes = append(notes, fmt.Sprintf("file tools keep the previous work directory: %v", err))
	}
	fn, enf, laneNotes, dropped, err := buildWithoutFailedRoots(added, func(roots []sandbox.AddedRoot) (tools.LaneExecFunc, sandbox.Enforcement, []string, error) {
		return buildExecFn(sandboxOn, projectDir, dir, denyExec, caches, persistentParents, roots)
	})
	if err != nil {
		notes = append(notes, fmt.Sprintf("shell commands keep the previous sandbox profile: %v", err))
	} else {
		shellExec.set(fn)
		if readLanePrompts {
			enf.ReadLane = false
		}
		registry.SetLaneExec(shellExec.run, enf)
		notes = append(notes, laneNotes...)
		if len(dropped) > 0 {
			registry.DropAddedDirs(dropped)
			if onDropped != nil {
				onDropped(dropped)
			}
		}
	}
	if setSystem != nil {
		setSystem()
	}
	return notes
}

// buildWithoutFailedRoots builds the lanes over added, leaving out each
// root that fails its probes: the file tools have already moved to the
// new work directory, so the lanes must follow it, and one ejected
// volume must not take the other roots with it (ADR-0114 §2). Each
// failure removes a root, so this builds at most once per root and once
// more. A failure that names no root is returned, and then nothing is
// dropped: the previous profile, which still names every root, stays.
func buildWithoutFailedRoots(added []sandbox.AddedRoot, build func([]sandbox.AddedRoot) (tools.LaneExecFunc, sandbox.Enforcement, []string, error)) (tools.LaneExecFunc, sandbox.Enforcement, []string, []string, error) {
	kept := append([]sandbox.AddedRoot(nil), added...)
	var dropped, dropNotes []string
	fn, enf, notes, err := build(kept)
	for range added {
		var bad *sandbox.AddedRootError
		if !errors.As(err, &bad) {
			break
		}
		dropped = append(dropped, bad.Root.Dir)
		// The command that restores its level: a read root brought back
		// with /add-dir would become writable.
		again := "/read-dir"
		if bad.Root.Write {
			again = "/add-dir"
		}
		dropNotes = append(dropNotes, fmt.Sprintf("removed from this session, because it no longer passes the sandbox check: %v — %s %s adds it again", err, again, bad.Root.Dir))
		kept = slices.DeleteFunc(kept, func(r sandbox.AddedRoot) bool { return r.Dir == bad.Root.Dir })
		fn, enf, notes, err = build(kept)
	}
	if err != nil {
		return nil, sandbox.Enforcement{}, nil, nil, err
	}
	return fn, enf, append(dropNotes, notes...), dropped, nil
}

// runFlags is the root command's flag values, read once at the top of
// runREPL (ADR-0124 §2.1): the session reads this, never the globals
// cobra binds.
type runFlags struct {
	config, model, thinking, mcp, prompt, resume string
	noSandbox, continueLast, auto                bool
	writable, readOnly, autoRO                   bool
	allow, addDir, readDir                       []string
}

func readFlags() runFlags {
	return runFlags{
		config: flagConfig, model: flagModel, thinking: flagThinking, mcp: flagMCP,
		prompt: flagPrompt, resume: flagResume,
		noSandbox: flagNoSandbox, continueLast: flagContinue, auto: flagAuto,
		writable: flagWritable, readOnly: flagReadOnly, autoRO: flagAutoRO,
		allow: flagAllow, addDir: flagAddDir, readDir: flagReadDir,
	}
}

package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"

	"github.com/nlink-jp/gem-agent/internal/uitext"
)

type phase int

const (
	phaseInput phase = iota
	phaseRunning
	phaseApproval
	phaseSettings
	phaseAsk
)

const (
	maxInputHeight = 6
	liveTailLines  = 12
	// Floors for a terminal that reports a bogus size.
	minWidth  = 20
	minHeight = 4
)

// approvalAnswers are the approval dialog's selectable answers, in
// display order. The index is the model's `choice`; the labels come
// from the language catalog (ADR-0029). Persisting ('p') is
// deliberately a separate answer from 'a': one is a session
// convenience, the other edits a file on disk (ADR-0009 §5).
var approvalAnswers = []byte{'y', 'n', 'a', 'p'}

func (m Model) approvalLabels() []string {
	return []string{m.msgs.ApproveAllow, m.msgs.ApproveDeny, m.msgs.ApproveAlways, m.msgs.ApprovePersist}
}

const (
	choiceAllow = 0
	choiceDeny  = 1
)

// styleSet holds the chrome styles. Accent colors use the ANSI-16
// palette (they follow the terminal theme). Dim text is harder:
// the Faint attribute and ANSI color 8 both render near-invisible on
// real themes (measured on the operator's terminal), so dim uses a
// fixed 256-palette mid-gray picked by the pre-detected background —
// 245 on dark, 240 on light — which keeps a real luminance gap to any
// background. Never lipgloss.AdaptiveColor here: it lazily queries the
// terminal at render time (the OSC leak).
type styleSet struct {
	user     lipgloss.Style
	tool     lipgloss.Style
	warn     lipgloss.Style
	errS     lipgloss.Style
	hint     lipgloss.Style
	status   lipgloss.Style
	selected lipgloss.Style
	box      lipgloss.Style
}

func defaultStyles(darkBackground bool) styleSet {
	dim := lipgloss.Color("240") // readable dark gray on light backgrounds
	if darkBackground {
		dim = lipgloss.Color("245") // readable light gray on dark backgrounds
	}
	return styleSet{
		user:     lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		tool:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Bold(true),
		errS:     lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		hint:     lipgloss.NewStyle().Foreground(dim),
		status:   lipgloss.NewStyle().Foreground(dim).Italic(true),
		selected: lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		box: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("3")).Padding(0, 1),
	}
}

// plainStyles renders everything in the default foreground — the
// [tui].theme = "plain" escape hatch for terminal themes that fight
// any styling. Errors keep their "✗" prefix, so nothing depends on
// color alone.
func plainStyles() styleSet {
	plain := lipgloss.NewStyle()
	return styleSet{
		user: plain, tool: plain, warn: plain, errS: plain, hint: plain, status: plain,
		selected: lipgloss.NewStyle().Bold(true),
		box:      lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1),
	}
}

// TurnStarter launches one agent turn in a goroutine. It must not
// block; completion arrives as a TurnDone message.
type TurnStarter func(ctx context.Context, input string)

// ShellStarter launches one direct (!-prefixed) shell command in a
// goroutine. It must not block; completion arrives as a ShellDone
// message.
type ShellStarter func(ctx context.Context, command string)

// CompactStarter launches history compaction in a goroutine. Like a
// turn it must not block: it makes an LLM call, and the UI has to stay
// interruptible while it runs. Completion arrives as TurnDone.
type CompactStarter func(ctx context.Context)

// RiskbookLearn launches a /riskbook learn pass in a goroutine
// (ADR-0050). Like compaction it must not block: it makes an LLM call
// and drives a dialog answered on this event loop — running it inline
// would deadlock on the first question. Completion arrives as TurnDone.
type RiskbookLearn func(ctx context.Context)

// SlashHandler executes a /command and returns its output, whether the
// output is an error (rendered so it stands out — an unknown command
// must never look like dim meta text), and whether the program should
// quit.
type SlashHandler func(cmd string) (output string, isErr bool, quit bool)

// Options configures the model.
type Options struct {
	StartTurn TurnStarter
	Shell     ShellStarter
	Compact   CompactStarter
	Riskbook  RiskbookLearn
	Slash     SlashHandler
	BaseCtx   context.Context
	// Msgs is the resolved language catalog (ADR-0029); nil means
	// English.
	Msgs *uitext.Messages
	// Theme is "dark", "light", or "notty" (plain: no colors anywhere).
	// It MUST be decided by the caller BEFORE the Bubble Tea program
	// starts: background detection sends an OSC query, and once raw
	// mode owns stdin the terminal's "rgb:..." reply would leak into
	// the input box as if the user typed it.
	Theme string
	// ModelName and ProjectDir feed the persistent footer.
	ModelName  string
	ProjectDir string
	// Banner lines are printed by the TUI itself right after the
	// startup screen clear — they must go through the line counter or
	// the bottom pinning (ADR-0003) would drift from frame one.
	Banner []string
	// AutoMode is the initial auto-approve state; ToggleAuto flips it
	// (shift+tab) and returns the new state.
	AutoMode   bool
	ToggleAuto func() bool
	// CompletePath returns candidate project paths for an @-reference
	// prefix (Tab completion in the input box).
	CompletePath func(prefix string) []string
	// CompleteSlash returns candidate completions for an input that
	// starts with "/" — command names, and skill names after "/skill ".
	CompleteSlash func(prefix string) []string
	// Settings supplies the panel's initial content, and ApplySetting
	// stores one edit and returns the refreshed content (ADR-0009).
	// Both nil disables /settings (the plain REPL prints a table).
	Settings     *SettingsData
	ApplySetting SettingsApplier
	// ExpandInput rewrites an input line into the text of a turn before
	// it is sent — the /skill route (ADR-0010): the operator's line is
	// echoed, the expanded text is what actually runs. handled=false
	// leaves the input to the normal paths; a non-empty errMsg means
	// handled but nothing to run.
	ExpandInput func(input string) (turn string, handled bool, errMsg string)
	// Printer overrides tea.Println for tests.
	Printer func(...any) tea.Cmd
	// RenderFactory overrides the Markdown renderer factory for tests.
	// The factory is re-invoked on resize; it must never query the
	// terminal (see DarkBackground).
	RenderFactory func(width int) func(string) string
}

// Model is the Bubble Tea model for the interactive session.
type Model struct {
	ta   textarea.Model
	spin spinner.Model
	// msgs is the resolved language catalog (ADR-0029); never nil.
	msgs *uitext.Messages

	phase   phase
	history []string
	histIdx int // -1 = not navigating (the guard the org lesson demands)
	draft   string

	// live is a pointer on purpose: Bubble Tea passes the model BY VALUE
	// through every Update, and a non-zero strings.Builder held by value
	// panics on the second WriteString after a copy ("illegal use of
	// non-zero Builder copied by value"). Found live: the second stream
	// chunk of the first real conversation crashed the program.
	live   *strings.Builder
	status string
	// pending holds a message typed and entered while a turn was running
	// (ADR-0007). It is sent when the turn finishes cleanly, and handed
	// back to the input box unsent when it does not.
	pending  string
	approval *ApprovalRequest
	// hold is the bottom-hold render state (ADR-0024): once the screen
	// is full, the frame's total height is held steady so the footer
	// stops moving when the view shrinks (flush resets, dialog closes).
	// A pointer, like live: View runs on a copy of the model, and this
	// bookkeeping must survive it.
	hold *bottomHold
	// approvalAt is when the dialog appeared. Keys arriving within the
	// grace window are dropped: the operator types during runs
	// (ADR-0007), so an Enter or a letter aimed at the input box can
	// land one message behind the dialog and answer it — 'a' would even
	// session-allowlist the tool (ADR-0021).
	approvalAt time.Time

	// Settings panel (ADR-0009). settingsData is the caller-supplied
	// snapshot used to open the panel; settings is the live copy.
	settingsData   *SettingsData
	settings       *SettingsData
	settingsCursor int
	settingsScope  string
	applySetting   SettingsApplier
	expandInput    func(input string) (string, bool, string)
	// choice indexes approvalOptions. Selection + Enter exists because
	// typing y/n/a is impossible with a Japanese IME switched on — the
	// letters are swallowed by composition — while arrows, Tab, and
	// Enter reach the app untouched when nothing is being composed.
	choice int

	startTurn       TurnStarter
	shell           ShellStarter
	compact         CompactStarter
	riskbook        RiskbookLearn
	slash           SlashHandler
	toggleAuto      func() bool
	autoMode        bool
	completePath    func(prefix string) []string
	completeSlashFn func(prefix string) []string
	baseCtx         context.Context
	cancelTurn      context.CancelFunc
	// ask is the pending ask_user dialog (ADR-0036).
	ask       *AskRequest
	askChoice int
	askAt     time.Time

	// interruptSent: Ctrl+C fired for the running turn; gate requests
	// arriving before TurnDone are auto-denied (review round 2).
	interruptSent bool
	// interruptPresses counts Ctrl+C AFTER interruptSent: 1 warns
	// that the next quits, 2 quits (ADR-0034 §3).
	interruptPresses int

	// Turn observability (ADR-0033): stream heartbeat + live thoughts.
	turnStart   time.Time
	chunkCount  int
	lastChunk   time.Time
	retryLine   string
	thoughtTail string
	// toolRunning: a tool call is executing, so the stream is silent
	// BY DESIGN — the stall warning must not cry wolf (ADR-0034
	// follow-up). Cleared when the stream speaks again.
	toolRunning bool

	width    int
	height   int
	sized    bool // first WindowSizeMsg received
	banner   []string
	st       styleSet
	render   func(string) string
	mkRender func(width int) func(string) string
	println  func(...any) tea.Cmd

	// Footer state.
	modelName     string
	projectDir    string
	ctxTokens     int // last round's prompt+output ≈ current context size
	usedTokens    int // cumulative prompt+output across the session
	promptTokens  int // last round's prompt alone (cache-share denominator)
	cachedTokens  int // last round's cached prompt tokens (ADR-0018)
	window        int // model input token limit, 0 = unknown
	windowAssumed bool
}

// New creates the model.
func New(opts Options) Model {
	msgs := opts.Msgs
	if msgs == nil {
		msgs = uitext.For(uitext.EN)
	}
	ta := textarea.New()
	// The placeholder is where key discovery lives now that the
	// always-on hint line is gone (it only shows while the input is
	// empty, so it costs nothing during a conversation).
	ta.Placeholder = msgs.Placeholder
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	m := Model{
		ta:              ta,
		spin:            sp,
		msgs:            msgs,
		histIdx:         -1,
		live:            &strings.Builder{},
		hold:            &bottomHold{},
		startTurn:       opts.StartTurn,
		shell:           opts.Shell,
		compact:         opts.Compact,
		riskbook:        opts.Riskbook,
		slash:           opts.Slash,
		toggleAuto:      opts.ToggleAuto,
		autoMode:        opts.AutoMode,
		completePath:    opts.CompletePath,
		completeSlashFn: opts.CompleteSlash,
		settingsData:    opts.Settings,
		applySetting:    opts.ApplySetting,
		expandInput:     opts.ExpandInput,
		baseCtx:         opts.BaseCtx,
		println:         opts.Printer,
		mkRender:        opts.RenderFactory,
		width:           80,
		banner:          opts.Banner,
		modelName:       opts.ModelName,
		projectDir:      opts.ProjectDir,
	}
	if m.baseCtx == nil {
		m.baseCtx = context.Background()
	}
	if m.println == nil {
		m.println = tea.Println
	}
	theme := opts.Theme
	if theme == "" {
		theme = "dark"
	}
	if theme == "notty" {
		m.st = plainStyles()
	} else {
		m.st = defaultStyles(theme == "dark")
	}
	if m.mkRender == nil {
		m.mkRender = func(width int) func(string) string {
			return newGlamourRenderer(width, theme)
		}
	}
	m.render = m.mkRender(m.width)
	return m
}

// pinWidthModel pins go-runewidth's East Asian Ambiguous handling to
// "narrow" unless the operator set RUNEWIDTH_EASTASIAN explicitly.
// Under a CJK locale (LANG=ja_JP.UTF-8) go-runewidth flips Ambiguous
// glyphs — box drawing ─│┌┐├┤, arrows ►◄, "…" — to two cells, while the
// rest of this program's width stack (x/ansi, uniseg) and the common
// terminal setting treat them as one. glamour pads code-block lines
// with go-runewidth, so box art came out with per-line padding that
// depended on how many box characters the line held (measured:
// 176/125/172/125 cells on consecutive lines of one ER diagram), and
// emit()'s hard-wrap then sheared the over-padded tails. One width
// model, everywhere (v0.37.1).
func pinWidthModel() {
	if os.Getenv("RUNEWIDTH_EASTASIAN") == "" {
		runewidth.DefaultCondition.EastAsianWidth = false
	}
}

func init() { pinWidthModel() }

// newGlamourRenderer builds a fixed-style renderer. WithAutoStyle is
// deliberately absent: it queries the terminal (OSC), and once Bubble
// Tea owns stdin the reply arrives as phantom user input.
//
// The wrap width is the terminal's, full stop. An aesthetic cap (100
// cols) was tried and removed: glamour hard-wraps by inserting real
// newlines, so on a wide terminal every copied line broke far short of
// the console edge (operator report).
func newGlamourRenderer(width int, style string) func(string) string {
	w := width - 2
	if w < 20 {
		w = 20
	}
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(w))
	if err != nil {
		return func(s string) string { return s }
	}
	return func(s string) string {
		// The model's Markdown is rendered as written. Mermaid fences
		// stay source: diagrams are drawn by the render_diagram tool,
		// which tells the model whether it worked — rewriting the reply
		// here drew the diagram but left the model unable to learn that
		// its source was malformed (ADR-0043 §1).
		out, err := r.Render(s)
		if err != nil {
			return s
		}
		return strings.Trim(out, "\n")
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd { return textarea.Blink }

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Inline-renderer resize is the fragile spot: when the terminal
		// narrows, the previous frame's lines re-wrap and the renderer's
		// recorded height no longer matches, leaving stale copies of the
		// input box on screen. Two defenses: View() clips every line to
		// the width (no line of ours ever soft-wraps), and a genuine
		// shrink clears the viewport once to sweep the re-wrapped
		// leftovers. The first size report must not clear — it would
		// wipe the banner.
		// A terminal that reports no size (some pty harnesses, and any
		// environment where the ioctl fails) would otherwise give the
		// textarea a negative width and render an input box that shows
		// nothing the operator types.
		width, height := msg.Width, msg.Height
		if width < minWidth {
			width = minWidth
		}
		if height < minHeight {
			height = minHeight
		}
		// Deliberately shrink-only (ADR-0021 §9): growth also reflows in
		// some terminals (the counter then over-states and the input
		// block floats until the next shrink), but clearing on every
		// grow would erase visible content repeatedly during a drag
		// resize — a worse trade than the graceful drift.
		resized := m.sized && width < m.width
		first := !m.sized
		m.sized = true
		m.width = width
		m.height = height
		m.ta.SetWidth(width - 2)
		m.render = m.mkRender(width)
		switch {
		case first:
			// ADR-0003: clear to a known cursor row, then print the
			// banner through the counter so pinning is exact from the
			// first frame. Deferred to the first size report because
			// counting needs the real width.
			m.hold.printed = 0
			m.hold.lastTotal = 0
			cmds := []tea.Cmd{tea.ClearScreen}
			for _, line := range m.banner {
				cmds = append(cmds, m.emit(line))
			}
			return m, tea.Sequence(cmds...)
		case resized:
			m.hold.printed = 0 // the clear empties the viewport
			m.hold.lastTotal = 0
			return m, tea.ClearScreen
		}
		return m, nil

	case spinner.TickMsg:
		if m.phase == phaseInput {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case TextDelta:
		m.live.WriteString(string(msg))
		// The visible answer supersedes the thought tail (ADR-0033).
		m.thoughtTail = ""
		return m, nil

	case StreamUpdate:
		switch msg.Kind {
		case "chunk":
			m.chunkCount++
			m.lastChunk = time.Now()
			m.retryLine = "" // data is flowing again
			// toolRunning is NOT cleared here: during a tool the only
			// streams are side-calls (the risk evaluation, a progress
			// review), and mistaking them for "the tool returned"
			// produced false stall warnings (review round 3). ToolDone
			// is the signal.
		case "thought":
			if m.toolRunning {
				// A side-call's thoughts are not the main model's.
				break
			}
			m.thoughtTail += msg.Thought
		case "retry":
			m.retryLine = fmt.Sprintf(m.msgs.RetryFmt,
				msg.Attempt, msg.Max, msg.Cause, (msg.DelayMS+999)/1000)
		}
		return m, nil

	case Usage:
		round := msg.Prompt + msg.Output
		m.ctxTokens = round
		m.usedTokens += round
		m.cachedTokens = msg.Cached
		m.promptTokens = msg.Prompt
		return m, nil

	case ContextWindow:
		m.window = msg.Tokens
		m.windowAssumed = msg.Assumed
		return m, nil

	case ToolCall:
		// Flushed text and the tool event ride ONE write, keeping the
		// true order (text → tool event) with a single repaint.
		m.status = fmt.Sprintf(m.msgs.StatusRunningFmt, msg.Name)
		m.thoughtTail = ""   // the round's thoughts ended in a call
		m.toolRunning = true // stream silence is expected until the tool returns
		line := m.st.tool.Render("⚙ " + msg.Name + " " + msg.Detail)
		// A gated call that was auto-approved or allowlisted never opens
		// the dialog, so the event line is the only place its declared
		// purpose (ADR-0047) can appear. Only gated tools carry one, so
		// read-only chatter stays one line per call.
		if msg.Purpose != "" {
			line += "\n" + m.st.hint.Render("  "+m.msgs.PurposePrefix+m.purposeText(msg.Purpose))
		}
		return m, m.emitJoined(m.takeLive(), line)

	case ToolDone:
		// The tool returned: stall detection re-arms, and the status
		// stops claiming the tool is running (it used to stick at
		// "waiting for the tool…" for the rest of the round).
		m.toolRunning = false
		m.lastChunk = time.Now() // the silence clock restarts at the return
		if m.phase == phaseRunning {
			m.status = m.msgs.StatusThinking
		}
		return m, nil

	case AskRequest:
		if m.interruptSent {
			// Same rule as approvals (ADR-0034): no dialogs on behalf
			// of dead turns — decline silently.
			msg.Resp <- -1
			return m, nil
		}
		req := msg
		m.ask = &req
		m.askChoice = 0
		m.askAt = time.Now()
		m.phase = phaseAsk
		return m, nil

	case ApprovalRequest:
		if m.interruptSent {
			// The turn is already cancelled; the dialog would demand
			// an answer on behalf of a dead call. Deny silently — the
			// TurnDone (interrupted) line is the visible outcome.
			msg.Resp <- 'n'
			return m, nil
		}
		req := msg
		m.approval = &req
		m.approvalAt = time.Now()
		m.phase = phaseApproval
		// An escalated call starts on 拒否 so a reflexive Enter cannot
		// approve what the risk ladder objected to; an ordinary prompt
		// starts on 許可, which is what the operator is there to do.
		m.choice = choiceAllow
		if req.Reason != "" {
			m.choice = choiceDeny
		}
		return m, nil

	case AutoApproved:
		return m, m.emit(m.st.tool.Render(fmt.Sprintf(m.msgs.AutoApprovedFmt, msg.Tier, msg.Reason)))

	case AutoMode:
		m.autoMode = bool(msg)
		return m, nil

	case Output:
		return m, m.emitJoined(msg.Lines...)

	case Attached:
		var parts []string
		for _, line := range msg.Lines {
			parts = append(parts, m.st.tool.Render("📎 "+line))
		}
		for _, note := range msg.Notes {
			parts = append(parts, m.st.warn.Render("⚠ "+note))
		}
		return m, m.emitJoined(parts...)

	case Diagram:
		// Verbatim, like ShellDone: box art through the Markdown
		// renderer would be re-wrapped and sheared.
		return m, m.emitJoined(msg.Art)

	case ShellDone:
		out := msg.Output
		if strings.TrimSpace(out) == "" {
			out = "(no output)"
		}
		interrupted := ""
		if msg.Interrupted {
			interrupted = m.st.status.Render(m.msgs.Interrupted)
		}
		// Raw terminal output — never through the Markdown renderer —
		// and the outcome line in the same single write.
		cmds := []tea.Cmd{m.emitJoined(strings.TrimRight(out, "\n"), interrupted)}
		m.phase = phaseInput
		m.status = ""
		m.releaseTurn()
		m.ta.Focus()
		return m.resumeAfterTurn(cmds, !msg.Interrupted)

	case TurnDone:
		// Flushed text and the outcome line ride ONE write: separate
		// Printlns gave the slow-terminal flash the operator reported.
		tail := ""
		if msg.Err != nil {
			if errors.Is(msg.Err, context.Canceled) {
				tail = m.st.status.Render(m.msgs.Interrupted)
			} else {
				tail = m.st.errS.Render(m.msgs.ErrorPrefix + msg.Err.Error())
			}
		}
		var cmds []tea.Cmd
		if c := m.emitJoined(m.takeLive(), tail); c != nil {
			cmds = append(cmds, c)
		}
		m.phase = phaseInput
		m.status = ""
		m.releaseTurn()
		m.ta.Focus()
		return m.resumeAfterTurn(cmds, msg.Err == nil)

	case tea.KeyMsg:
		switch m.phase {
		case phaseSettings:
			return m.updateSettings(msg)
		case phaseApproval:
			return m.updateApproval(msg)
		case phaseAsk:
			return m.updateAsk(msg)
		case phaseRunning:
			switch msg.Type {
			case tea.KeyCtrlC:
				// Always the interrupt while running, never a draft
				// clear: an escape hatch conditional on the input box
				// being empty is not an escape hatch (ADR-0007).
				if m.interruptSent {
					// The last-resort exit (ADR-0034 §3): a wedged
					// tool that ignores cancellation must not trap
					// the operator forever. Second press warns, third
					// quits. Three, not two — a panic double-tap must
					// not kill a session about to finish cleanly.
					m.interruptPresses++
					if m.interruptPresses == 1 {
						return m, m.emit(m.st.warn.Render(m.msgs.InterruptStuckWarn))
					}
					return m, tea.Sequence(m.emit(m.st.hint.Render(m.msgs.Bye)), tea.Quit)
				}
				if m.cancelTurn != nil {
					m.cancelTurn()
					m.status = m.msgs.StatusInterrupting
					// A gate request already in flight would open a
					// dialog for a turn the operator just killed —
					// and an 'a' answer would allowlist on its behalf.
					// Auto-deny anything that arrives until TurnDone
					// (review round 2).
					m.interruptSent = true
				}
				return m, nil
			case tea.KeyShiftTab:
				// Toggling mid-run matters most here: a long agent loop
				// that started in manual mode would otherwise demand an
				// approval for every step until it finishes.
				return m.toggleAutoMode()
			}
			return m.updateRunningInput(msg)
		default:
			return m.updateInput(msg)
		}
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	return m, cmd
}

// emit prints one string into scrollback AND counts its physical lines
// (ANSI-aware, wrap-adjusted) — the accounting the bottom pinning rests
// on (ADR-0003). Every print of the model must go through here, never
// through m.println directly.
func (m *Model) emit(s string) tea.Cmd {
	// Tabs are expanded before counting AND printing: the width counter
	// sees "\t" as zero cells while the terminal advances to the next
	// 8-column stop, and every mismatch shifts the pinned input line —
	// `!git diff` output drifted it one row per wrapped tab line
	// (ADR-0021). Printing the expansion keeps count and drawing equal.
	s = expandTabs(s)
	// Then every line is hard-wrapped under the terminal width: an
	// over-wide scrollback line is NOT harmless (see wrapForScrollback).
	s = wrapForScrollback(s, m.width)
	w := m.width
	if w <= 0 {
		w = 80
	}
	total := 0
	for _, line := range strings.Split(s, "\n") {
		total += physicalRows(line, w)
	}
	if m.hold != nil {
		m.hold.printed += total
		// Each printed line scrolls history up one row; hand those rows
		// back to the bottom-hold gap so history flows into it
		// (ADR-0024 §3).
		if m.hold.lastTotal > 0 {
			m.hold.lastTotal -= total
			if m.hold.lastTotal < 0 {
				m.hold.lastTotal = 0
			}
		}
	}
	return m.println(s)
}

// beginTurnStats arms the ADR-0033 heartbeat for a fresh turn.
func (m *Model) beginTurnStats() {
	m.turnStart = time.Now()
	m.chunkCount = 0
	m.lastChunk = time.Time{}
	m.retryLine = ""
	m.thoughtTail = ""
	m.toolRunning = false // never inherited from an interrupted turn
}

// stallSeconds is how long with no data before the heartbeat switches
// to the warning style (ADR-0033 §1).
const stallSeconds = 20

// heartbeatLine renders the live stream stats for the running status
// bar: a scheduled retry wins, then a stall warning, then the normal
// elapsed/chunks/age line.
func (m Model) heartbeatLine() string {
	if m.turnStart.IsZero() {
		return ""
	}
	if m.retryLine != "" {
		return m.st.warn.Render("  " + m.retryLine)
	}
	last := m.lastChunk
	if last.IsZero() {
		last = m.turnStart
	}
	age := int(time.Since(last).Seconds())
	if age >= stallSeconds && !m.toolRunning {
		return m.st.warn.Render("  " + fmt.Sprintf(m.msgs.StallFmt, age))
	}
	return m.st.hint.Render("  " + fmt.Sprintf(m.msgs.HeartbeatFmt,
		fmtElapsed(time.Since(m.turnStart)), m.chunkCount, age))
}

// fmtElapsed renders a duration as "42s" / "3m07s".
func fmtElapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// purposeText renders the model's declared purpose, or names its
// absence (ADR-0047 §4). Clipped: the field asks for one sentence, and
// a model that writes a paragraph must not push the arguments off the
// approval prompt.
func (m Model) purposeText(purpose string) string {
	if p := strings.TrimSpace(purpose); p != "" {
		return clip(strings.ReplaceAll(p, "\n", " "), 200)
	}
	return m.msgs.PurposeNone
}

// thoughtView renders the live thought tail (ADR-0033 §3), dim, capped
// to the last two lines at the current width. Empty when there is
// nothing to show.
func (m Model) thoughtView() string {
	if m.thoughtTail == "" {
		return ""
	}
	text := m.msgs.ThoughtPrefix + strings.ReplaceAll(strings.TrimSpace(m.thoughtTail), "\n", " ")
	w := m.width
	if w <= 0 {
		w = 80
	}
	// Wrap to the width and keep the LAST two physical lines — the
	// freshest thought text. The old front-trim-then-clip showed the
	// oldest part of the kept window, so the newest words were never
	// visible (review round 3).
	lines := strings.Split(ansi.Hardwrap(text, w-1, true), "\n")
	if len(lines) > 2 {
		lines = lines[len(lines)-2:]
	}
	return m.st.status.Render(strings.Join(lines, "\n"))
}

// releaseTurn ends the per-turn context lifecycle. The cancel func was
// only ever invoked on Ctrl+C; clean finishes discarded it un-called,
// leaking one child context per turn on the process-lifetime BaseCtx
// (review round 2). Cancelling a finished context is a no-op, so this
// is safe on every completion path.
func (m *Model) releaseTurn() {
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	m.cancelTurn = nil
	m.interruptSent = false
	m.interruptPresses = 0
	if m.ask != nil {
		m.ask.Resp <- -1 // never strand the tool goroutine
		m.ask = nil
	}
	if m.approval != nil {
		m.approval.Resp <- 'n' // same rule for the approval gate
		m.approval = nil
	}
	m.turnStart = time.Time{}
	m.retryLine = ""
	m.thoughtTail = ""
	m.toolRunning = false
}

// wrapForScrollback hard-wraps every line of s to width-1 display
// cells before it is handed to tea.Println. Bubble Tea's inline
// renderer (v1.3.10 standard_renderer.flush) prints queued message
// lines VERBATIM — no truncation, and no EraseLineRight for a line at
// or beyond the terminal width — so an over-wide line (a ⚙ tool event
// with a long detail, a wide shell output line) soft-wraps on screen:
// the renderer's cursor ends up rows lower than its own accounting,
// the top rows of the previous frame (the thought line, the running
// status) are never repainted and leak into scrollback, and the old
// row's tail survives beyond the wrapped line's end on the same row
// (measured: the footer/thought-leak recurrence fixed in v0.34.1).
// Wrapping — never truncating, tool details and shell output are
// evidence — keeps every printed line strictly narrower than the
// terminal, so the renderer's math is exact and every line gets its
// EraseLineRight. width-1 matches clipLines' pending-auto-wrap margin.
func wrapForScrollback(s string, width int) string {
	if width <= 1 {
		return s
	}
	return ansi.Hardwrap(s, width-1, true)
}

// physicalRows models the terminal's greedy wrap for one logical line:
// a double-width rune that does not fit in the last column wraps WHOLE
// to the next row, wasting a cell. ceil(cells/width) assumed perfect
// packing and under-counted those rows, so the bottom pin drifted one
// row per straddling CJK line (review round 2 — the ADR-0028 heal
// trusts this count and cannot see the drift).
func physicalRows(line string, width int) int {
	if width <= 0 {
		return 1
	}
	rows, cells := 1, 0
	g := uniseg.NewGraphemes(ansi.Strip(line))
	for g.Next() {
		w := g.Width()
		if w <= 0 {
			continue
		}
		if cells+w > width {
			rows++
			cells = 0
		}
		cells += w
	}
	return rows
}

// expandTabs replaces each tab with spaces to the next 8-column stop,
// tracking the column ANSI-aware so color codes do not skew it.
func expandTabs(s string) string {
	if !strings.Contains(s, "\t") {
		return s
	}
	var out strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			out.WriteByte('\n')
		}
		col, start := 0, 0
		for j := 0; j < len(line); j++ {
			if line[j] != '\t' {
				continue
			}
			seg := line[start:j]
			out.WriteString(seg)
			col += ansi.StringWidth(seg)
			pad := 8 - col%8
			out.WriteString(strings.Repeat(" ", pad))
			col += pad
			start = j + 1
		}
		out.WriteString(line[start:])
	}
	return out.String()
}

// takeLive renders the accumulated streamed text as Markdown and
// returns it for scrollback (empty when nothing streamed). Rendering
// happens exactly once per segment — the live region shows raw text,
// the flush shows the pretty version.
func (m *Model) takeLive() string {
	text := strings.TrimSpace(m.live.String())
	m.live.Reset()
	if text == "" {
		return ""
	}
	return m.render(text)
}

// emitJoined prints consecutive scrollback lines as ONE write. Every
// tea.Println is a separate clear-insert-repaint cycle on the inline
// renderer; over a slow terminal (SSH to the test machine) the
// intermediate frames are visible as content flashing through the
// output area — the operator saw the Ctrl+C "(interrupted)" line do
// exactly that. One write, one repaint, no window (ADR-0003 note).
func (m *Model) emitJoined(parts ...string) tea.Cmd {
	kept := parts[:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return m.emit(strings.Join(kept, "\n"))
}

// approvalGrace is how long after the dialog appears keys are ignored —
// long enough to swallow a keystroke that was already in flight for the
// input box, short enough to be imperceptible when answering for real.
const approvalGrace = 300 * time.Millisecond

func (m Model) updateApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if time.Since(m.approvalAt) < approvalGrace {
		return m, nil // typed-ahead key aimed at the input box (ADR-0021)
	}
	answer := byte(0)
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp, tea.KeyShiftTab:
		m.choice = (m.choice - 1 + len(approvalAnswers)) % len(approvalAnswers)
		return m, nil
	case tea.KeyRight, tea.KeyDown, tea.KeyTab:
		m.choice = (m.choice + 1) % len(approvalAnswers)
		return m, nil
	case tea.KeyEnter:
		answer = approvalAnswers[m.choice]
	case tea.KeyEsc, tea.KeyCtrlC:
		answer = 'n'
	}
	if answer == 0 {
		// Letter shortcuts still work when the IME is off.
		switch strings.ToLower(msg.String()) {
		case "y":
			answer = 'y'
		case "n":
			answer = 'n'
		case "a":
			answer = 'a'
		case "p":
			answer = 'p'
		default:
			return m, nil
		}
	}
	req := m.approval
	m.approval = nil
	m.phase = phaseRunning
	m.status = m.msgs.StatusToolWait

	var cmds []tea.Cmd
	applyLine := ""
	if answer == 'p' && req != nil {
		// Persist first, so the line printed below reports what was
		// actually written rather than what was asked for.
		if m.applySetting == nil {
			answer = 'y' // no policy store in this mode: allow once
		} else {
			data, line := m.applySetting(SettingChange{
				Tool: req.Tool, Value: "never", Scope: ScopeGlobal,
			})
			m.settingsData = &data
			if line != "" {
				applyLine = m.st.warn.Render("  ⚠ " + line)
			}
		}
	}
	if req != nil {
		if answer == 'p' {
			req.Resp <- 'y'
		} else {
			req.Resp <- answer
		}
	}
	verdict := map[byte]string{
		'y': m.msgs.VerdictApproved,
		'n': m.msgs.VerdictDenied,
		'a': m.msgs.VerdictAlways,
		'p': m.msgs.VerdictPersist,
	}[answer]
	// One write for verdict + applier note: the 'p' path was the last
	// two-Println event left after the single-write convergence, and
	// separate writes are exactly the slow-terminal flash the rule
	// exists for (review round 2).
	cmds = append([]tea.Cmd{m.emitJoined(m.st.tool.Render("  ↳ "+verdict), applyLine)}, cmds...)
	return m, tea.Sequence(cmds...)
}

// updateRunningInput handles typing while a turn is running (ADR-0007).
// The box stays live so the operator can see what they are writing;
// Enter queues the message rather than sending it, because the agent
// loop owns the conversation until it returns.
func (m Model) updateRunningInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlJ, msg.Type == tea.KeyEnter && msg.Alt:
		m.ta.InsertString("\n")
		m.syncHeight()
		return m, nil

	case msg.Type == tea.KeyEnter:
		// The trailing-backslash newline route works here too.
		if strings.HasSuffix(m.ta.Value(), "\\") {
			v := m.ta.Value()
			m.ta.SetValue(v[:len(v)-1] + "\n")
			m.ta.CursorEnd()
			m.syncHeight()
			return m, nil
		}
		text := strings.TrimSpace(m.ta.Value())
		if text == "" {
			return m, nil
		}
		// Commands cannot be queued (ADR-0021 §7): queued messages merge
		// into ONE input, and prefix-routing the merged block would run
		// queued prose as shell after a queued `!`, or silently discard
		// everything after a queued `/command`. The text stays in the
		// box; Ctrl+C interrupts the turn if it cannot wait.
		if strings.HasPrefix(text, "!") || strings.HasPrefix(text, "/") {
			return m, m.emit(m.st.warn.Render(m.msgs.QueueRefused))
		}
		m.ta.Reset()
		m.ta.SetHeight(1)
		// A second Enter appends rather than replacing: nothing the
		// operator typed is dropped, and one pending message keeps the
		// agent one-turn-per-instruction (ADR-0007).
		if m.pending != "" {
			m.pending += "\n" + text
		} else {
			m.pending = text
		}
		return m, m.emit(m.st.hint.Render(m.msgs.QueuedPrefix + clip(text, 100)))

	case msg.Type == tea.KeyCtrlD, msg.Type == tea.KeyUp, msg.Type == tea.KeyDown:
		// Quitting and history navigation stay prompt-only: both would
		// mean something different in the middle of a turn.
		return m, nil
	}

	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.syncHeight()
	return m, cmd
}

// takePending returns the queued message, if any, and clears it.
func (m *Model) takePending() string {
	text := m.pending
	m.pending = ""
	return text
}

// resumeAfterTurn returns the UI to the prompt and deals with anything
// queued while the turn was running (ADR-0007). clean says whether the
// turn finished normally: a queued message is only sent when it did.
// A message written during a turn that then failed was written against a
// world that no longer exists, so it is handed back instead.
func (m Model) resumeAfterTurn(cmds []tea.Cmd, clean bool) (tea.Model, tea.Cmd) {
	pending := m.takePending()
	// A half-typed draft (written after the queued Enter, not yet
	// entered) must survive: overwriting the box with pending erased it
	// without a trace (ADR-0021).
	draft := strings.TrimSpace(m.ta.Value())
	if pending == "" {
		return m, tea.Sequence(append(cmds, textarea.Blink)...)
	}
	if !clean {
		// Hand back everything, in the order it was written.
		if draft != "" {
			pending += "\n" + draft
		}
		m.ta.SetValue(pending)
		m.ta.CursorEnd()
		m.syncHeight()
		cmds = append(cmds,
			m.emit(m.st.warn.Render(m.msgs.QueueHandback)),
			textarea.Blink)
		return m, tea.Sequence(cmds...)
	}
	m.ta.SetValue(pending)
	next, cmd := m.submit()
	if nm, ok := next.(Model); ok && draft != "" {
		// The queued message went out; the draft goes back to the box.
		nm.ta.SetValue(draft)
		nm.ta.CursorEnd()
		nm.syncHeight()
		next = nm
	}
	return next, tea.Sequence(append(cmds, cmd)...)
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		if strings.TrimSpace(m.ta.Value()) != "" {
			m.ta.Reset()
			m.histIdx = -1
			return m, nil
		}
		return m, tea.Sequence(m.emit(m.st.hint.Render(m.msgs.Bye)), tea.Quit)

	case msg.Type == tea.KeyCtrlD:
		if strings.TrimSpace(m.ta.Value()) == "" {
			return m, tea.Sequence(m.emit(m.st.hint.Render(m.msgs.Bye)), tea.Quit)
		}
		return m, nil

	case msg.Type == tea.KeyCtrlJ, msg.Type == tea.KeyEnter && msg.Alt:
		// Newline. Ctrl+J always arrives as a distinct key; Alt+Enter
		// only reaches us when the terminal is configured to send Meta
		// for Option (macOS defaults are not), and Shift+Enter never
		// does — both otherwise arrive as a plain CR that cannot be
		// told apart from submit. Hence the always-available Ctrl+J and
		// the trailing-backslash route below.
		m.ta.InsertString("\n")
		m.syncHeight()
		return m, nil

	case msg.Type == tea.KeyShiftTab:
		return m.toggleAutoMode()

	case msg.Type == tea.KeyTab:
		// An input that IS a command completes as one; otherwise Tab
		// serves the @-reference under the cursor.
		if strings.HasPrefix(m.ta.Value(), "/") {
			return m.completeSlash()
		}
		return m.completeMention()

	case msg.Type == tea.KeyEnter:
		// A trailing backslash continues the line, the shell convention
		// — the third newline route, for muscle memory that expects it.
		if strings.HasSuffix(m.ta.Value(), "\\") {
			v := m.ta.Value()
			m.ta.SetValue(v[:len(v)-1] + "\n")
			m.ta.CursorEnd()
			m.syncHeight()
			return m, nil
		}
		// Bracketed paste never arrives as KeyEnter (pasted newlines
		// travel inside a Paste-flagged KeyRunes message), so Enter
		// here is always a human submit.
		return m.submit()

	case msg.Type == tea.KeyUp && m.historyNavEligible():
		if m.histIdx == -1 {
			if len(m.history) == 0 {
				break
			}
			m.draft = m.ta.Value()
			m.histIdx = len(m.history) - 1
		} else if m.histIdx > 0 {
			m.histIdx--
		}
		m.ta.SetValue(m.history[m.histIdx])
		m.ta.CursorEnd()
		return m, nil

	case msg.Type == tea.KeyDown && m.historyNavEligible():
		// The guard: outside history navigation, Down must not touch
		// the draft (the recorded org lesson — an unguarded handler
		// destroys the input).
		if m.histIdx < 0 {
			break
		}
		m.histIdx++
		if m.histIdx >= len(m.history) {
			m.histIdx = -1
			m.ta.SetValue(m.draft)
		} else {
			m.ta.SetValue(m.history[m.histIdx])
		}
		m.ta.CursorEnd()
		return m, nil
	}

	// Any edit while a history entry is displayed turns it into the new
	// draft and leaves navigation mode.
	if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace || msg.Type == tea.KeyBackspace {
		m.histIdx = -1
	}
	var cmd tea.Cmd
	m.ta, cmd = m.ta.Update(msg)
	m.syncHeight()
	return m, cmd
}

// historyNavEligible: arrows navigate history only while the input is a
// single line; in multi-line drafts they move the cursor.
func (m Model) historyNavEligible() bool {
	return !strings.Contains(m.ta.Value(), "\n")
}

func (m *Model) syncHeight() {
	h := m.ta.LineCount()
	if h < 1 {
		h = 1
	}
	if h > maxInputHeight {
		h = maxInputHeight
	}
	m.ta.SetHeight(h)
}

func (m Model) submit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.ta.Value())
	if input == "" {
		return m, nil
	}
	m.ta.Reset()
	m.ta.SetHeight(1)
	m.history = append(m.history, input)
	m.histIdx = -1
	m.draft = ""

	// !command — direct shell mode: runs sandboxed without an approval
	// prompt (the user typed it themselves); the output is also fed to
	// the model as context by the runner.
	if strings.HasPrefix(input, "!") {
		command := strings.TrimSpace(strings.TrimPrefix(input, "!"))
		if command == "" || m.shell == nil {
			return m, nil
		}
		m.phase = phaseRunning
		m.status = fmt.Sprintf(m.msgs.StatusShellFmt, clip(command, 60))
		m.beginTurnStats()
		// A shell command has no model stream: the heartbeat shows the
		// elapsed time, never a "connection stalled" warning.
		m.toolRunning = true
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.cancelTurn = cancel
		m.shell(ctx, command)
		// Leading blank line separates this turn from the previous
		// output so consecutive turns don't run together.
		return m, tea.Batch(m.emit("\n"+m.st.user.Render("! ")+command), m.spin.Tick)
	}

	if input == "/settings" && m.settingsData != nil && m.applySetting != nil {
		return m.openSettings()
	}

	// /skill expands into a turn (ADR-0010): echo what the operator
	// typed, run the expanded text. Checked before the slash handler so
	// a synchronous handler never sees it.
	if m.expandInput != nil {
		if turn, handled, errMsg := m.expandInput(input); handled {
			if errMsg != "" {
				return m, m.emit(m.st.errS.Render("✗ " + errMsg))
			}
			m.phase = phaseRunning
			m.status = m.msgs.StatusThinking
			m.beginTurnStats()
			m.live.Reset()
			ctx, cancel := context.WithCancel(m.baseCtx)
			m.cancelTurn = cancel
			if m.startTurn != nil {
				m.startTurn(ctx, turn)
			}
			return m, tea.Batch(m.emit("\n"+m.st.user.Render("> ")+input), m.spin.Tick)
		}
	}

	// /compact makes an LLM call, so it runs like a turn rather than
	// like a slash command: the UI stays interruptible, and the result
	// arrives as TurnDone.
	if input == "/compact" && m.compact != nil {
		m.phase = phaseRunning
		m.status = m.msgs.StatusCompacting
		m.beginTurnStats()
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.cancelTurn = cancel
		m.compact(ctx)
		return m, tea.Batch(m.emit("\n"+m.st.user.Render("> ")+input), m.spin.Tick)
	}

	// /riskbook learn drives dialogs answered on this event loop, so it
	// runs like a turn, not like a slash command. toolRunning suppresses
	// the ADR-0033 stall warning: a pass waiting on a human produces no
	// model stream, and silence there is the normal case.
	if input == "/riskbook learn" && m.riskbook != nil {
		m.phase = phaseRunning
		m.status = m.msgs.RiskbookStatusLearning
		m.toolRunning = true
		m.beginTurnStats()
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.cancelTurn = cancel
		m.riskbook(ctx)
		return m, tea.Batch(m.emit("\n"+m.st.user.Render("> ")+input), m.spin.Tick)
	}

	if strings.HasPrefix(input, "/") {
		if m.slash == nil {
			return m, nil
		}
		out, isErr, quit := m.slash(input)
		text := strings.TrimRight(out, "\n")
		var line string
		if isErr {
			// Errors must stand out — dim meta styling here is how an
			// unknown command got camouflaged as help text.
			line = m.st.errS.Render("✗ " + text)
		} else {
			line = text // default foreground: readable on any theme
		}
		cmds := []tea.Cmd{m.emit(line)}
		if quit {
			cmds = append(cmds, tea.Quit)
			return m, tea.Sequence(cmds...)
		}
		return m, tea.Batch(cmds...)
	}

	m.phase = phaseRunning
	m.status = m.msgs.StatusThinking
	m.beginTurnStats()
	m.live.Reset()
	ctx, cancel := context.WithCancel(m.baseCtx)
	m.cancelTurn = cancel
	if m.startTurn != nil {
		m.startTurn(ctx, input)
	}
	// Leading blank line separates this turn from the previous output.
	return m, tea.Batch(m.emit("\n"+m.st.user.Render("> ")+input), m.spin.Tick)
}

// View implements tea.Model. Every line is clipped to the terminal
// width: a managed-region line that soft-wraps breaks the inline
// renderer's height accounting and leaves stale frames behind.
//
// The view is padded from the top so the input block pins to the window
// bottom (ADR-0003): height − printed lines − view height − 1. Once the
// conversation fills the screen the padding floors at zero and the
// layout degrades to plain inline following.
// bottomHold carries the pinning render state across View calls
// (ADR-0024, extended by ADR-0028): the printed-line counter and the
// held frame height live behind a pointer because View runs on a copy
// of the model, and both must survive it.
type bottomHold struct {
	// printed counts the physical rows above the frame top. It is
	// self-healing (ADR-0028): a frame taller than the rows left below
	// the printed content scrolls the terminal as it renders, moving
	// the anchor up — printed follows, or the next smaller frame is
	// positioned against rows that scrolled away (the /settings-ESC
	// bug: the panel filled the screen and the input view then floated
	// mid-screen).
	printed   int
	lastTotal int // frame height being held; 0 = disarmed (screen not full)
}

func (m Model) View() string {
	content := clipLines(m.viewContent(), m.width)
	if m.height > 0 {
		// The managed view must never exceed height-1 lines: an
		// over-tall frame scrolls the terminal and permanently desyncs
		// the printed-line counter (the settings-panel lesson, ADR-0021
		// generalises it). Drop from the top — the input box and footer
		// at the bottom are what the operator must always see.
		if lines := strings.Split(content, "\n"); len(lines) > m.height-1 {
			lines = lines[len(lines)-(m.height-1):]
			content = strings.Join(lines, "\n")
		}
		core := strings.Count(content, "\n") + 1
		if m.hold == nil {
			return content
		}
		// Scroll accounting (ADR-0028): rendering past the available
		// rows scrolls the terminal and moves the frame anchor up by
		// the overflow; the counter must follow reality.
		if avail := m.height - 1 - m.hold.printed; core > avail {
			m.hold.printed = m.height - 1 - core
			if m.hold.printed < 0 {
				m.hold.printed = 0
			}
		}
		if pad := m.height - m.hold.printed - core - 1; pad > 0 {
			// Screen not full: the pad is the absorber (ADR-0003).
			m.hold.lastTotal = 0
			content = strings.Repeat("\n", pad) + content
		} else if m.hold != nil {
			// Bottom-hold (ADR-0024): the pad has clamped to zero, so a
			// shrinking view would lift the frame bottom — the footer —
			// by the difference. Hold the frame's total height instead:
			// vacated rows render blank at the frame top, and every
			// later scrollback line gives one row back (see emit), so
			// history flows into the gap. Without this, every MCP-call
			// flush (live tail reset) and every closing approval dialog
			// bounced the footer by up to a dozen rows.
			total := m.hold.lastTotal
			if total < core {
				total = core
			}
			if max := m.height - 1; total > max {
				total = max
			}
			m.hold.lastTotal = total
			if gap := total - core; gap > 0 {
				content = strings.Repeat("\n", gap) + content
			}
		}
	}
	return content
}

// maxApprovalDetailLines bounds the approval box's detail body; hidden
// lines are counted on a marker line, never dropped silently.
const maxApprovalDetailLines = 8

// clipDetail clips a call detail for the approval box: at most maxLines
// lines, each rune-safely shortened. hidden reports what was cut.
func clipDetail(detail string, maxLines int) (string, int) {
	// Count lines BEFORE the rune clip: clipping a 200-line heredoc to
	// 600 runes first left only the surviving lines to count, so the
	// disclosure whose whole job is honesty under-reported the hidden
	// count (review round 2).
	lines := strings.Split(detail, "\n")
	hidden := 0
	if len(lines) > maxLines {
		hidden = len(lines) - maxLines
		lines = lines[:maxLines]
	}
	return clip(strings.Join(lines, "\n"), 600), hidden
}

// clipLines truncates each line to width-1 display cells (ANSI-aware).
// The -1 keeps the cursor off the last column, where pending auto-wrap
// behaviour differs between terminals.
func clipLines(s string, width int) string {
	if width <= 1 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width-1, "")
	}
	return strings.Join(lines, "\n")
}

func (m Model) viewContent() string {
	switch m.phase {
	case phaseSettings:
		return m.settingsView() + "\n" + m.footer() + "\n"
	case phaseRunning:
		// The input box renders here too: ADR-0007 promises "the
		// operator sees what they are writing" while a turn runs, and
		// the keys were routed (updateRunningInput) without the box
		// ever being drawn (review round 2).
		body := m.liveView()
		if tv := m.thoughtView(); tv != "" {
			body += "\n" + tv
		}
		return body + "\n" + m.spin.View() + " " + m.st.status.Render(m.status) +
			m.heartbeatLine() + m.st.hint.Render(m.msgs.CtrlCHint) +
			"\n" + m.ta.View() + "\n" + m.footer() + "\n"
	case phaseAsk:
		return m.askView()
	case phaseApproval:
		req := m.approval
		if req == nil {
			return ""
		}
		// A multi-line shell command (heredoc, script) must not blow the
		// box past the view budget — but hiding lines silently would let
		// the operator approve a command they have not seen, so the
		// count of hidden lines is shown (ADR-0021).
		// The detail budget adapts to the terminal: on a short screen
		// the fixed 8-line budget overflowed the frame, and the View
		// clamp then cut rows FROM THE TOP with no disclosure — the
		// title (the tool being approved!) vanished first, the exact
		// silent hiding clipDetail exists to prevent (review round 2).
		// ~13 rows of fixed chrome: box borders, title, purpose, hidden
		// marker, reason, options, hint, live line, footer, clamp margin.
		budget := maxApprovalDetailLines
		if m.height > 0 { // 0 = size not yet reported; keep the full budget
			if avail := m.height - 13; avail < budget {
				budget = avail
				if budget < 0 {
					budget = 0
				}
			}
		}
		detail, hidden := clipDetail(req.Detail, budget)
		body := fmt.Sprintf(m.msgs.ApprovalTitleFmt, req.Tool)
		// The model's declared purpose (ADR-0047) frames the arguments
		// below it: the operator's question about an innocuous-looking
		// `cp` is never "is this dangerous" but "why does it want this".
		// Always rendered — a silently absent line would be read as "the
		// build does not have this yet" rather than "the model skipped a
		// required field".
		body += "\n" + m.st.tool.Render(m.msgs.PurposePrefix+m.purposeText(req.Purpose))
		if detail != "" {
			body += "\n" + m.st.hint.Render(detail)
		}
		if hidden > 0 {
			body += "\n" + m.st.warn.Render(fmt.Sprintf(m.msgs.ApprovalHiddenFmt, hidden))
		}
		if req.Reason != "" {
			// The escalation cause gets its own accented line: in auto
			// mode the operator's first question is "why is this asking
			// at all?", and dim text beside the arguments does not
			// answer it.
			body += "\n" + m.st.warn.Render("⚠ "+clip(req.Reason, 200))
		}
		body += "\n" + m.optionsLine() + "\n" +
			m.st.hint.Render(m.msgs.ApprovalHint)
		return m.liveView() + "\n" + m.st.box.Render(body) + "\n" + m.footer() + "\n"
	default:
		// One status line only — the key bindings live in /help. Two
		// stacked meta lines made the block read as clutter rather than
		// a status bar (operator feedback).
		return m.ta.View() + "\n" + m.footer() + "\n"
	}
}

// completeMention completes the @-reference the input ends with. Tab is
// a no-op otherwise, so it never inserts a stray tab character into a
// message. With one match it completes fully; with several it advances
// to the longest common prefix and lists the candidates.
func (m Model) completeMention() (tea.Model, tea.Cmd) {
	if m.completePath == nil {
		return m, nil
	}
	value := m.ta.Value()
	at := strings.LastIndex(value, "@")
	if at < 0 {
		return m, nil
	}
	prefix := value[at+1:]
	if strings.ContainsAny(prefix, " \t\n") {
		return m, nil // the @-reference already ended
	}
	candidates := m.completePath(prefix)
	if len(candidates) == 0 {
		return m, nil
	}
	completed := candidates[0]
	var cmd tea.Cmd
	if len(candidates) > 1 {
		completed = longestCommonPrefix(candidates)
		if completed == prefix {
			// No further progress possible — show what is available
			// rather than leaving Tab looking broken.
			cmd = m.emit(m.st.hint.Render("  " + strings.Join(candidates, "  ")))
		}
	}
	m.ta.SetValue(value[:at+1] + completed)
	m.ta.CursorEnd()
	m.syncHeight()
	return m, cmd
}

// completeSlash completes a /command (and, through the injected
// completer, a /skill name) the same way @-references complete: to the
// unique match, else to the longest common prefix, listing the
// candidates when Tab cannot advance.
func (m Model) completeSlash() (tea.Model, tea.Cmd) {
	if m.completeSlashFn == nil {
		return m, nil
	}
	value := m.ta.Value()
	if strings.ContainsAny(value, "\n") {
		return m, nil // multi-line input is a message, not a command
	}
	candidates := m.completeSlashFn(value)
	if len(candidates) == 0 {
		return m, nil
	}
	completed := candidates[0]
	var cmd tea.Cmd
	if len(candidates) > 1 {
		completed = longestCommonPrefix(candidates)
		if completed == value {
			cmd = m.emit(m.st.hint.Render("  " + strings.Join(candidates, "  ")))
		}
	}
	m.ta.SetValue(completed)
	m.ta.CursorEnd()
	m.syncHeight()
	return m, cmd
}

func longestCommonPrefix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := candidates[0]
	for _, c := range candidates[1:] {
		for !strings.HasPrefix(c, prefix) {
			// Trim a RUNE, not a byte: 資料/説明 share the lead byte of
			// their first kanji, and a byte-wise trim converged to that
			// lone 0xE8 — invalid UTF-8 written into the input box
			// (measured; review round 2).
			_, size := utf8.DecodeLastRuneInString(prefix)
			prefix = prefix[:len(prefix)-size]
			if prefix == "" {
				return ""
			}
		}
	}
	return prefix
}

// toggleAutoMode flips auto-approve and announces the new state. It
// works during a run as well as at the prompt; the agent reads the flag
// per tool call, so the change lands on the next one (a call already
// waiting at the approval dialog still needs its answer).
func (m Model) toggleAutoMode() (tea.Model, tea.Cmd) {
	if m.toggleAuto == nil {
		return m, nil
	}
	m.autoMode = m.toggleAuto()
	// The same catalog strings /auto prints: shift+tab and /auto are
	// documented as the same toggle, and the two paths announced it in
	// different words — one of them never localized (review round 2).
	state := strings.TrimSpace(m.msgs.AutoOff)
	if m.autoMode {
		state = strings.TrimSpace(m.msgs.AutoOn)
	}
	// One write: the notice lands after the output it followed, with a
	// single repaint.
	return m, m.emitJoined(m.takeLive(), m.st.tool.Render(state))
}

// updateAsk handles the ask_user dialog (ADR-0036): the approval
// dialog's interaction grammar — arrows/Tab move, Enter confirms,
// digits 1-9 select-and-confirm in one press, Esc declines.
func (m Model) updateAsk(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if time.Since(m.askAt) < approvalGrace {
		return m, nil // typed-ahead key aimed at the input box
	}
	req := m.ask
	if req == nil {
		m.phase = phaseRunning
		return m, nil
	}
	n := len(req.Options)
	if n == 0 {
		// Unreachable through cmd (2–8 validated), but the exported
		// message must not be able to panic the UI on `% 0`.
		m.ask = nil
		m.phase = phaseRunning
		req.Resp <- -1
		return m, nil
	}
	answer := -2 // -2 = no answer yet; -1 = decline
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp, tea.KeyShiftTab:
		m.askChoice = (m.askChoice - 1 + n) % n
		return m, nil
	case tea.KeyRight, tea.KeyDown, tea.KeyTab:
		m.askChoice = (m.askChoice + 1) % n
		return m, nil
	case tea.KeyEnter:
		answer = m.askChoice
	case tea.KeyEsc, tea.KeyCtrlC:
		answer = -1
	default:
		if r := msg.String(); len(r) == 1 && r[0] >= '1' && r[0] <= '9' {
			if i := int(r[0] - '1'); i < n {
				answer = i
			}
		}
	}
	if answer == -2 {
		return m, nil
	}
	m.ask = nil
	m.phase = phaseRunning
	m.status = m.msgs.StatusToolWait
	req.Resp <- answer
	chosen := m.msgs.VerdictDenied
	if answer >= 0 {
		chosen = req.Options[answer]
	}
	return m, m.emit(m.st.tool.Render("  ↳ " + chosen))
}

// askView renders the ask_user dialog: the question and a numbered
// vertical option list.
func (m Model) askView() string {
	req := m.ask
	if req == nil {
		return ""
	}
	// The box has no width of its own, and clipLines would truncate
	// every over-wide line at the terminal edge with no marker — the
	// operator answered questions they could not fully read (review
	// round 3). So: wrap to the inner width, budget the height, and
	// disclose what was hidden, exactly like the approval dialog.
	inner := m.width - 6
	if inner < 20 {
		inner = 20
	}
	qLines := strings.Split(ansi.Hardwrap(fmt.Sprintf(m.msgs.AskTitleFmt, req.Question), inner, true), "\n")
	maxQ := maxAskQuestionLines
	if m.height > 0 && m.height-12 < maxQ {
		maxQ = m.height - 12
		if maxQ < 2 {
			maxQ = 2
		}
	}
	hidden := 0
	if len(qLines) > maxQ {
		hidden = len(qLines) - maxQ
		qLines = qLines[:maxQ]
	}
	var b strings.Builder
	b.WriteString(strings.Join(qLines, "\n"))
	if hidden > 0 {
		b.WriteString("\n" + m.st.warn.Render(fmt.Sprintf(m.msgs.AskHiddenFmt, hidden)))
	}
	for i, o := range req.Options {
		text := fmt.Sprintf("%d) %s", i+1, o)
		oLines := strings.Split(ansi.Hardwrap(text, inner-2, true), "\n")
		for j, l := range oLines {
			prefix := "  "
			if j == 0 && i == m.askChoice {
				b.WriteString("\n" + m.st.selected.Render("▶ "+l))
				continue
			}
			if j > 0 {
				prefix = "     "
			}
			b.WriteString("\n" + prefix + l)
		}
	}
	b.WriteString("\n" + m.st.hint.Render(m.msgs.AskHint))
	return m.liveView() + "\n" + m.st.box.Render(b.String()) + "\n" + m.footer() + "\n"
}

// maxAskQuestionLines bounds the wrapped question body; hidden lines
// are disclosed on a marker line, never dropped silently.
const maxAskQuestionLines = 8

// optionsLine renders the selectable answers. The selection is marked
// with "▶" as well as styled, so it stays visible under theme = plain
// (nothing here may depend on color alone).
func (m Model) optionsLine() string {
	labels := m.approvalLabels()
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == m.choice {
			parts = append(parts, m.st.selected.Render("▶ "+label))
			continue
		}
		parts = append(parts, "  "+label)
	}
	return strings.Join(parts, "   ")
}

// footer is the persistent status line: model, context occupancy vs the
// model's window, cumulative token consumption, project directory.
func (m Model) footer() string {
	ctx := "–"
	if m.ctxTokens > 0 {
		ctx = humanTokens(m.ctxTokens)
	}
	window := "–"
	if m.window > 0 {
		window = humanTokens(m.window)
		if m.windowAssumed {
			window = "~" + window
		}
	}
	occupancy := "ctx " + ctx + "/" + window
	if m.ctxTokens > 0 && m.window > 0 {
		occupancy += fmt.Sprintf(" (%.0f%%)", float64(m.ctxTokens)/float64(m.window)*100)
	}
	if m.cachedTokens > 0 && m.promptTokens > 0 {
		// The measured answer to "is implicit caching firing" (ADR-0018).
		occupancy += fmt.Sprintf(" · cache %.0f%%", float64(m.cachedTokens)/float64(m.promptTokens)*100)
	}
	parts := []string{m.modelName, occupancy, "total " + humanTokens(m.usedTokens)}
	if m.projectDir != "" {
		parts = append(parts, m.projectDir)
	}
	line := m.st.hint.Render(strings.Join(parts, " · "))
	if m.autoMode {
		// Auto mode changes what runs without asking — it must be
		// visible at all times, and in the accent color, not the dim one.
		line = m.st.tool.Render("⚡auto") + m.st.hint.Render(" · ") + line
	}
	return line
}

// humanTokens renders a token count compactly (999 / 12.3k / 1.0M).
func humanTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// liveView shows the tail of the streaming text in the managed region;
// the full text lands in scrollback at flush time.
func (m Model) liveView() string {
	text := m.live.String()
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > liveTailLines {
		lines = lines[len(lines)-liveTailLines:]
	}
	// Tabs are expanded here for the same reason emit() expands them:
	// the width clip counts "\t" as zero cells while the terminal
	// advances to the next stop, so a tab-indented code line passed
	// the clip and soft-wrapped — the renderer-desync class again,
	// from the managed region this time (review round 3).
	return expandTabs(strings.Join(lines, "\n"))
}

// clip truncates for display, by runes — a byte cut splits a UTF-8
// sequence two times out of three on Japanese text and prints U+FFFD
// mid-word (ADR-0021).
func clip(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}

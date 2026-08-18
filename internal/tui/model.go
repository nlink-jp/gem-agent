package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type phase int

const (
	phaseInput phase = iota
	phaseRunning
	phaseApproval
)

const (
	maxInputHeight = 6
	liveTailLines  = 12
	maxMDWidth     = 100
	// Floors for a terminal that reports a bogus size.
	minWidth  = 20
	minHeight = 4
)

// approvalOptions are the approval dialog's selectable answers, in
// display order. The index is the model's `choice`.
var approvalOptions = []struct {
	label  string
	answer byte
}{
	{"許可 (y)", 'y'},
	{"拒否 (n)", 'n'},
	{"常に許可 (a)", 'a'},
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

// SlashHandler executes a /command and returns its output, whether the
// output is an error (rendered so it stands out — an unknown command
// must never look like dim meta text), and whether the program should
// quit.
type SlashHandler func(cmd string) (output string, isErr bool, quit bool)

// Options configures the model.
type Options struct {
	StartTurn TurnStarter
	Shell     ShellStarter
	Slash     SlashHandler
	BaseCtx   context.Context
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

	phase   phase
	history []string
	histIdx int // -1 = not navigating (the guard the org lesson demands)
	draft   string

	// live is a pointer on purpose: Bubble Tea passes the model BY VALUE
	// through every Update, and a non-zero strings.Builder held by value
	// panics on the second WriteString after a copy ("illegal use of
	// non-zero Builder copied by value"). Found live: the second stream
	// chunk of the first real conversation crashed the program.
	live     *strings.Builder
	status   string
	approval *ApprovalRequest
	// choice indexes approvalOptions. Selection + Enter exists because
	// typing y/n/a is impossible with a Japanese IME switched on — the
	// letters are swallowed by composition — while arrows, Tab, and
	// Enter reach the app untouched when nothing is being composed.
	choice int

	startTurn    TurnStarter
	shell        ShellStarter
	slash        SlashHandler
	toggleAuto   func() bool
	autoMode     bool
	completePath func(prefix string) []string
	baseCtx      context.Context
	cancelTurn   context.CancelFunc

	width    int
	height   int
	sized    bool // first WindowSizeMsg received
	banner   []string
	printed  int // physical lines emitted since the last screen clear (ADR-0003)
	st       styleSet
	render   func(string) string
	mkRender func(width int) func(string) string
	println  func(...any) tea.Cmd

	// Footer state.
	modelName     string
	projectDir    string
	ctxTokens     int // last round's prompt+output ≈ current context size
	usedTokens    int // cumulative prompt+output across the session
	window        int // model input token limit, 0 = unknown
	windowAssumed bool
}

// New creates the model.
func New(opts Options) Model {
	ta := textarea.New()
	// The placeholder is where key discovery lives now that the
	// always-on hint line is gone (it only shows while the input is
	// empty, so it costs nothing during a conversation).
	ta.Placeholder = "message…  Enter 送信 · Ctrl+J 改行 · /help · !shell"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	m := Model{
		ta:           ta,
		spin:         sp,
		histIdx:      -1,
		live:         &strings.Builder{},
		startTurn:    opts.StartTurn,
		shell:        opts.Shell,
		slash:        opts.Slash,
		toggleAuto:   opts.ToggleAuto,
		autoMode:     opts.AutoMode,
		completePath: opts.CompletePath,
		baseCtx:      opts.BaseCtx,
		println:      opts.Printer,
		mkRender:     opts.RenderFactory,
		width:        80,
		banner:       opts.Banner,
		modelName:    opts.ModelName,
		projectDir:   opts.ProjectDir,
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

// newGlamourRenderer builds a fixed-style renderer. WithAutoStyle is
// deliberately absent: it queries the terminal (OSC), and once Bubble
// Tea owns stdin the reply arrives as phantom user input.
func newGlamourRenderer(width int, style string) func(string) string {
	w := min(width-2, maxMDWidth)
	if w < 20 {
		w = 20
	}
	r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(w))
	if err != nil {
		return func(s string) string { return s }
	}
	return func(s string) string {
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
		shrank := m.sized && width < m.width
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
			m.printed = 0
			cmds := []tea.Cmd{tea.ClearScreen}
			for _, line := range m.banner {
				cmds = append(cmds, m.emit(line))
			}
			return m, tea.Sequence(cmds...)
		case shrank:
			m.printed = 0 // the clear empties the viewport
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
		return m, nil

	case Usage:
		round := msg.Prompt + msg.Output
		m.ctxTokens = round
		m.usedTokens += round
		return m, nil

	case ContextWindow:
		m.window = msg.Tokens
		m.windowAssumed = msg.Assumed
		return m, nil

	case ToolCall:
		// Flush accumulated text first so scrollback keeps the true
		// order: text → tool event → next text.
		var cmds []tea.Cmd
		cmds = append(cmds, m.flushLive()...)
		m.status = "running " + msg.Name
		cmds = append(cmds, m.emit(m.st.tool.Render("⚙ "+msg.Name+" "+msg.Detail)))
		return m, tea.Batch(cmds...)

	case ApprovalRequest:
		req := msg
		m.approval = &req
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
		return m, m.emit(m.st.tool.Render("  ↳ auto-approved (" + msg.Tier + "): " + msg.Reason))

	case AutoMode:
		m.autoMode = bool(msg)
		return m, nil

	case Attached:
		var cmds []tea.Cmd
		for _, line := range msg.Lines {
			cmds = append(cmds, m.emit(m.st.tool.Render("📎 "+line)))
		}
		for _, note := range msg.Notes {
			cmds = append(cmds, m.emit(m.st.warn.Render("⚠ "+note)))
		}
		return m, tea.Batch(cmds...)

	case ShellDone:
		out := msg.Output
		if strings.TrimSpace(out) == "" {
			out = "(no output)"
		}
		// Raw terminal output — never through the Markdown renderer.
		cmds := []tea.Cmd{m.emit(strings.TrimRight(out, "\n"))}
		m.phase = phaseInput
		m.status = ""
		m.cancelTurn = nil
		m.ta.Focus()
		cmds = append(cmds, textarea.Blink)
		return m, tea.Batch(cmds...)

	case TurnDone:
		var cmds []tea.Cmd
		cmds = append(cmds, m.flushLive()...)
		if msg.Err != nil {
			if errors.Is(msg.Err, context.Canceled) {
				cmds = append(cmds, m.emit(m.st.status.Render("(interrupted)")))
			} else {
				cmds = append(cmds, m.emit(m.st.errS.Render("✗ error: "+msg.Err.Error())))
			}
		}
		m.phase = phaseInput
		m.status = ""
		m.cancelTurn = nil
		m.ta.Focus()
		cmds = append(cmds, textarea.Blink)
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		switch m.phase {
		case phaseApproval:
			return m.updateApproval(msg)
		case phaseRunning:
			switch msg.Type {
			case tea.KeyCtrlC:
				if m.cancelTurn != nil {
					m.cancelTurn()
					m.status = "interrupting…"
				}
			case tea.KeyShiftTab:
				// Toggling mid-run matters most here: a long agent loop
				// that started in manual mode would otherwise demand an
				// approval for every step until it finishes.
				return m.toggleAutoMode()
			}
			return m, nil
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
	w := m.width
	if w <= 0 {
		w = 80
	}
	for _, line := range strings.Split(s, "\n") {
		phys := 1
		if lw := ansi.StringWidth(line); lw > w {
			phys = (lw + w - 1) / w
		}
		m.printed += phys
	}
	return m.println(s)
}

// flushLive renders accumulated streamed text as Markdown and emits it
// to scrollback. Rendering happens exactly once per segment — the live
// region shows raw text, the flush shows the pretty version.
func (m *Model) flushLive() []tea.Cmd {
	text := strings.TrimSpace(m.live.String())
	m.live.Reset()
	if text == "" {
		return nil
	}
	return []tea.Cmd{m.emit(m.render(text))}
}

func (m Model) updateApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	answer := byte(0)
	switch msg.Type {
	case tea.KeyLeft, tea.KeyUp, tea.KeyShiftTab:
		m.choice = (m.choice - 1 + len(approvalOptions)) % len(approvalOptions)
		return m, nil
	case tea.KeyRight, tea.KeyDown, tea.KeyTab:
		m.choice = (m.choice + 1) % len(approvalOptions)
		return m, nil
	case tea.KeyEnter:
		answer = approvalOptions[m.choice].answer
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
		default:
			return m, nil
		}
	}
	req := m.approval
	m.approval = nil
	m.phase = phaseRunning
	m.status = "waiting for the tool…"
	if req != nil {
		req.Resp <- answer
	}
	verdict := map[byte]string{'y': "approved", 'n': "denied", 'a': "approved (always this session)"}[answer]
	return m, m.emit(m.st.tool.Render("  ↳ " + verdict))
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		if strings.TrimSpace(m.ta.Value()) != "" {
			m.ta.Reset()
			m.histIdx = -1
			return m, nil
		}
		return m, tea.Sequence(m.emit(m.st.hint.Render("bye")), tea.Quit)

	case msg.Type == tea.KeyCtrlD:
		if strings.TrimSpace(m.ta.Value()) == "" {
			return m, tea.Sequence(m.emit(m.st.hint.Render("bye")), tea.Quit)
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
		m.status = "shell: " + clip(command, 60)
		ctx, cancel := context.WithCancel(m.baseCtx)
		m.cancelTurn = cancel
		m.shell(ctx, command)
		// Leading blank line separates this turn from the previous
		// output so consecutive turns don't run together.
		return m, tea.Batch(m.emit("\n"+m.st.user.Render("! ")+command), m.spin.Tick)
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
	m.status = "thinking…"
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
func (m Model) View() string {
	content := clipLines(m.viewContent(), m.width)
	if m.height > 0 {
		core := strings.Count(content, "\n") + 1
		if pad := m.height - m.printed - core - 1; pad > 0 {
			content = strings.Repeat("\n", pad) + content
		}
	}
	return content
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
	case phaseRunning:
		return m.liveView() + "\n" + m.spin.View() + " " + m.st.status.Render(m.status) +
			m.st.hint.Render("  (Ctrl+C で中断)") + "\n" + m.footer() + "\n"
	case phaseApproval:
		req := m.approval
		if req == nil {
			return ""
		}
		body := "approval required: " + req.Tool + "\n" +
			m.st.hint.Render(clip(req.Detail, 300))
		if req.Reason != "" {
			// The escalation cause gets its own accented line: in auto
			// mode the operator's first question is "why is this asking
			// at all?", and dim text beside the arguments does not
			// answer it.
			body += "\n" + m.st.warn.Render("⚠ "+clip(req.Reason, 200))
		}
		body += "\n" + m.optionsLine() + "\n" +
			m.st.hint.Render("←→/Tab 選択 · Enter 決定 · y/n/a 直接指定 · Esc 拒否")
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

func longestCommonPrefix(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	prefix := candidates[0]
	for _, c := range candidates[1:] {
		for !strings.HasPrefix(c, prefix) {
			prefix = prefix[:len(prefix)-1]
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
	state := "auto-approve: OFF (every change asks)"
	if m.autoMode {
		state = "auto-approve: ON (risky actions still ask)"
	}
	// Flush streamed text first so the notice lands after the output it
	// followed, not before it.
	cmds := m.flushLive()
	cmds = append(cmds, m.emit(m.st.tool.Render(state)))
	return m, tea.Batch(cmds...)
}

// optionsLine renders the selectable answers. The selection is marked
// with "▶" as well as styled, so it stays visible under theme = plain
// (nothing here may depend on color alone).
func (m Model) optionsLine() string {
	parts := make([]string, 0, len(approvalOptions))
	for i, opt := range approvalOptions {
		if i == m.choice {
			parts = append(parts, m.st.selected.Render("▶ "+opt.label))
			continue
		}
		parts = append(parts, "  "+opt.label)
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
	return strings.Join(lines, "\n")
}

func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

package tui

import (
	"context"
	"errors"
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
)

// styleSet holds the chrome styles. Only ANSI-16 palette colors are
// used — they follow the user's terminal theme, unlike fixed 256/RGB
// values that can vanish against unknown backgrounds. Dim text uses
// color 8 (theme-defined gray), never the Faint attribute, which some
// terminals render close to invisible.
type styleSet struct {
	user   lipgloss.Style
	tool   lipgloss.Style
	errS   lipgloss.Style
	hint   lipgloss.Style
	status lipgloss.Style
	box    lipgloss.Style
}

func defaultStyles() styleSet {
	return styleSet{
		user:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		tool:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		errS:   lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true),
		hint:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		status: lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Italic(true),
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
		user: plain, tool: plain, errS: plain, hint: plain, status: plain,
		box: lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(0, 1),
	}
}

// TurnStarter launches one agent turn in a goroutine. It must not
// block; completion arrives as a TurnDone message.
type TurnStarter func(ctx context.Context, input string)

// SlashHandler executes a /command and returns its output, whether the
// output is an error (rendered so it stands out — an unknown command
// must never look like dim meta text), and whether the program should
// quit.
type SlashHandler func(cmd string) (output string, isErr bool, quit bool)

// Options configures the model.
type Options struct {
	StartTurn TurnStarter
	Slash     SlashHandler
	BaseCtx   context.Context
	// Theme is "dark", "light", or "notty" (plain: no colors anywhere).
	// It MUST be decided by the caller BEFORE the Bubble Tea program
	// starts: background detection sends an OSC query, and once raw
	// mode owns stdin the terminal's "rgb:..." reply would leak into
	// the input box as if the user typed it.
	Theme string
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

	startTurn  TurnStarter
	slash      SlashHandler
	baseCtx    context.Context
	cancelTurn context.CancelFunc

	width    int
	sized    bool // first WindowSizeMsg received
	st       styleSet
	render   func(string) string
	mkRender func(width int) func(string) string
	println  func(...any) tea.Cmd
}

// New creates the model.
func New(opts Options) Model {
	ta := textarea.New()
	ta.Placeholder = "message… (/help)"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetHeight(1)
	ta.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot

	m := Model{
		ta:        ta,
		spin:      sp,
		histIdx:   -1,
		live:      &strings.Builder{},
		startTurn: opts.StartTurn,
		slash:     opts.Slash,
		baseCtx:   opts.BaseCtx,
		println:   opts.Printer,
		mkRender:  opts.RenderFactory,
		width:     80,
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
		m.st = defaultStyles()
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
		shrank := m.sized && msg.Width < m.width
		m.sized = true
		m.width = msg.Width
		m.ta.SetWidth(msg.Width - 2)
		m.render = m.mkRender(msg.Width)
		if shrank {
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

	case ToolCall:
		// Flush accumulated text first so scrollback keeps the true
		// order: text → tool event → next text.
		var cmds []tea.Cmd
		cmds = append(cmds, m.flushLive()...)
		m.status = "running " + msg.Name
		cmds = append(cmds, m.println(m.st.tool.Render("⚙ "+msg.Name+" "+msg.Detail)))
		return m, tea.Batch(cmds...)

	case ApprovalRequest:
		req := msg
		m.approval = &req
		m.phase = phaseApproval
		return m, nil

	case TurnDone:
		var cmds []tea.Cmd
		cmds = append(cmds, m.flushLive()...)
		if msg.Err != nil {
			if errors.Is(msg.Err, context.Canceled) {
				cmds = append(cmds, m.println(m.st.status.Render("(interrupted)")))
			} else {
				cmds = append(cmds, m.println(m.st.errS.Render("✗ error: "+msg.Err.Error())))
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
			if msg.Type == tea.KeyCtrlC {
				if m.cancelTurn != nil {
					m.cancelTurn()
					m.status = "interrupting…"
				}
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

// flushLive renders accumulated streamed text as Markdown and emits it
// to scrollback. Rendering happens exactly once per segment — the live
// region shows raw text, the flush shows the pretty version.
func (m *Model) flushLive() []tea.Cmd {
	text := strings.TrimSpace(m.live.String())
	m.live.Reset()
	if text == "" {
		return nil
	}
	return []tea.Cmd{m.println(m.render(text))}
}

func (m Model) updateApproval(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	answer := byte(0)
	switch strings.ToLower(msg.String()) {
	case "y":
		answer = 'y'
	case "n", "esc":
		answer = 'n'
	case "a":
		answer = 'a'
	case "ctrl+c":
		answer = 'n'
	default:
		return m, nil
	}
	req := m.approval
	m.approval = nil
	m.phase = phaseRunning
	m.status = "waiting for the tool…"
	if req != nil {
		req.Resp <- answer
	}
	verdict := map[byte]string{'y': "approved", 'n': "denied", 'a': "approved (always this session)"}[answer]
	return m, m.println(m.st.tool.Render("  ↳ " + verdict))
}

func (m Model) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		if strings.TrimSpace(m.ta.Value()) != "" {
			m.ta.Reset()
			m.histIdx = -1
			return m, nil
		}
		return m, tea.Sequence(m.println(m.st.hint.Render("bye")), tea.Quit)

	case msg.Type == tea.KeyCtrlD:
		if strings.TrimSpace(m.ta.Value()) == "" {
			return m, tea.Sequence(m.println(m.st.hint.Render("bye")), tea.Quit)
		}
		return m, nil

	case msg.Type == tea.KeyCtrlJ:
		m.ta.InsertString("\n")
		m.syncHeight()
		return m, nil

	case msg.Type == tea.KeyEnter && !msg.Alt:
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
		cmds := []tea.Cmd{m.println(line)}
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
	return m, tea.Batch(m.println(m.st.user.Render("> ")+input), m.spin.Tick)
}

// View implements tea.Model. Every line is clipped to the terminal
// width: a managed-region line that soft-wraps breaks the inline
// renderer's height accounting and leaves stale frames behind.
func (m Model) View() string {
	return clipLines(m.viewContent(), m.width)
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
			m.st.hint.Render("  (Ctrl+C で中断)") + "\n"
	case phaseApproval:
		req := m.approval
		if req == nil {
			return ""
		}
		body := "approval required: " + req.Tool + "\n" +
			m.st.hint.Render(clip(req.Detail, 300)) + "\n" +
			"[y] 許可   [n] 拒否   [a] このセッションでは常に許可"
		return m.liveView() + "\n" + m.st.box.Render(body) + "\n"
	default:
		return m.ta.View() + "\n" +
			m.st.hint.Render("Enter 送信 · Ctrl+J 改行 · ↑↓ 履歴 · /help · Ctrl+D 終了") + "\n"
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

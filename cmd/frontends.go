package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/banner"
	"github.com/nlink-jp/gem-agent/internal/diagram"
	"github.com/nlink-jp/gem-agent/internal/mention"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/termimg"
	"github.com/nlink-jp/gem-agent/internal/tui"
)

// The front ends (ADR-0124 §1): each drives the assembled session, and
// all three start a turn through liveSession.turn.

// runOneShot is -p: one turn, quiet stderr, exit.
func runOneShot(sess *liveSession, cmd *cobra.Command, fl runFlags, rs runSettings, adm admission) error {
	// --- one-shot mode: single turn, quiet stderr, exit ---
	for _, n := range adm.policyNotes {
		fmt.Fprintf(sess.stderr, "warning: %s\n", n)
	}
	go sess.resolveWindow()
	// The measured state, all four branches: passing constants meant
	// one-shot printed nothing when the read lane was unverified —
	// and every shell_exec then needs an approval nobody is there to
	// give (pre-release review).
	if line := banner.SandboxLine(sess.registry.Confined(), sess.registry.ReadLane(), sess.cfg.Sandbox.ReadLanePrompts); line != "" {
		fmt.Fprintln(sess.stderr, line)
	}
	// One-shot returns before the banner is built, so the fact that
	// this run approves its own mutating tools had no surface at all
	// — in the mode where it matters most, since the ladder answers
	// and nobody is at a prompt (pre-release review).
	if sess.ag.AutoApprove() {
		fmt.Fprintln(sess.stderr, banner.AutoApproveOneShotLine())
	}
	// Same argument for the ceiling: one-shot has no footer to carry
	// it and no /readonly to type, so the only place this fact can
	// appear is here (ADR-0080 §1, ADR-0078's test for a line).
	if sess.ag.CeilingState().ReadOnly {
		fmt.Fprintln(sess.stderr, banner.ReadOnlyOneShotLine(sess.registry.Confined()))
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
			fmt.Fprintln(sess.stderr, stdinWaitMessage)
		})
		if warning != "" {
			fmt.Fprintf(sess.stderr, "warning: %s\n", warning)
		}
		if content != "" {
			sess.ag.AttachData("-", "stdin", content)
		}
		if line := stdinOutcomeLine(content, warning, waited); line != "" {
			fmt.Fprintln(sess.stderr, line)
		}
	}
	wrote := false
	runErr := runTurnWith(sess.ctx, rs.ladder, func(turnCtx context.Context) error {
		return sess.turn(turnCtx, fl.prompt, func(s string) {
			wrote = wrote || s != ""
			fmt.Fprint(cmd.OutOrStdout(), s)
		})
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

// buildBanner composes the startup banner, and reads the diagram font
// the TUI draws with: both happen where they did, after one-shot has
// returned and before a front end shows anything.
func buildBanner(sess *liveSession, cmd *cobra.Command, adm admission, op opened, ld loaded, fe frontEnd) ([]string, diagram.Picture) {
	// --- banner (ADR-0078) ---
	// The rule, and the composition, live in internal/banner: a line
	// earns a place here only if nothing else will say it, and that
	// rule needs somewhere to be stated, tested and rendered for the
	// operator-text read-through.
	warnLines := make([]string, 0, len(adm.policyNotes))
	for _, n := range adm.policyNotes {
		warnLines = append(warnLines, string(n))
	}
	// The diagram font is read here, once, and only where pictures can be
	// drawn (ADR-0092 §6); a setting that does not load is a warning.
	picture, fontNotes := diagramPicture(fe.images, sess.cfg.TUI.Diagram, systemFonts)
	warnLines = append(warnLines, fontNotes...)
	bannerLines := banner.Lines(banner.Facts{
		Version: cmd.Root().Version, Model: sess.cfg.Model.Name,
		Instructions: sess.contextLabels,
		Servers:      len(sess.mcpClients), Tools: mcpToolCount(sess.registry),
		Skills: len(sess.skillsList), Memories: len(ld.memories),
		ResumedID: op.resumedID, Restored: len(op.restored),
		// The measurement, not the intent: --no-sandbox is folded into
		// sandboxOn already, but a failed write-lane probe zeroes the
		// enforcement and left the banner saying "enabled" while
		// /settings said "DISABLED" — in the one case the row exists for.
		SandboxOn: sess.registry.Confined(), ReadLane: sess.registry.ReadLane(),
		ReadLanePrompts: sess.cfg.Sandbox.ReadLanePrompts,
		AutoApprove:     sess.ag.AutoApprove(),
		ReadOnly:        sess.ag.CeilingState(),
		Notes:           warnLines,
	})
	return bannerLines, picture
}

// runTUI runs the interactive TUI until the operator leaves.
func runTUI(sess *liveSession, cl *cleanups, notes *startupNotes, rs runSettings, adm admission, ld loaded, md models, fe frontEnd, as assembled, bannerLines []string, picture diagram.Picture) error {
	// --- interactive TUI (ADR-0002/0003) ---
	// prog is assigned before the TUI runs; the agent only executes
	// inside prog.Run, so the callbacks below never see it half-set.
	var prog *tea.Program
	// The banner goes through the TUI (not stderr): bottom pinning
	// counts every printed line, and the startup clear would wipe a
	// pre-printed banner anyway. Startup warnings join it for the
	// same reason (ADR-0021).
	// Nothing reads the tee after the banner; without the freeze the
	// stderr stream accumulated every line for the whole session
	// (review round 3).
	bannerLines = append(bannerLines, notes.freeze()...)
	// The renderer writes through the sweep writer, which erases the
	// rows a narrowing re-wrapped (ADR-0094); the model arms it.
	sweep := tui.NewSweepWriter(os.Stdout)
	// A dialog left waiting is told to the terminal (ADR-0104): the
	// runtime's own fixed bytes, written between two renderer
	// flushes. "off" passes no notifier, so nothing is scheduled.
	notify := noticeWriter(sess.cfg.TUI.Notify, sweep.Inject)
	model := tui.New(tui.Options{
		Sweep:   sweep,
		Notify:  notify,
		BaseCtx: sess.ctx,
		// Msgs is the wiring ADR-0029 shipped without: the catalog
		// was resolved here but never handed to the TUI, so the
		// whole chrome fell back to English (review round 2).
		Msgs:    sess.msgs,
		Theme:   resolveTheme(sess.cfg.TUI.Theme),
		Images:  fe.images,
		Picture: picture,
		// Bubble Tea draws on stdout; its cell size comes from an
		// ioctl there, never from a query (ADR-0092 §4).
		CellSize:      func() (termimg.Cell, bool) { return termimg.CellSize(int(os.Stdout.Fd())) },
		ModelName:     sess.cfg.Model.Name,
		ProjectDir:    abbreviateHome(sess.projectDir),
		Banner:        bannerLines,
		InitialInput:  rs.initialInput,
		AutoMode:      sess.ag.AutoApprove(),
		AutoState:     sess.ag.AutoApprove,
		ReadOnlyState: sess.ag.CeilingState,
		ToggleAuto: func() bool {
			sess.ag.SetAutoApprove(!sess.ag.AutoApprove())
			return sess.ag.AutoApprove()
		},
		CompletePath: func(prefix string) []string {
			return mention.Complete(sess.projectDir, prefix, 24)
		},
		CompleteSlash:   slashCompletions(func() []skills.Skill { return sess.skillsList }),
		Settings:        &as.settingsData,
		ApplySetting:    sess.settings.Apply,
		RefreshSettings: sess.settings.data,
		ExpandInput: func(in string) (string, bool, string) {
			return expandSkillInput(in, sess.skillsList)
		},
		Shell: func(shellCtx context.Context, command string) {
			go func() {
				out := runDirectShell(shellCtx, sess.registry, sess.ag, command, adm.afterShell)
				// Interrupted runs hand a queued message back instead
				// of auto-sending it (ADR-0007 via ADR-0021).
				prog.Send(tui.ShellDone{Output: out, Interrupted: shellCtx.Err() != nil})
			}()
		},
		Compact: func(compactCtx context.Context) {
			go func() {
				note, err := compactNow(compactCtx, sess.ag, sess.msgs, sess.sink)
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
				err := sess.turn(turnCtx, input, func(s string) { prog.Send(tui.TextDelta(s)) })
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
				as.rbRunner.Learn(learnCtx)
				prog.Send(tui.TurnDone{})
			}()
		},
		Slash: func(in string) (string, bool, bool) { return sess.slash(in, ld, md, as) },
	})
	prog = tea.NewProgram(model, tea.WithOutput(sweep))
	sess.surf.setProgram(prog)
	fe.tuiGate.SetProgram(prog)
	ld.screen.SetProgram(prog)
	go sess.resolveWindow()
	cl.push("restore the standard logger", quietStdLog(func(line string) {
		_ = liveLog{get: func() agent.SessionLog { return sess.sessionLog }}.Log("library_log", map[string]any{"line": line})
	}))
	_, err := prog.Run()
	// What the exit says — the session_end hooks run in the cleanups —
	// goes to the terminal: the ended program drops what it is sent
	// (ADR-0124 addendum, Found 5). Nothing else does.
	sess.surf.endTUI()
	return err
}

// runPlain is the plain line REPL, the fallback when stdin or stdout is
// not a terminal.
func runPlain(sess *liveSession, cmd *cobra.Command, notes *startupNotes, rs runSettings, adm admission, ld loaded, md models, fe frontEnd, as assembled, bannerLines []string) error {
	// Plain REPL / one-shot: the banner is printed, the tee is done.
	notes.freeze()
	for _, line := range bannerLines {
		fmt.Fprintln(sess.stderr, line)
	}
	fmt.Fprintf(sess.stderr, "/help for commands, Ctrl+D to quit\n")
	// After the banner: a lookup that fails says so below it, never
	// between its lines.
	go sess.resolveWindow()

	// --- plain REPL loop (non-TTY fallback) ---
	// The argv first message (ADR-0064) runs before the first read,
	// through the same handling a read line gets; the echoed "> line"
	// keeps the terminal record showing what ran. Piped stdin lines
	// follow as they always have: without -p, a pipe is the operator's
	// own input (ADR-0055 §3, ADR-0110).
	pending := rs.initialInput
	for {
		var input string
		if pending != "" {
			input, pending = pending, ""
			fmt.Fprintf(sess.stderr, "\n> %s\n", input)
		} else {
			line, err := fe.reader.Read("\n> ")
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(sess.stderr, sess.msgs.Bye)
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
			_ = runTurnWith(sess.ctx, rs.ladder, func(shellCtx context.Context) error {
				fmt.Fprintln(cmd.OutOrStdout(), runDirectShell(shellCtx, sess.registry, sess.ag, command, adm.afterShell))
				return nil
			})
			continue
		}
		if input == "/settings" {
			writeSettingsTable(sess.stderr, sess.settings.data())
			continue
		}
		if turn, handled, errMsg := expandSkillInput(input, sess.skillsList); handled {
			if errMsg != "" {
				fmt.Fprintln(sess.stderr, errMsg)
				continue
			}
			runErr := runTurnWith(sess.ctx, rs.ladder, func(turnCtx context.Context) error {
				return sess.turn(turnCtx, turn, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
			})
			fmt.Fprintln(cmd.OutOrStdout())
			if runErr != nil && !errors.Is(runErr, errInterrupted) {
				fmt.Fprintf(sess.stderr, "%s%v\n", sess.msgs.ErrorPrefix, runErr)
			}
			continue
		}
		// The plain REPL owns stdin on this goroutine, so the learn pass
		// runs inline: there is no event loop to deadlock, and Ctrl+C
		// reaches it through the shared context.
		if input == "/riskbook learn" {
			as.rbRunner.Learn(sess.ctx)
			continue
		}
		if input == "/compact" {
			// Synchronous here: the plain REPL has no phase machine, and
			// nothing else can be happening while it waits for a line.
			// runTurn makes Ctrl+C interrupt the summariser call rather
			// than kill the process (ADR-0021).
			runErr := runTurnWith(sess.ctx, rs.ladder, func(compactCtx context.Context) error {
				note, err := compactNow(compactCtx, sess.ag, sess.msgs, sess.sink)
				if err == nil {
					fmt.Fprintln(sess.stderr, note)
				}
				return err
			})
			if errors.Is(runErr, errInterrupted) {
				fmt.Fprintln(sess.stderr, sess.msgs.Interrupted)
			} else if runErr != nil {
				fmt.Fprintf(sess.stderr, "%s%v\n", sess.msgs.ErrorPrefix, runErr)
			}
			continue
		}
		if strings.HasPrefix(input, "/") {
			out, _, quit := sess.slash(input, ld, md, as)
			fmt.Fprint(sess.stderr, out)
			if quit {
				return nil
			}
			continue
		}

		// SIGINT cancels the in-flight turn, not the process.
		runErr := runTurnWith(sess.ctx, rs.ladder, func(turnCtx context.Context) error {
			return sess.turn(turnCtx, input, func(s string) { fmt.Fprint(cmd.OutOrStdout(), s) })
		})
		fmt.Fprintln(cmd.OutOrStdout())
		if runErr != nil {
			if errors.Is(runErr, errInterrupted) {
				fmt.Fprintln(sess.stderr, sess.msgs.Interrupted)
				continue
			}
			fmt.Fprintf(sess.stderr, "%s%v\n", sess.msgs.ErrorPrefix, runErr)
		}
	}
}

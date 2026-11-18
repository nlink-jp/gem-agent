package cmd

import (
	"bufio"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/workdir"

	"github.com/spf13/cobra"
)

// The workdirs command is the cleanup half of the ADR-0058 accumulation
// note (ADR-0059): the note tells the operator what earlier sessions
// left behind, and this is the tool it points at. It is a CLI
// subcommand, not a slash command, because freeing disk must not
// require starting a model session.

var flagWorkdirsYes bool

var workdirsCmd = &cobra.Command{
	Use:   "workdirs",
	Short: "List this project's session work directories",
	Long: `List the work directories earlier sessions of this project left
behind — id, age, files, size — newest first.

Nothing is ever deleted automatically; 'workdirs clean' is
the explicit remedy, and it never touches a running session's directory.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runWorkdirsList,
}

var workdirsCleanCmd = &cobra.Command{
	Use:   "clean [session-id|prefix]...",
	Short: "Delete session work directories (asks first)",
	Long: `Delete the named session work directories, or every one whose
session is not currently running when no ids are given. An id may be
given by its first characters (four or more), as for --resume.

The exact list — ids, sizes, total — is printed and confirmed before
anything is removed; EOF or anything but 'y' aborts. A directory that
belongs to a running gem-agent is skipped.`,
	SilenceUsage: true,
	RunE:         runWorkdirsClean,
}

func init() {
	workdirsCleanCmd.Flags().BoolVar(&flagWorkdirsYes, "yes", false, "delete without asking (for scripts; interactive confirmation is the default)")
	workdirsCmd.AddCommand(workdirsCleanCmd)
	rootCmd.AddCommand(workdirsCmd)
}

// workdirsProject resolves the project the same way a session would, so
// the listing shows exactly what the startup note counted.
func workdirsProject() (string, error) {
	return currentProject()
}

func runWorkdirsList(cmd *cobra.Command, _ []string) error {
	projectDir, err := workdirsProject()
	if err != nil {
		return err
	}
	infos, more, err := workdir.List(projectDir, "")
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(infos) == 0 {
		fmt.Fprintln(out, "no session work directories here")
		return nil
	}
	if more {
		fmt.Fprintf(out, "[more than %d session work directories — the listing stopped there]\n", workdir.ListCap)
	}
	sessDir, sessErr := session.DefaultDir()
	tw := tabwriter.NewWriter(out, 2, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tAGE\tFILES\tSIZE\t")
	var total int64
	for _, in := range infos {
		note := ""
		if sessErr == nil && session.InUse(sessDir, projectDir, in.ID) {
			note = "(running)"
		}
		files := fmt.Sprint(in.Files)
		if in.Partial {
			files += "+" // walk cut at WalkCap: a lower bound
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", in.ID, humanAge(in.LastUsed), files, humanBytes(in.Bytes), note)
		total += in.Bytes
	}
	_ = tw.Flush()
	fmt.Fprintf(out, "total: %s in %d dir(s) — delete with 'gem-agent workdirs clean'\n",
		humanBytes(total), len(infos))
	return nil
}

func runWorkdirsClean(cmd *cobra.Command, args []string) error {
	projectDir, err := workdirsProject()
	if err != nil {
		return err
	}
	infos, more, err := workdir.List(projectDir, "")
	if err != nil {
		return err
	}
	sessDir, sessErr := session.DefaultDir()
	live := func(id string) bool {
		return sessErr == nil && session.InUse(sessDir, projectDir, id)
	}

	// Select: the named ids, or everything not running. Naming an id
	// that does not exist is an error, not a silent skip — a cleanup
	// that quietly ignores a typo looks like it worked (ADR-0037's
	// allowlist lesson, applied to deletion).
	var picked []workdir.Info
	if len(args) > 0 {
		stat := func(id string) (workdir.Info, error) { return workdir.Stat(projectDir, id) }
		for _, typed := range args {
			in, err := resolveWorkdir(typed, infos, more, stat)
			if err != nil {
				return err
			}
			if live(in.ID) {
				return fmt.Errorf("session %s is running — not deleting its work directory", in.ID)
			}
			picked = append(picked, in)
		}
	} else {
		if more {
			fmt.Fprintf(cmd.OutOrStdout(), "[more than %d session work directories — this pass covers the first %d; run again for the rest]\n", workdir.ListCap, workdir.ListCap)
		}
		for _, in := range infos {
			if live(in.ID) {
				fmt.Fprintf(cmd.OutOrStdout(), "skipping %s (session is running)\n", in.ID)
				continue
			}
			picked = append(picked, in)
		}
	}
	out := cmd.OutOrStdout()
	if len(picked) == 0 {
		fmt.Fprintln(out, "nothing to clean")
		return nil
	}

	var total int64
	for _, in := range picked {
		fmt.Fprintf(out, "  %s  %s (%d file(s))\n", in.ID, humanBytes(in.Bytes), in.Files)
		total += in.Bytes
	}
	fmt.Fprintf(out, "delete %d dir(s), %s in total? [y/N] ",
		len(picked), humanBytes(total))
	if !flagWorkdirsYes {
		// Deny on EOF and on a non-TTY, the approval gate's stance: a
		// pipe that says nothing has not said yes — and a pipe that says
		// "y" has not either (ADR-0059: a non-TTY run consents only
		// through --yes; review round 4 found the pipe was accepted).
		if !confirmYes(cmd.InOrStdin(), workdirsStdinIsTerminal(cmd.InOrStdin())) {
			fmt.Fprintln(out, "aborted — nothing deleted (pass --yes to consent without a terminal)")
			return nil
		}
	} else {
		fmt.Fprintln(out, "y (--yes)")
	}

	for _, in := range picked {
		if err := workdir.Remove(projectDir, in.ID); err != nil {
			return fmt.Errorf("remove %s: %w", in.ID, err)
		}
		fmt.Fprintf(out, "deleted %s\n", in.ID)
	}
	return nil
}

// resolveWorkdir finds the work directory an operator named: a full
// session id, or its leading characters as --resume takes them (four
// or more, hex and hyphens) — `gem-agent sessions` shows eight. A
// full id not in the listing, which may have been cut, is looked up
// directly. A prefix must name exactly one directory, and is refused
// on a cut listing, where the one it found may not be the only one.
// The ids picked are printed in full and confirmed before anything is
// removed.
func resolveWorkdir(typed string, infos []workdir.Info, cut bool, stat func(string) (workdir.Info, error)) (workdir.Info, error) {
	for _, in := range infos {
		if in.ID == typed {
			return in, nil
		}
	}
	if st, err := stat(typed); err == nil {
		return st, nil
	}
	if !session.ValidPrefix(typed) {
		return workdir.Info{}, fmt.Errorf("no work directory for session %s here", typed)
	}
	var matches []workdir.Info
	for _, in := range infos {
		if strings.HasPrefix(in.ID, typed) {
			matches = append(matches, in)
		}
	}
	switch {
	case len(matches) == 0 && cut:
		return workdir.Info{}, fmt.Errorf("no work directory starts with %q among the first %d listed — type the full id", typed, workdir.ListCap)
	case len(matches) == 0:
		return workdir.Info{}, fmt.Errorf("no work directory for session %s here", typed)
	case len(matches) > 1:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		sort.Strings(ids)
		return workdir.Info{}, fmt.Errorf("%q matches %d work directories (%s); type more characters", typed, len(matches), strings.Join(ids, ", "))
	case cut:
		return workdir.Info{}, fmt.Errorf("%q matches %s among the first %d listed, but the listing was cut — type the full id", typed, matches[0].ID, workdir.ListCap)
	}
	return matches[0], nil
}

// confirmYes reads one line and accepts only an explicit yes.
func confirmYes(in io.Reader, interactive bool) bool {
	if !interactive {
		return false
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// humanAge renders how long ago t was, coarsely — the listing is for
// telling last week's session from this morning's, not for timing.
func humanAge(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// workdirsStdinIsTerminal decides whether a typed answer counts; tests
// that exercise the typed path replace it, since a buffer is not a
// terminal.
var workdirsStdinIsTerminal = stdinIsTerminal

// stdinIsTerminal reports whether in is a terminal.
func stdinIsTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

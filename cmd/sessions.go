package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/session"

	"github.com/spf13/cobra"
)

var flagSessionsAll bool

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "List resumable sessions for this project",
	Long: `List the recorded sessions for the current project, newest first.

Resume one with --resume <id>, or the most recent with --continue.
Sessions from other project directories are not listed: a session
resumes only into the directory it was recorded in (ADR-0005).`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runSessions,
}

func init() {
	sessionsCmd.Flags().BoolVar(&flagSessionsAll, "all", false, "list sessions from every project, not just this one")
	rootCmd.AddCommand(sessionsCmd)
}

func runSessions(cmd *cobra.Command, args []string) error {
	dir, err := session.DefaultDir()
	if err != nil {
		return err
	}
	projectDir := ""
	if !flagSessionsAll {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		if projectDir, err = sandbox.ResolveWriteDir(cwd); err != nil {
			return err
		}
	}
	metas, err := session.List(dir, projectDir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(metas) == 0 {
		if flagSessionsAll {
			fmt.Fprintf(out, "no sessions recorded yet (%s)\n", dir)
			return nil
		}
		fmt.Fprintf(out, "no sessions recorded for %s\n(gem-agent sessions --all lists every project)\n", projectDir)
		return nil
	}
	writeSessions(out, metas, flagSessionsAll)
	return nil
}

// writeSessions renders the listing. Separate from runSessions so the
// formatting is testable without a home directory or a cwd.
func writeSessions(out io.Writer, metas []session.Meta, showProject bool) {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, m := range metas {
		row := []string{m.ID, ago(m.LastActive), humanBytes(m.Size), m.Header.Model}
		if showProject {
			row = append(row, abbreviateHome(m.Header.Project))
		}
		row = append(row, m.Preview)
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	tw.Flush()
	fmt.Fprintln(out, "\nresume with:  gem-agent --resume <id>   (or --continue for the most recent)")
}

// resolveResume finds the session --continue / --resume names and loads
// it, refusing rather than warning when it does not belong here.
//
// Both refusals are deliberate (ADR-0005): a transcript replayed in the
// wrong tree describes files that are not there, and thought signatures
// are model-bound opaque tokens with no basis for cross-model replay.
// Each message names what to do instead.
func resolveResume(dir, projectDir, model, id string) (session.Meta, error) {
	var meta session.Meta
	if id == "" {
		latest, ok, err := session.Latest(dir, projectDir)
		if err != nil {
			return meta, err
		}
		if !ok {
			return meta, fmt.Errorf("no previous session recorded for %s — start one without --continue", projectDir)
		}
		meta = latest
	} else {
		if !session.ValidID(id) {
			return meta, fmt.Errorf("%q is not a session id; `gem-agent sessions` lists them (ids look like 20260819-150102)", id)
		}
		found, err := session.Find(dir, projectDir, id)
		if err != nil {
			return meta, fmt.Errorf("no session %s in %s — `gem-agent sessions` lists what is there", id, dir)
		}
		meta = found
	}

	if meta.Header.Project != "" && meta.Header.Project != projectDir {
		return meta, fmt.Errorf("session %s was recorded in %s, not %s — its history describes that project's files; run gem-agent there to resume it",
			meta.ID, meta.Header.Project, projectDir)
	}
	if meta.Header.Model != "" && meta.Header.Model != model {
		return meta, fmt.Errorf("session %s was recorded with %s and this run uses %s — a conversation cannot move between models (the replayed reasoning tokens are model-bound); resume it with --model %s",
			meta.ID, meta.Header.Model, model, meta.Header.Model)
	}

	// The history itself is NOT loaded here: loading before Reopen's
	// flock left a window where another process's final appends landed
	// between our read and our lock — we then appended after records
	// we never replayed (review round 2). loadResumedHistory runs
	// under the lock.
	return meta, nil
}

// loadResumedHistory reads the resumed transcript UNDER the flock that
// Reopen already holds — path comes from the open Logger, so the file
// read is exactly the file being appended to.
func loadResumedHistory(lg *session.Logger, id string) ([]llm.Message, []string, error) {
	history, _, skipped, err := session.Load(lg.Path())
	if err != nil {
		return nil, nil, err
	}
	if len(history) == 0 {
		return nil, nil, fmt.Errorf("session %s has no conversation to resume", id)
	}
	var notes []string
	if skipped > 0 {
		// A shorter conversation that says nothing looks complete; the
		// count is the honest version (ADR-0021).
		notes = append(notes, fmt.Sprintf("session %s: %d unreadable line(s) skipped — likely a torn write from a crash; the rest of the conversation is intact", id, skipped))
	}
	return history, notes, nil
}

// openSessionLog starts a new transcript, or reopens the one being
// resumed. A resumed session appends to its own file: one file is one
// conversation, however many processes it took (ADR-0005).
func openSessionLog(dir, resumeID, projectDir, model, location, version string) (*session.Logger, error) {
	if resumeID != "" {
		lg, err := session.Reopen(dir, projectDir, resumeID)
		if err != nil {
			return nil, err
		}
		// Best-effort marker: a transcript that shows where a resume
		// happened is far easier to read back than one that does not.
		_ = lg.Log(session.KindResumed, map[string]string{"version": version})
		return lg, nil
	}
	lg, err := session.Open(dir, projectDir)
	if err != nil {
		return nil, err
	}
	if err := lg.Log(session.KindHeader, session.Header{
		Schema:   session.SchemaVersion,
		Version:  version,
		Model:    model,
		Project:  projectDir,
		Location: location,
	}); err != nil {
		lg.Close()
		return nil, err
	}
	return lg, nil
}

// ago renders a coarse age; a session list is scanned, not read.
func ago(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

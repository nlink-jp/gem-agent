package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/workdir"

	"github.com/spf13/cobra"
)

// workdirsFixture isolates the state root, chdirs into a scratch
// project, and seeds two earlier session work directories.
func workdirsFixture(t *testing.T) (projectDir string) {
	t.Helper()
	t.Setenv(statedir.EnvRoot, t.TempDir())
	// Resolved, as the command resolves cwd (macOS t.TempDir sits under
	// a symlinked /var; an unresolved seed would land in a different
	// escaped state subdirectory than the one the command reads).
	project, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	for _, id := range []string{"20260830-000001", "20260830-000002"} {
		dir, err := workdir.Ensure(project, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "spill.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return project
}

// asTerminal makes the typed confirmation count for one test: the
// harness feeds a buffer, which the real check refuses (ADR-0059).
func asTerminal(t *testing.T) {
	t.Helper()
	prev := workdirsStdinIsTerminal
	workdirsStdinIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { workdirsStdinIsTerminal = prev })
}

// run one cobra command fresh, with the given stdin, capturing stdout.
func runWorkdirs(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	cmd.SetIn(strings.NewReader(stdin))
	var err error
	if len(args) > 0 && args[0] == "clean" {
		err = runWorkdirsClean(cmd, args[1:])
	} else {
		err = runWorkdirsList(cmd, nil)
	}
	if err != nil {
		t.Fatalf("workdirs %v: %v", args, err)
	}
	return out.String()
}

func TestWorkdirsListShowsWhatTheNoteCounted(t *testing.T) {
	workdirsFixture(t)
	out := runWorkdirs(t, "")
	if !strings.Contains(out, "20260830-000001") || !strings.Contains(out, "20260830-000002") {
		t.Errorf("listing incomplete:\n%s", out)
	}
	if !strings.Contains(out, "workdirs clean") {
		t.Errorf("the listing must point at the remedy:\n%s", out)
	}
}

// The confirmation is the whole safety story: an explicit y deletes,
// and EOF — a pipe that said nothing — deletes nothing.
func TestWorkdirsCleanAsksAndEOFMeansNo(t *testing.T) {
	project := workdirsFixture(t)
	flagWorkdirsYes = false

	out := runWorkdirs(t, "" /* EOF */, "clean")
	if !strings.Contains(out, "aborted") {
		t.Fatalf("EOF must abort:\n%s", out)
	}
	if infos, _, _ := workdir.List(project, ""); len(infos) != 2 {
		t.Fatalf("EOF deleted something: %d dirs left", len(infos))
	}

	// A "y" arriving through a pipe is not consent (ADR-0059: a non-TTY
	// run consents only through --yes; review round 4).
	out = runWorkdirs(t, "y\n", "clean")
	if !strings.Contains(out, "aborted") || !strings.Contains(out, "--yes") {
		t.Fatalf("piped y must abort and name --yes:\n%s", out)
	}
	if infos, _, _ := workdir.List(project, ""); len(infos) != 2 {
		t.Fatalf("piped y deleted something: %d dirs left", len(infos))
	}

	asTerminal(t)
	out = runWorkdirs(t, "y\n", "clean")
	if !strings.Contains(out, "deleted 20260830-000001") || !strings.Contains(out, "deleted 20260830-000002") {
		t.Fatalf("y should delete both:\n%s", out)
	}
	if infos, _, _ := workdir.List(project, ""); len(infos) != 0 {
		t.Fatalf("%d dirs survived a confirmed clean", len(infos))
	}
}

// A running session's directory is never deleted: bare clean skips it,
// and naming it outright is an error, not a skip.
func TestWorkdirsCleanNeverTouchesALiveSession(t *testing.T) {
	project := workdirsFixture(t)
	flagWorkdirsYes = false

	sessDir, err := session.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	lg, err := session.Open(sessDir, project)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lg.Close() }()
	liveDir, err := workdir.Ensure(project, lg.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveDir, "in-flight.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	asTerminal(t)
	out := runWorkdirs(t, "y\n", "clean")
	if !strings.Contains(out, "skipping "+lg.ID()) {
		t.Errorf("live session not announced as skipped:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(liveDir, "in-flight.txt")); err != nil {
		t.Fatalf("a live session's work directory was deleted: %v", err)
	}

	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader("y\n"))
	if err := runWorkdirsClean(cmd, []string{lg.ID()}); err == nil {
		t.Error("naming a live session must be an error, not a silent skip")
	}
}

// A typo'd id fails loudly — a cleanup that quietly ignores it looks
// like it worked.
func TestWorkdirsCleanRefusesAnUnknownID(t *testing.T) {
	workdirsFixture(t)
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetIn(strings.NewReader("y\n"))
	if err := runWorkdirsClean(cmd, []string{"20991231-235959"}); err == nil {
		t.Error("an unknown id must be an error")
	}
}

// An id may be named by its leading characters, as --resume takes it:
// `gem-agent sessions` shows eight, and the full id was the only form
// clean accepted.
func TestResolveWorkdirByPrefix(t *testing.T) {
	a := workdir.Info{ID: "3f2a9c1e-0b7d-4e21-9a3c-5d6e7f809a1b"}
	b := workdir.Info{ID: "3f2a1111-0b7d-4e21-9a3c-5d6e7f809a1b"}
	c := workdir.Info{ID: "77aa0000-0b7d-4e21-9a3c-5d6e7f809a1b"}
	d := workdir.Info{ID: "20250101-120000-2"}
	infos := []workdir.Info{a, b, c, d}
	far := workdir.Info{ID: "99bb0000-0b7d-4e21-9a3c-5d6e7f809a1b"}
	stat := func(id string) (workdir.Info, error) {
		if id == far.ID {
			return far, nil
		}
		return workdir.Info{}, os.ErrNotExist
	}

	for _, tc := range []struct {
		typed, want, errHas string
		cut                 bool
	}{
		{typed: a.ID, want: a.ID},
		{typed: "3f2a9c1e", want: a.ID},
		{typed: "77aa", want: c.ID},
		{typed: far.ID, want: far.ID, cut: true},
		{typed: "3f2a", errHas: "matches 2 work directories"},
		{typed: "77a", errHas: "no work directory for session 77a"},
		{typed: "zzzz", errHas: "no work directory for session zzzz"},
		{typed: "4444", errHas: "no work directory for session 4444"},
		{typed: "4444", cut: true, errHas: "among the first"},
		{typed: "77aa", cut: true, errHas: "listing was cut"},
		// A full id whose directory is gone names nothing, even when it
		// is a prefix of another (the same second's "-2"), and a full id
		// beyond a cut listing is not called a prefix.
		{typed: "20250101-120000", errHas: "no work directory for session 20250101-120000"},
		{typed: "4444aaaa-0b7d-4e21-9a3c-5d6e7f809a1b", cut: true, errHas: "no work directory for session 4444aaaa"},
	} {
		got, err := resolveWorkdir(tc.typed, infos, tc.cut, stat)
		if tc.errHas != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errHas) {
				t.Errorf("%q (cut=%v): err = %v, want it to say %q", tc.typed, tc.cut, err, tc.errHas)
			}
			continue
		}
		if err != nil || got.ID != tc.want {
			t.Errorf("%q (cut=%v) = %q, %v; want %q", tc.typed, tc.cut, got.ID, err, tc.want)
		}
	}
}

// The eight characters `gem-agent sessions` shows are enough to clean
// one session's directory, and the confirmation names it in full.
func TestWorkdirsCleanTakesAPrefix(t *testing.T) {
	project := workdirsFixture(t)
	const id = "3f2a9c1e-0b7d-4e21-9a3c-5d6e7f809a1b"
	if _, err := workdir.Ensure(project, id); err != nil {
		t.Fatal(err)
	}
	flagWorkdirsYes = true
	t.Cleanup(func() { flagWorkdirsYes = false })

	out := runWorkdirs(t, "", "clean", id[:8])
	if !strings.Contains(out, "  "+id+"  ") || !strings.Contains(out, "deleted "+id) {
		t.Fatalf("the prefix did not resolve to the full id:\n%s", out)
	}
	if infos, _, _ := workdir.List(project, ""); len(infos) != 2 {
		t.Fatalf("%d dirs left, want the other two", len(infos))
	}
}

// A directory named twice — by a prefix and by its full id — is listed
// and counted once.
func TestWorkdirsCleanCountsADirectoryOnce(t *testing.T) {
	project := workdirsFixture(t)
	const id = "3f2a9c1e-0b7d-4e21-9a3c-5d6e7f809a1b"
	if _, err := workdir.Ensure(project, id); err != nil {
		t.Fatal(err)
	}
	flagWorkdirsYes = true
	t.Cleanup(func() { flagWorkdirsYes = false })

	out := runWorkdirs(t, "", "clean", id[:8], id)
	if !strings.Contains(out, "delete 1 dir(s)") || strings.Count(out, "deleted "+id) != 1 {
		t.Fatalf("a directory named twice was counted twice:\n%s", out)
	}
}

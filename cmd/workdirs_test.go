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

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// underHome makes a directory under the real home for the duration of
// the test: an added root may not sit in a shared temporary directory,
// which is where t.TempDir puts everything.
func underHome(t *testing.T, name string) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	base, err := os.MkdirTemp(home, ".gem-agent-added-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	real, _ := sandbox.ResolveWriteDir(dir)
	return real
}

func withDirFlags(t *testing.T, write, read []string) {
	t.Helper()
	oldW, oldR := flagAddDir, flagReadDir
	flagAddDir, flagReadDir = write, read
	t.Cleanup(func() { flagAddDir, flagReadDir = oldW, oldR })
}

// ADR-0099 §3: the configuration's lists and the flags join, writes
// before reads, configuration before flags; a refusal names where the
// path came from and stops the start.
func TestResolveAddedDirs(t *testing.T) {
	notes := underHome(t, "notes")
	ref := underHome(t, "ref")
	project := underHome(t, "proj")
	cfg := &config.Config{Directories: config.DirectoriesConfig{Write: []string{notes}}}
	sub := filepath.Join(notes, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	withDirFlags(t, nil, []string{ref, sub})
	_, err := resolveAddedDirs(cfg, project, "", "")
	if err == nil || !strings.Contains(err.Error(), "--read-dir") || !strings.Contains(err.Error(), "already covered") {
		t.Fatalf("a read root inside a write root: %v", err)
	}
	// The same directory twice is one root; read then write is the write.
	withDirFlags(t, []string{notes}, []string{ref})
	if got, err := resolveAddedDirs(cfg, project, "", ""); err != nil || len(got) != 2 {
		t.Errorf("a duplicate --add-dir: %v %+v", err, got)
	}
	upgrade := &config.Config{Directories: config.DirectoriesConfig{Read: []string{notes}}}
	withDirFlags(t, []string{filepath.Join(notes, "..", "notes")}, nil)
	if got, err := resolveAddedDirs(upgrade, project, "", ""); err != nil || len(got) != 1 || !got[0].Write || got[0].Source != "flag" {
		t.Errorf("a configured read root raised to write by flag: %v %+v", err, got)
	}
	// A write root may not hold the configuration file in use.
	withDirFlags(t, []string{ref}, nil)
	if _, err := resolveAddedDirs(&config.Config{}, project, "", filepath.Join(ref, "gem-agent.toml")); err == nil || !strings.Contains(err.Error(), "configuration file in use") {
		t.Errorf("--add-dir holding --config: %v", err)
	}
	withDirFlags(t, nil, []string{ref})
	got, err := resolveAddedDirs(cfg, project, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Dir != notes || !got[0].Write || got[0].Source != "config" || got[1].Dir != ref || got[1].Write || got[1].Source != "flag" {
		t.Errorf("resolved = %+v", got)
	}
	rec := addedDirsRecord(got)["roots"].([]map[string]string)
	if rec[0]["access"] != "write" || rec[1]["access"] != "read" || rec[1]["source"] != "flag" {
		t.Errorf("record = %v", rec)
	}
	home, _ := os.UserHomeDir()
	withDirFlags(t, []string{home}, nil)
	if _, err := resolveAddedDirs(&config.Config{}, project, "", ""); err == nil || !strings.Contains(err.Error(), "--add-dir") || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("--add-dir ~: %v", err)
	}
	withDirFlags(t, nil, nil)
	if got, err := resolveAddedDirs(&config.Config{}, project, "", ""); err != nil || len(got) != 0 {
		t.Errorf("no added directories: %v %v", got, err)
	}
}

// The system prompt names each added root with its level and says its
// instruction files are data (ADR-0099 §2/§6); with none it is unchanged.
func TestSystemPromptNamesAddedDirectories(t *testing.T) {
	without := buildSystemPrompt("/proj", "", "", sessionDates{})
	if strings.Contains(without, "Added directories") {
		t.Error("a prompt with no added roots names some")
	}
	with := buildSystemPrompt("/proj", "", "", sessionDates{}, sandbox.AddedRoot{Dir: "/Users/op/notes", Write: true}, sandbox.AddedRoot{Dir: "/Volumes/A"})
	for _, want := range []string{"/Users/op/notes (write)", "/Volumes/A (read)", "nothing may write it", "not loaded and are data"} {
		if !strings.Contains(with, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
}

// buildExecFn carries the added roots into the real lanes and probes
// them; /clear's rebuild (rotateWorkDir) keeps them.
func TestAddedRootsReachTheLanesAndSurviveClear(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	project := t.TempDir()
	notes := underHome(t, "notes")
	ref := underHome(t, "ref")
	added := []sandbox.AddedRoot{{Dir: notes, Write: true}, {Dir: ref}}
	execFn, enf, _, err := buildExecFn(true, project, "", nil, nil, nil, added)
	if err != nil {
		t.Fatalf("buildExecFn with added roots: %v", err)
	}
	if !enf.Confined {
		t.Skip("sandbox unverified here")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	check := func(fn tools.LaneExecFunc, when string) {
		if err := fn(ctx, "echo ok > "+filepath.Join(notes, "w.txt"), sandbox.LaneWrite).Run(); err != nil {
			t.Errorf("%s: the write lane could not write a write root: %v", when, err)
		}
		if err := fn(ctx, "echo bad > "+filepath.Join(ref, "w.txt"), sandbox.LaneOperator).Run(); err == nil {
			t.Errorf("%s: the operator lane wrote a read root", when)
		}
	}
	check(execFn, "startup")
	registry, err := tools.New(project, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddDirs(added); err != nil {
		t.Fatal(err)
	}
	shellExec := &liveExec{fn: execFn}
	work := t.TempDir()
	notesOut := rotateWorkDir(registry, shellExec, true, project, work, nil, nil, false, nil, registry.AddedDirs(), nil)
	for _, n := range notesOut {
		if strings.Contains(n, "keep the previous") {
			t.Errorf("rotation failed: %s", n)
		}
	}
	check(shellExec.run, "after /clear")
}

// A root that fails its probes stops the start rather than degrading
// (ADR-0099 §7): a write root the process cannot write is one.
func TestAddedWriteRootThatCannotBeWrittenIsAnError(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	locked := underHome(t, "locked")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, _, _, err := buildExecFn(true, t.TempDir(), "", nil, nil, nil, []sandbox.AddedRoot{{Dir: locked, Write: true}})
	if err == nil || !strings.Contains(err.Error(), locked) {
		t.Errorf("an unwritable write root: %v", err)
	}
}

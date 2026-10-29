package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// newTestGranter builds the grant path over a real sandbox the way
// runREPL does: a registry, a live runner verified with the given
// startup roots, the watch and a record sink.
func newTestGranter(t *testing.T, startup []sandbox.AddedRoot, readLanePrompts bool) (*dirGranter, *[]map[string]any, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	t.Setenv(statedir.EnvRoot, t.TempDir())
	project := underHome(t, "proj")
	execFn, enf, _, err := buildExecFn(true, project, "", nil, nil, nil, startup)
	if err != nil || !enf.Confined {
		t.Skipf("sandbox unverified here: %v", err)
	}
	if readLanePrompts {
		enf.ReadLane = false
	}
	registry, err := tools.New(project, nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	shellExec := &liveExec{fn: execFn}
	registry.SetLaneExec(shellExec.run, enf)
	if err := registry.AddDirs(startup); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var records []map[string]any
	g := &dirGranter{
		registry: registry, shellExec: shellExec, sandboxOn: true, projectDir: project,
		workDir: func() string { return "" }, readLanePrompts: readLanePrompts,
		parents: func() []string { return nil }, base: rootBaseContext(""),
		watch: newAddedWatch(nil, project, &out, uitext.For(uitext.EN)), sources: map[string]string{},
		log: func(kind string, data any) error {
			if kind == "added_directories" {
				records = append(records, data.(map[string]any))
			}
			return nil
		},
	}
	for _, a := range startup {
		g.sources[a.Dir] = "flag"
	}
	return g, &records, &out
}

func runLane(t *testing.T, g *dirGranter, lane sandbox.Lane, command string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return g.shellExec.run(ctx, command, lane).Run()
}

// An upgrade takes effect everywhere (ADR-0099 §12): the file tools,
// the verdict and the lanes write the root that was read-only.
func TestGrantUpgradesAReadRoot(t *testing.T) {
	ref := underHome(t, "ref")
	g, records, _ := newTestGranter(t, []sandbox.AddedRoot{{Dir: ref}}, false)
	target := filepath.Join(ref, "n.md")
	if v := risk.Classify("write_file", true, map[string]any{"path": target}, g.projectDir, "", g.registry.AddedDirs()...); v.Tier != risk.Block {
		t.Fatalf("before the upgrade the verdict is %v", v)
	}
	note, err := g.grant(context.Background(), ref, true, "slash", g.currentEpoch(), nil)
	if err != nil || !strings.Contains(note, "(write)") {
		t.Fatalf("upgrade: %q %v", note, err)
	}
	if got := g.registry.AddedDirs(); len(got) != 1 || !got[0].Write {
		t.Fatalf("roots after the upgrade = %+v", got)
	}
	if v := risk.Classify("write_file", true, map[string]any{"path": target}, g.projectDir, "", g.registry.AddedDirs()...); v.Tier != risk.Safe {
		t.Errorf("after the upgrade the verdict is %v", v)
	}
	if err := runLane(t, g, sandbox.LaneWrite, "echo x > "+shellQuoteTest(target)); err != nil {
		t.Errorf("the write lane cannot write the upgraded root: %v", err)
	}
	if len(*records) != 1 || (*records)[0]["granted"].(map[string]string)["source"] != "slash" {
		t.Errorf("records = %v", *records)
	}
	if !g.promptStale.Load() {
		t.Error("the prompt is not marked for a rebuild")
	}
}

// A write root over a read root absorbs it: one entry remains, the
// write, and the note names the absorbed one.
func TestGrantAbsorbsAReadRootInside(t *testing.T) {
	outer := underHome(t, "outer")
	inner := filepath.Join(outer, "inner")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	g, _, _ := newTestGranter(t, []sandbox.AddedRoot{{Dir: inner}}, false)
	note, err := g.grant(context.Background(), outer, true, "slash", g.currentEpoch(), nil)
	if err != nil || !strings.Contains(note, inner+" is now part of it") {
		t.Fatalf("absorb: %q %v", note, err)
	}
	if got := g.registry.AddedDirs(); len(got) != 1 || got[0].Dir != outer || !got[0].Write {
		t.Errorf("roots = %+v", got)
	}
	if err := runLane(t, g, sandbox.LaneWrite, "echo x > "+shellQuoteTest(filepath.Join(inner, "w"))); err != nil {
		t.Errorf("the absorbed directory is not writable: %v", err)
	}
	// Covered: a read root inside the write root changes nothing.
	note, err = g.grant(context.Background(), inner, false, "slash", g.currentEpoch(), nil)
	if err != nil || !strings.Contains(note, "already covered") {
		t.Errorf("covered: %q %v", note, err)
	}
}

// A failed verification leaves the runner, the registry, the prompt and
// the watch as they were (§4, §12): a write root this process cannot
// write fails its probes.
func TestGrantFailedVerificationChangesNothing(t *testing.T) {
	g, records, _ := newTestGranter(t, nil, false)
	locked := underHome(t, "locked")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	_, err := g.grant(context.Background(), locked, true, "slash", g.currentEpoch(), nil)
	if err == nil {
		t.Fatal("a root that fails its probes was added")
	}
	if len(g.registry.AddedDirs()) != 0 || len(*records) != 0 || g.promptStale.Load() || len(g.watch.roots) != 0 {
		t.Errorf("state changed: roots %v records %v stale %v watch %v", g.registry.AddedDirs(), *records, g.promptStale.Load(), g.watch.roots)
	}
	// The runner kept its profile: the write lane still cannot write the
	// locked directory's parent's sibling — nothing was added for it.
	if err := runLane(t, g, sandbox.LaneWrite, "echo x > "+shellQuoteTest(filepath.Join(filepath.Dir(locked), "sibling"))); err == nil {
		t.Error("the runner widened after a failed grant")
	}
	if g.registry.Enforcement() != (sandbox.Enforcement{Confined: true, ReadLane: true}) {
		t.Errorf("enforcement = %+v", g.registry.Enforcement())
	}
}

// A cancelled call and a grant from a session /clear has moved on from
// do not commit (§12 step 3).
func TestGrantDroppedWhenCancelledOrStale(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, false)
	notes := underHome(t, "notes")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.grant(ctx, notes, true, "request", g.currentEpoch(), nil); !errors.Is(err, errDropped) {
		t.Errorf("a cancelled grant: %v", err)
	}
	stale := g.currentEpoch()
	g.rotate(func() {})
	if _, err := g.grant(context.Background(), notes, true, "request", stale, nil); !errors.Is(err, errDropped) {
		t.Errorf("a late grant after /clear: %v", err)
	}
	if len(g.registry.AddedDirs()) != 0 {
		t.Errorf("a dropped grant committed: %v", g.registry.AddedDirs())
	}
	if _, err := g.grant(context.Background(), notes, true, "request", g.currentEpoch(), nil); err != nil {
		t.Errorf("a current grant: %v", err)
	}
}

// The read-lane opt-out survives a grant: the grant never turns the
// read lane on (§12 step 4).
func TestGrantKeepsTheReadLaneOptOut(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, true)
	notes := underHome(t, "notes")
	if _, err := g.grant(context.Background(), notes, true, "slash", g.currentEpoch(), nil); err != nil {
		t.Fatal(err)
	}
	if g.registry.Enforcement().ReadLane {
		t.Error("a grant switched the read lane on")
	}
}

// A path that resolves through a link names its real path and adds
// nothing; the real path adds (§11). The note and record name the real
// path; the record of all roots carries each source.
func TestGrantThroughALinkAsksForTheRealPath(t *testing.T) {
	g, records, _ := newTestGranter(t, nil, false)
	real := underHome(t, "docs")
	link := filepath.Join(g.projectDir, "notes")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	_, err := g.grant(context.Background(), "notes", true, "slash", g.currentEpoch(), nil)
	if err == nil || !strings.Contains(err.Error(), real) {
		t.Fatalf("a link: %v", err)
	}
	if len(g.registry.AddedDirs()) != 0 {
		t.Fatal("a link was added")
	}
	if _, err := g.grant(context.Background(), real, true, "slash", g.currentEpoch(), nil); err != nil {
		t.Fatal(err)
	}
	ref := underHome(t, "ref")
	if _, err := g.grant(context.Background(), ref, false, "request", g.currentEpoch(), map[string]any{"reason": "the task reads it"}); err != nil {
		t.Fatal(err)
	}
	last := (*records)[len(*records)-1]
	if last["reason"] != "the task reads it" {
		t.Errorf("the request record = %v", last)
	}
	rec := g.record()["roots"].([]map[string]string)
	if len(rec) != 2 || rec[0]["source"] != "slash" || rec[1]["source"] != "request" || rec[1]["access"] != "read" {
		t.Errorf("the /clear record = %v", rec)
	}
	// RefuseRoot still decides: the home is refused.
	home, _ := os.UserHomeDir()
	if _, err := g.grant(context.Background(), home, true, "slash", g.currentEpoch(), nil); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("home: %v", err)
	}
}

// A write root granted mid-session joins the watch from the grant
// (§10): a file moved in after it is reported at /clear.
func TestGrantedWriteRootIsWatched(t *testing.T) {
	g, _, out := newTestGranter(t, nil, false)
	notes := underHome(t, "notes")
	if _, err := g.grant(context.Background(), notes, true, "slash", g.currentEpoch(), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(notes, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notes, "d", "AGENTS.md"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g.watch.report("clear", nil)
	if !strings.Contains(out.String(), "d/AGENTS.md (added)") {
		t.Errorf("the granted root was not watched: %q", out.String())
	}
}

// The model's entrance: registered with the operator-only verdict, and
// not in the delegated search agent's registry.
func TestRequestDirectoryToolAndSubset(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, false)
	if err := registerRequestDirectory(g.registry, g); err != nil {
		t.Fatal(err)
	}
	tool, ok := g.registry.Get(requestDirectoryName)
	if !ok || !tool.Mutating {
		t.Fatalf("tool = %+v %v", tool, ok)
	}
	// The search agent's registry is built from an explicit list
	// (searchAgentTools); request_directory is not on it.
	for _, n := range searchAgentTools {
		if n == requestDirectoryName {
			t.Fatal("searchAgentTools lists request_directory")
		}
	}
	sub, err := g.registry.Subset("list_files", "read_file")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := sub.Get(requestDirectoryName); ok {
		t.Error("the delegated search agent can request a directory")
	}
	notes := underHome(t, "notes")
	out, err := tool.Run(context.Background(), map[string]any{"path": notes, "access": "read", "reason": "r"})
	if err != nil || !strings.Contains(out, "(read)") {
		t.Errorf("run: %q %v", out, err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"path": notes, "access": "all", "reason": "r"}); err == nil {
		t.Error("an unknown access was accepted")
	}
}

func shellQuoteTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// §11's grammar: the rest of the line is the path, spaces and all; a
// bare /add-dir, /read-dir or /dirs lists the roots.
func TestAddDirSlashGrammar(t *testing.T) {
	var gotRest string
	var gotWrite bool
	reload := slashReloads{
		addDir: func(rest string, write bool) (string, bool) { gotRest, gotWrite = rest, write; return "added", false },
		dirs:   func() string { return "the list" },
	}
	msgs := uitext.For(uitext.EN)
	run := func(in string) string {
		out, _, _ := slashOutput(in, nil, nil, nil, nil, reload, nil, nil, nil, "", msgs, nil)
		return out
	}
	if out := run("/add-dir   ~/My Notes/2026  "); out != "added" || gotRest != "~/My Notes/2026" || !gotWrite {
		t.Errorf("/add-dir: %q rest=%q write=%v", out, gotRest, gotWrite)
	}
	if out := run("/read-dir /Volumes/A B"); out != "added" || gotRest != "/Volumes/A B" || gotWrite {
		t.Errorf("/read-dir: %q rest=%q write=%v", out, gotRest, gotWrite)
	}
	for _, in := range []string{"/add-dir", "/read-dir  ", "/dirs"} {
		if out := run(in); out != "the list" {
			t.Errorf("%q = %q, want the list", in, out)
		}
	}
}

// The typed case is not what is judged or installed (review of step 2,
// #1): a case variant lands as the volume's name.
func TestGrantJudgesTheVolumesName(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, false)
	notes := underHome(t, "notes")
	upper := strings.ToUpper(notes)
	if _, err := os.Stat(upper); err != nil {
		t.Skip("a case-sensitive volume")
	}
	if _, err := g.grant(context.Background(), upper, true, "slash", g.currentEpoch(), nil); err != nil {
		t.Fatal(err)
	}
	if got := g.registry.AddedDirs(); len(got) != 1 || got[0].Dir != notes {
		t.Errorf("installed %+v, want %s", got, notes)
	}
	home, _ := os.UserHomeDir()
	if _, err := g.grant(context.Background(), strings.ToUpper(home), true, "slash", g.currentEpoch(), nil); err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Errorf("the home in capitals: %v", err)
	}
}

// A grant cancelled while its probes run does not commit (#3), and
// /clear racing grants leaves one consistent state (#2; run with -race).
func TestGrantCancelledMidVerificationAndClearRace(t *testing.T) {
	g, _, _ := newTestGranter(t, nil, false)
	notes := underHome(t, "notes")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.grant(ctx, notes, true, "request", g.currentEpoch(), nil)
		done <- err
	}()
	cancel()
	if err := <-done; !errors.Is(err, errDropped) {
		t.Errorf("a cancelled grant: %v", err)
	}
	if len(g.registry.AddedDirs()) != 0 {
		t.Fatalf("a cancelled grant committed: %v", g.registry.AddedDirs())
	}
	dirs := []string{underHome(t, "a"), underHome(t, "b"), underHome(t, "c")}
	results := make(chan error, len(dirs))
	for _, d := range dirs {
		d := d
		epoch := g.currentEpoch()
		go func() {
			_, err := g.grant(context.Background(), d, false, "request", epoch, nil)
			results <- err
		}()
	}
	for i := 0; i < 3; i++ {
		g.rotate(func() { _ = g.record() })
	}
	committed := 0
	for range dirs {
		if err := <-results; err == nil {
			committed++
		} else if !errors.Is(err, errDropped) {
			t.Errorf("a racing grant: %v", err)
		}
	}
	if got := len(g.registry.AddedDirs()); got != committed {
		t.Errorf("%d grants reported success, %d roots installed", committed, got)
	}
}

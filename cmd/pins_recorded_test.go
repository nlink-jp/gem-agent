package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// staleSession is a session that pinned the project when it started and
// has not written the policy file since: its copy is the startup one.
// AGENTS.md then changes outside it.
func staleSession(t *testing.T) (cfg *config.Config, pf *config.PolicyFile, policyPath, proj string) {
	t.Helper()
	cfg, pf, policyPath, proj = pinFixture(t)
	if err := repin(policyPath, proj, pf); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("edited outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return cfg, pf, policyPath, proj
}

// acceptElsewhere is `gem-agent trust --accept` in another terminal: its
// own load of the policy file, the current content recorded.
func acceptElsewhere(t *testing.T, policyPath, proj string) {
	t.Helper()
	other, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := repin(policyPath, proj, other); err != nil {
		t.Fatal(err)
	}
}

// The notice a mid-session re-check prints says to re-trust with `trust
// --accept`. Run in another terminal, that changed nothing: the
// re-check compared with the copy of the record this process read at
// startup, and said "changed" again until the next start.
func TestRecheckSeesATrustRecordedElsewhere(t *testing.T) {
	cfg, pf, policyPath, proj := staleSession(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer

	ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if !ex["AGENTS.md"] || len(notes) != 1 || !strings.Contains(notes[0], "AGENTS.md changed") {
		t.Fatalf("before the re-trust: excluded=%v notes=%v", ex, notes)
	}

	acceptElsewhere(t, policyPath, proj)

	if ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs); ex != nil || len(notes) != 0 {
		t.Errorf("after `trust --accept` elsewhere the file is still left out: excluded=%v notes=%v", ex, notes)
	}
	// Changed again after that: left out again. The record is read each
	// time, not once more and then kept.
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("edited once more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ex, _ := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs); !ex["AGENTS.md"] {
		t.Errorf("a change after the re-trust was loaded: excluded=%v", ex)
	}
}

// The same record is read by the note after a `!` command and by the
// check before an approved write. Left on the startup copy, the first
// kept naming a file the operator had re-trusted, and the second kept an
// approved edit of it from being recorded.
func TestTheOtherReadersSeeItToo(t *testing.T) {
	cfg, pf, policyPath, proj := staleSession(t)
	msgs := uitext.For(uitext.EN)

	if note := pinChangesNote(cfg, pf, policyPath, proj, true, msgs); !strings.Contains(note, "AGENTS.md changed") {
		t.Fatalf("before the re-trust the note = %q", note)
	}
	if pinIsCurrent(pf, policyPath, proj, "AGENTS.md") {
		t.Fatal("before the re-trust the changed file reads as current")
	}

	acceptElsewhere(t, policyPath, proj)

	if note := pinChangesNote(cfg, pf, policyPath, proj, true, msgs); note != "" {
		t.Errorf("after the re-trust the note still names it: %q", note)
	}
	if !pinIsCurrent(pf, policyPath, proj, "AGENTS.md") {
		t.Error("after the re-trust the file still reads as drifted")
	}
}

// A session that started before the project had pins holds a copy with
// none. An approved write must still be recorded once another session
// has pinned the project: "no pins yet" is read from the file too.
func TestRepinNameReadsWhetherPinsExistFromTheFile(t *testing.T) {
	_, pf, policyPath, proj := pinFixture(t)
	acceptElsewhere(t, policyPath, proj)
	disk, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	before := disk.PinsFor(proj)["AGENTS.md"]
	if pf.HasPins(proj) || before == "" {
		t.Fatalf("fixture: the session copy has pins (%v) or the file has none (%q)", pf.HasPins(proj), before)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("approved edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repinName(policyPath, proj, "AGENTS.md", pf, nil); err != nil {
		t.Fatal(err)
	}
	if disk, err = config.LoadPolicyFile(policyPath); err != nil || disk.PinsFor(proj)["AGENTS.md"] == before {
		t.Errorf("the approved write was not recorded: %v %v", err, disk.PinsFor(proj))
	}
}

// A record that is gone or unreadable never loosens the comparison: the
// session's own copy stands in, the changed file stays out, and an
// unreadable file is said.
func TestAnUnreadableRecordLeavesTheSessionsCopy(t *testing.T) {
	cfg, pf, policyPath, proj := staleSession(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer

	if err := os.WriteFile(policyPath, []byte("this is = not [ toml\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if !ex["AGENTS.md"] {
		t.Errorf("an unreadable record loaded the changed file: excluded=%v", ex)
	}
	if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "could not re-read the trust record") || !strings.Contains(joined, "AGENTS.md changed") {
		t.Errorf("notes = %v", notes)
	}
	if pinIsCurrent(pf, policyPath, proj, "AGENTS.md") {
		t.Error("an unreadable record made the changed file current")
	}

	if err := os.Remove(policyPath); err != nil {
		t.Fatal(err)
	}
	ex, notes = checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if !ex["AGENTS.md"] || len(notes) != 1 {
		t.Errorf("a missing record: excluded=%v notes=%v", ex, notes)
	}
	if note := pinChangesNote(cfg, pf, policyPath, proj, true, msgs); !strings.Contains(note, "AGENTS.md changed") {
		t.Errorf("a missing record: note = %q", note)
	}
}

// The record on disk can re-trust a file; it cannot un-pin the project.
// "No pins" reads as "nothing pinned yet", which loads everything, so a
// policy file that parses but has lost this project's pins must not
// become the record: deleted and then rewritten by a session in another
// project, emptied, or restored from an older copy.
func TestARecordWithoutThisProjectsPinsIsNotTheRecord(t *testing.T) {
	msgs := uitext.For(uitext.EN)
	for name, lose := range map[string]func(t *testing.T, policyPath, proj string){
		"rewritten by a session in another project": func(t *testing.T, policyPath, proj string) {
			if err := os.Remove(policyPath); err != nil {
				t.Fatal(err)
			}
			if _, err := config.MutatePolicyFile(policyPath, func(pf *config.PolicyFile) {
				pf.SetTrust(t.TempDir(), config.TrustGranted)
			}); err != nil {
				t.Fatal(err)
			}
		},
		"emptied": func(t *testing.T, policyPath, proj string) {
			if err := os.WriteFile(policyPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"trust kept, pins gone": func(t *testing.T, policyPath, proj string) {
			bare := &config.PolicyFile{Tools: map[string]string{}, Projects: map[string]config.ProjectPolicy{}}
			bare.SetTrust(proj, config.TrustGranted)
			if err := bare.Save(policyPath); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, pf, policyPath, proj := staleSession(t)
			if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte("{\"changed\":1}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			lose(t, policyPath, proj)
			if disk, err := config.LoadPolicyFile(policyPath); err != nil || disk.HasPins(proj) {
				t.Fatalf("fixture: the file should parse and hold no pins for the project: %v", err)
			}
			var out bytes.Buffer
			ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
			if !ex["AGENTS.md"] || !ex[".mcp.json"] {
				t.Errorf("changed files were loaded: excluded=%v notes=%v", ex, notes)
			}
			if joined := strings.Join(notes, "\n"); !strings.Contains(joined, "no longer records this project's trusted files") {
				t.Errorf("the operator is not told the record lost the project: %v", notes)
			}
			if note := pinChangesNote(cfg, pf, policyPath, proj, true, msgs); !strings.Contains(note, "AGENTS.md changed") {
				t.Errorf("the note after a command went silent: %q", note)
			}
			if pinIsCurrent(pf, policyPath, proj, "AGENTS.md") {
				t.Error("the changed file reads as current")
			}
		})
	}
}

// A file this session left out is not re-pinned by an approved write
// into it, and the write has to say so. After a `trust --accept`
// elsewhere the file matches the record again; with no /clear since,
// the session still has it excluded, and the write passed as recorded
// while repinName skipped it without a word.
func TestAWriteIntoAFileTheSessionLeftOutSaysSo(t *testing.T) {
	_, pf, policyPath, proj := staleSession(t)
	excluded := map[string]bool{"AGENTS.md": true}
	acceptElsewhere(t, policyPath, proj)
	if !pinIsCurrent(pf, policyPath, proj, "AGENTS.md") {
		t.Fatal("fixture: after the re-trust the file should match the record")
	}
	if repinnable(pf, policyPath, proj, "AGENTS.md", excluded) {
		t.Error("a write into a file the session left out reads as one that will be recorded")
	}
	if !repinnable(pf, policyPath, proj, "AGENTS.md", nil) {
		t.Error("a write into a loaded, current file should be recorded")
	}
	if !repinnable(pf, policyPath, proj, ".mcp.json", excluded) {
		t.Error("the exclusion of one name spread to another")
	}
}

// Every comparison in pins.go goes through recordedPolicy. A reader
// added later that compares with the session's copy directly brings the
// stale answer back, and no behavioural test would know to look for it.
func TestNoPinIsComparedWithTheStartupCopy(t *testing.T) {
	src, err := os.ReadFile("pins.go")
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "//") {
			continue
		}
		// recordedPolicy names the session's copy `session`, and is the one
		// place that may ask it whether pins exist.
		for _, direct := range []string{"policyFile.PinsFor(", "policyFile.HasPins(", "session.PinsFor("} {
			if strings.Contains(l, direct) {
				t.Errorf("pins.go:%d reads the session's copy of the pins: %s", i+1, strings.TrimSpace(l))
			}
		}
	}
	root, err := os.ReadFile("root.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(root), "writes.begin(tc, name, repinnable(") {
		t.Error("root.go: an approved write does not ask repinnable whether it will be recorded")
	}
	for _, direct := range []string{".PinsFor(", ".HasPins("} {
		if strings.Contains(string(root), direct) {
			t.Errorf("root.go reads pins itself (%s): comparisons belong in pins.go", direct)
		}
	}
}

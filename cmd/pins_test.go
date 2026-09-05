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

func pinFixture(t *testing.T) (cfg *config.Config, pf *config.PolicyFile, policyPath, proj string) {
	t.Helper()
	proj = t.TempDir()
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".mcp.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	policyPath = filepath.Join(t.TempDir(), "policy.toml")
	pf = &config.PolicyFile{Tools: map[string]string{}, Projects: map[string]config.ProjectPolicy{}}
	pf.SetTrust(proj, config.TrustGranted)
	if err := pf.Save(policyPath); err != nil {
		t.Fatal(err)
	}
	c := config.Config{Approval: config.ApprovalConfig{PinTrustedFiles: true}}
	return &c, pf, policyPath, proj
}

// ADR-0074 §1: the first start after trust pins silently; a changed
// file then asks interactively — y re-pins, N excludes — and is left
// out without a prompt non-interactively.
func TestCheckPinsTrustsOnFirstUseThenAsks(t *testing.T) {
	cfg, pf, policyPath, proj := pinFixture(t)
	msgs := uitext.For(uitext.EN)
	var out bytes.Buffer
	excluded, notes := checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs)
	if excluded != nil || len(notes) != 1 || !strings.Contains(notes[0], "recorded as trusted") {
		t.Fatalf("first use: excluded=%v notes=%v", excluded, notes)
	}
	if len(pf.PinsFor(proj)) != 2 {
		t.Fatalf("pins recorded: %v", pf.PinsFor(proj))
	}
	// Unchanged: nothing asked, nothing excluded.
	if ex, notes := checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader(""), &out, msgs); ex != nil || len(notes) != 0 {
		t.Errorf("unchanged content: excluded=%v notes=%v", ex, notes)
	}
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-interactive: excluded and said.
	ex, notes := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs)
	if !ex["AGENTS.md"] || ex[".mcp.json"] {
		t.Errorf("non-interactive excluded = %v", ex)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "AGENTS.md changed") {
		t.Errorf("non-interactive notes = %v", notes)
	}
	// Interactive N: still excluded, pin unchanged (asks again next time).
	old := pf.PinsFor(proj)["AGENTS.md"]
	ex, _ = checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader("n\n"), &out, msgs)
	if !ex["AGENTS.md"] || pf.PinsFor(proj)["AGENTS.md"] != old {
		t.Errorf("after N: excluded=%v pin changed=%v", ex, pf.PinsFor(proj)["AGENTS.md"] != old)
	}
	if !strings.Contains(out.String(), "AGENTS.md changed") || !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("prompt text: %q", out.String())
	}
	// Interactive y: re-pinned, not excluded; the policy file on disk agrees.
	ex, notes = checkPins(cfg, pf, policyPath, proj, true, true, strings.NewReader("y\n"), &out, msgs)
	if ex != nil || pf.PinsFor(proj)["AGENTS.md"] == old {
		t.Errorf("after y: excluded=%v notes=%v", ex, notes)
	}
	disk, err := config.LoadPolicyFile(policyPath)
	if err != nil || disk.PinsFor(proj)["AGENTS.md"] != pf.PinsFor(proj)["AGENTS.md"] {
		t.Errorf("disk pins disagree: %v %v", err, disk.PinsFor(proj))
	}
	// The grant reads the exclusion.
	g := projectGrant{trusted: true, excluded: map[string]bool{".mcp.json": true}}
	if g.mcp() || !g.instruction("AGENTS.md") || !g.skill("x") {
		t.Error("grant accessors wrong")
	}
	// Untrusted or opted out: no pins, nothing excluded.
	if ex, _ := checkPins(cfg, pf, policyPath, proj, false, true, nil, &out, msgs); ex != nil {
		t.Error("untrusted project got exclusions")
	}
	cfg.Approval.PinTrustedFiles = false
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("changed again\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ex, _ := checkPins(cfg, pf, policyPath, proj, true, false, nil, &out, msgs); ex != nil {
		t.Error("opt-out still excluded a changed file")
	}
}

// A repin records the current content — what an operator-approved
// write or `gem-agent trust --accept` does.
func TestRepinRecordsCurrentContent(t *testing.T) {
	_, pf, policyPath, proj := pinFixture(t)
	if err := repin(policyPath, proj, pf); err != nil {
		t.Fatal(err)
	}
	before := pf.PinsFor(proj)["AGENTS.md"]
	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := repin(policyPath, proj, pf); err != nil {
		t.Fatal(err)
	}
	if pf.PinsFor(proj)["AGENTS.md"] == before {
		t.Error("repin did not record the new content")
	}
}

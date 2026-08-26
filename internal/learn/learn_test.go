package learn

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/session"
)

// writeTranscript builds a synthetic transcript in the flat legacy
// layout, which List reads in place (ADR-0022 §3).
func writeTranscript(t *testing.T, dir, projectDir, id string, records ...map[string]any) {
	t.Helper()
	var lines []byte
	add := func(kind string, data any) {
		rec := map[string]any{"ts": "2026-08-26T10:00:00Z", "kind": kind, "data": data}
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, b...)
		lines = append(lines, '\n')
	}
	add(session.KindHeader, map[string]any{
		"schema": session.SchemaVersion, "version": "test",
		"model": "m", "project": projectDir,
	})
	// A conversation, so List does not skip the file as empty.
	add(session.KindMessage, map[string]any{"role": "user", "content": "do it"})
	for _, r := range records {
		kind, _ := r["kind"].(string)
		add(kind, r["data"])
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), lines, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gate(name, decision, key, detail string) map[string]any {
	return map[string]any{"kind": "gate_decision", "data": map[string]any{
		"name": name, "decision": decision, "key": key, "detail": detail,
	}}
}

func auto(name, key string, approved bool) map[string]any {
	return map[string]any{"kind": "auto_decision", "data": map[string]any{
		"name": name, "approved": approved, "key": key, "tier": "review",
	}}
}

// ids must match the timestamp shape session.Open writes, or List
// skips the file.
func id(n int) string {
	return []string{
		"20260820-100000", "20260821-100000", "20260822-100000",
		"20260823-100000", "20260824-100000",
	}[n]
}

func scan(t *testing.T, dir, projectDir string) Report {
	t.Helper()
	rep, err := Scan(dir, projectDir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func emptyPolicy(t *testing.T) policy.Policy {
	t.Helper()
	p, _, err := policy.Build(nil, nil, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The core case: the same command approved in three sessions, never
// denied, becomes a "never" proposal.
func TestProposesNeverAfterRepeatedApprovals(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "approved", "go test", "go test ./..."))
	}
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj)
	if len(got) != 1 {
		t.Fatalf("proposals = %+v, want 1", got)
	}
	if got[0].Key != "go test" || got[0].Decision != "never" {
		t.Errorf("proposal = %+v", got[0])
	}
	if got[0].ApproveSessions != 3 {
		t.Errorf("ApproveSessions = %d, want 3", got[0].ApproveSessions)
	}
	if len(got[0].Examples) == 0 || got[0].Examples[0] != "go test ./..." {
		t.Errorf("examples = %v", got[0].Examples)
	}
}

// The load-bearing count: a session allowlist turns one keystroke into
// many approvals, so repeats inside one session are one vote.
func TestRepeatsWithinOneSessionAreOneVote(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./..."),
		gate("shell_exec", "approved", "go test", "go test ./internal/..."),
		gate("shell_exec", "approved", "go test", "go test -run X ./..."),
		gate("shell_exec", "approved", "go test", "go test -v ./..."),
		gate("shell_exec", "approved", "go test", "go test -race ./..."))

	rep := scan(t, dir, proj)
	if got := rep.Keys["go test"].ApproveSessions; got != 1 {
		t.Errorf("ApproveSessions = %d — five calls in one session are one decision", got)
	}
	if len(Propose(rep, emptyPolicy(t), proj)) != 0 {
		t.Error("one session cleared the bar for a rule")
	}
}

// A single denial anywhere disqualifies a "never" proposal: the bar is
// unanimity, not a majority.
func TestOneDenialBlocksANeverProposal(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "approved", "make build", "make build"))
	}
	writeTranscript(t, dir, proj, id(3),
		gate("shell_exec", "denied", "make build", "make build"))

	for _, p := range Propose(scan(t, dir, proj), emptyPolicy(t), proj) {
		if p.Key == "make build" && p.Decision == "never" {
			t.Errorf("a denied key was proposed for never: %+v", p)
		}
	}
}

// Denials propose the tightening rule, at the lower bar.
func TestProposesAlwaysAfterDenials(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 2 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "denied", "make deploy", "make deploy"))
	}
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj)
	if len(got) != 1 || got[0].Decision != "always" || got[0].Key != "make deploy" {
		t.Fatalf("proposals = %+v", got)
	}
}

// An empty key marks a call no rule could ever match; it must not
// produce one either.
func TestUnkeyableDecisionsAreIgnored(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "approved", "", "cat x | sh"))
	}
	rep := scan(t, dir, proj)
	if len(rep.Keys) != 0 {
		t.Errorf("keys = %v, want none", rep.Keys)
	}
}

// ADR-0045 §5: memory writes are excluded outright — frequency is not
// evidence where the risk lives in each call's content.
func TestMemoryWritesAreNeverProposed(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 4 {
		writeTranscript(t, dir, proj, id(i),
			gate("save_memory", "approved", "save_memory", "save_memory: a fact"))
	}
	if got := Propose(scan(t, dir, proj), emptyPolicy(t), proj); len(got) != 0 {
		t.Errorf("memory writes were proposed: %+v", got)
	}
}

// MCP tools key by name and are proposable — the wobble this feature
// exists to retire.
func TestMCPToolIsProposedByName(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("mcp__asn__lookup_ip", "approved", "mcp__asn__lookup_ip", "lookup_ip 203.0.113.7"),
			auto("mcp__asn__lookup_ip", "mcp__asn__lookup_ip", false))
	}
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj)
	if len(got) != 1 || got[0].Key != "mcp__asn__lookup_ip" || got[0].Decision != "never" {
		t.Fatalf("proposals = %+v", got)
	}
	// The wobble evidence rides along for display.
	if got[0].ModelEscalated != 3 {
		t.Errorf("ModelEscalated = %d, want 3", got[0].ModelEscalated)
	}
}

// A key the policy already answers is not proposed again.
func TestKeysAlreadyCoveredAreSkipped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "approved", "go test", "go test ./..."))
	}
	p, _, err := policy.Build(nil, nil, map[string]string{"go test": "never"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := Propose(scan(t, dir, proj), p, proj); len(got) != 0 {
		t.Errorf("an already-settled key was proposed: %+v", got)
	}
}

// "never" does not lift the Block floor, so proposing it for a key whose
// calls the rule tier blocks would offer a rule that changes nothing.
func TestBlockTierKeysAreNotProposedForNever(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("shell_exec", "approved", "git push", "git push origin main"))
	}
	for _, p := range Propose(scan(t, dir, proj), emptyPolicy(t), proj) {
		if p.Decision == "never" {
			t.Errorf("a Block-tier key was proposed for never: %+v", p)
		}
	}
}

// Another project's transcripts are not this project's evidence.
func TestScanIsScopedToTheProject(t *testing.T) {
	dir := t.TempDir()
	for i := range 3 {
		writeTranscript(t, dir, "/work/other", id(i),
			gate("shell_exec", "approved", "go test", "go test ./..."))
	}
	rep := scan(t, dir, "/work/proj")
	if len(rep.Keys) != 0 {
		t.Errorf("keys leaked from another project: %v", rep.Keys)
	}
}

// Auto-mode verdicts are how the ladder behaved, never evidence of what
// the operator wants: they must not carry a proposal on their own.
func TestAutoDecisionsAloneProposeNothing(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 5 {
		writeTranscript(t, dir, proj, id(i),
			auto("shell_exec", "go test", true))
	}
	rep := scan(t, dir, proj)
	if got := rep.Keys["go test"].ModelApproved; got != 5 {
		t.Errorf("ModelApproved = %d, want 5", got)
	}
	if got := Propose(rep, emptyPolicy(t), proj); len(got) != 0 {
		t.Errorf("auto verdicts alone produced a proposal: %+v", got)
	}
}

// A torn or corrupt line costs its own record, not the file.
func TestCorruptLinesAreSkipped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./..."))
	path := filepath.Join(dir, id(0)+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("{not json\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	rep := scan(t, dir, proj)
	if rep.Keys["go test"] == nil || rep.Keys["go test"].ApproveSessions != 1 {
		t.Errorf("a corrupt trailing line cost the whole file: %v", rep.Keys)
	}
}

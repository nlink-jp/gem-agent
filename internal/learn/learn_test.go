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

// gate builds a decision the operator typed. Typed is the default in
// these fixtures because it is the case the thresholds are about.
func gate(name, decision, key, detail string) map[string]any {
	return gateFrom(name, decision, key, detail, "operator")
}

// gateFrom builds a decision with an explicit source: "operator" for a
// keystroke, "allowlist" for one the session allowlist answered.
func gateFrom(name, decision, key, detail, source string) map[string]any {
	return map[string]any{"kind": "gate_decision", "data": map[string]any{
		"name": name, "decision": decision, "key": key,
		"detail": detail, "source": source,
	}}
}

// noServers/noTools stand in where a test has no MCP servers.
func noServers(string) bool   { return false }
func noTools(string) []string { return nil }

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
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools)
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

// ADR-0048 §1, the defect that made /learn propose nothing on its first
// real run: an allowlist answer is ONE keystroke standing in for any
// number of calls, so it stays worth one vote however many calls it
// covers.
func TestAllowlistAnswersAreOneVote(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("shell_exec", "approved", "go test", "go test ./..."), // the 'a' keystroke
		gateFrom("shell_exec", "approved", "go test", "go test -v ./...", "allowlist"),
		gateFrom("shell_exec", "approved", "go test", "go test -run X ./...", "allowlist"),
		gateFrom("shell_exec", "approved", "go test", "go test -race ./...", "allowlist"),
		gateFrom("shell_exec", "approved", "go test", "go test ./internal/...", "allowlist"))

	rep := scan(t, dir, proj)
	st := rep.Keys["go test"]
	if st.TypedApprovals != 1 {
		t.Errorf("TypedApprovals = %d — four allowlist answers are not four decisions", st.TypedApprovals)
	}
	if got := Propose(rep, emptyPolicy(t), proj, noServers, noTools); len(got) != 0 {
		t.Errorf("one keystroke cleared the bar for a rule: %+v", got)
	}
}

// The other half of §1: answers the operator actually typed count
// individually, even within one session. Collapsing these is what threw
// away 25 real decisions.
func TestTypedApprovalsCountIndividually(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	var recs []map[string]any
	for range approvalsForNever {
		recs = append(recs, gate("shell_exec", "approved", "go test", "go test ./..."))
	}
	writeTranscript(t, dir, proj, id(0), recs...)

	rep := scan(t, dir, proj)
	if got := rep.Keys["go test"].TypedApprovals; got != approvalsForNever {
		t.Errorf("TypedApprovals = %d, want %d", got, approvalsForNever)
	}
	got := Propose(rep, emptyPolicy(t), proj, noServers, noTools)
	if len(got) != 1 || got[0].Key != "go test" {
		t.Fatalf("typed approvals in one session proposed nothing: %+v", got)
	}
}

// A gate_decision written before sources existed is read as an
// allowlist answer: counting an unknown as a typed decision is the
// direction that hands out permissions.
func TestUnsourcedRecordsAreNotCountedAsTyped(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	var recs []map[string]any
	for range 6 {
		recs = append(recs, gateFrom("shell_exec", "approved", "go test", "go test ./...", ""))
	}
	writeTranscript(t, dir, proj, id(0), recs...)

	rep := scan(t, dir, proj)
	if got := rep.Keys["go test"].TypedApprovals; got != 0 {
		t.Errorf("TypedApprovals = %d, want 0 for unsourced records", got)
	}
	if got := Propose(rep, emptyPolicy(t), proj, noServers, noTools); len(got) != 0 {
		t.Errorf("unsourced records cleared the bar: %+v", got)
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

	for _, p := range Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools) {
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
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools)
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
	if got := Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools); len(got) != 0 {
		t.Errorf("memory writes were proposed: %+v", got)
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
	if got := Propose(scan(t, dir, proj), p, proj, noServers, noTools); len(got) != 0 {
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
	for _, p := range Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools) {
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
	if got := Propose(rep, emptyPolicy(t), proj, noServers, noTools); len(got) != 0 {
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

// servers/tools build the MCP callbacks a real session would supply.
func globalServers(names ...string) func(string) bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return func(s string) bool { return set[s] }
}

func toolsOf(m map[string][]string) func(string) []string {
	return func(s string) []string { return m[s] }
}

// The reported failure, as a regression test: ONE session, many
// different MCP tools each approved once. ADR-0045's per-tool
// thresholds proposed nothing here; the server rule is what this shape
// of friction needs (ADR-0048 §2).
func TestOneSessionManyMCPToolsProposesTheServer(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("mcp__asn-lookup__lookup_ip", "approved", "mcp__asn-lookup__lookup_ip", "lookup_ip 203.0.113.7"),
		auto("mcp__asn-lookup__lookup_ip", "mcp__asn-lookup__lookup_ip", false),
		gate("mcp__asn-lookup__lookup_asn", "approved", "mcp__asn-lookup__lookup_asn", "lookup_asn 64500"),
		auto("mcp__asn-lookup__lookup_asn", "mcp__asn-lookup__lookup_asn", false),
		gate("mcp__whois-lookup__lookup", "approved", "mcp__whois-lookup__lookup", "lookup example.com"))

	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj,
		globalServers("asn-lookup", "whois-lookup"),
		toolsOf(map[string][]string{
			"asn-lookup": {"mcp__asn-lookup__lookup_ip", "mcp__asn-lookup__lookup_asn",
				"mcp__asn-lookup__db_status", "mcp__asn-lookup__update_db"},
		}))

	if len(got) != 1 {
		t.Fatalf("proposals = %+v, want the asn-lookup server", got)
	}
	p := got[0]
	if p.Pattern != "mcp__asn-lookup__*" || !p.Global || p.Server != "asn-lookup" {
		t.Errorf("proposal = %+v", p)
	}
	// whois-lookup had one approved tool: below the diversity bar.
	if len(p.CoveredApproved) != 2 {
		t.Errorf("CoveredApproved = %v, want the two approved tools", p.CoveredApproved)
	}
	// The disclosure: what the rule grants beyond its evidence.
	if len(p.CoveredUnused) != 2 {
		t.Errorf("CoveredUnused = %v, want db_status and update_db", p.CoveredUnused)
	}
}

// A server the project supplied through .mcp.json is that project's,
// and never earns a rule that applies everywhere.
func TestProjectSuppliedServerIsNotProposed(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("mcp__local__a", "approved", "mcp__local__a", "a"),
		gate("mcp__local__b", "approved", "mcp__local__b", "b"))

	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj,
		globalServers(), // "local" is not global
		toolsOf(map[string][]string{"local": {"mcp__local__a", "mcp__local__b"}}))
	if len(got) != 0 {
		t.Errorf("a project-supplied server was proposed globally: %+v", got)
	}
}

// One denial anywhere in a server disqualifies it: the rule covers
// every tool, so the evidence has to as well.
func TestDeniedToolBlocksItsServerProposal(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("mcp__urlscan__search", "approved", "mcp__urlscan__search", "search x"),
		gate("mcp__urlscan__get_result", "approved", "mcp__urlscan__get_result", "get_result y"),
		gate("mcp__urlscan__scan_url", "denied", "mcp__urlscan__scan_url", "scan_url https://x"))

	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj,
		globalServers("urlscan"),
		toolsOf(map[string][]string{"urlscan": {"mcp__urlscan__search", "mcp__urlscan__scan_url"}}))
	for _, p := range got {
		if p.Server == "urlscan" && p.Decision == "never" {
			t.Errorf("a server with a denied tool was proposed: %+v", p)
		}
	}
}

// One approved tool is not evidence about a server: diversity is the
// counter, and it is what makes the allowlist unable to inflate it.
func TestOneToolIsBelowTheServerBar(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	writeTranscript(t, dir, proj, id(0),
		gate("mcp__asn__lookup_ip", "approved", "mcp__asn__lookup_ip", "lookup_ip x"))
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj,
		globalServers("asn"), toolsOf(map[string][]string{"asn": {"mcp__asn__lookup_ip"}}))
	if len(got) != 0 {
		t.Errorf("one tool proposed a whole server: %+v", got)
	}
}

// MCP tools no longer produce per-tool project rules (ADR-0048 §4):
// two proposal shapes for the same call would duplicate or contradict.
func TestMCPToolsGetNoPerToolProposal(t *testing.T) {
	dir, proj := t.TempDir(), "/work/proj"
	for i := range 3 {
		writeTranscript(t, dir, proj, id(i),
			gate("mcp__asn__lookup_ip", "approved", "mcp__asn__lookup_ip", "lookup_ip x"))
	}
	got := Propose(scan(t, dir, proj), emptyPolicy(t), proj, noServers, noTools)
	if len(got) != 0 {
		t.Errorf("an MCP tool produced a per-tool proposal: %+v", got)
	}
}

func TestServerOf(t *testing.T) {
	for _, tc := range []struct {
		tool, server string
		ok           bool
	}{
		{"mcp__asn-lookup__lookup_ip", "asn-lookup", true},
		{"mcp__a__b__c", "a", true},
		{"shell_exec", "", false},
		{"mcp__noserver", "", false},
		{"mcp____x", "", false},
	} {
		got, ok := ServerOf(tc.tool)
		if got != tc.server || ok != tc.ok {
			t.Errorf("ServerOf(%q) = (%q, %v), want (%q, %v)", tc.tool, got, ok, tc.server, tc.ok)
		}
	}
}

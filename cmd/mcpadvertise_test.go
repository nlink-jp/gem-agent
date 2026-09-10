package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// advInventory builds an inventory whose registered names are the given
// tools per server, plus any extra (excluded) names given per server.
func advInventory(servers map[string][]string) mcpInventory {
	inv := mcpInventory{registered: map[string][]string{}}
	for s, toolNames := range servers {
		for _, t := range toolNames {
			inv.registered[s] = append(inv.registered[s], mcpToolName(s, t))
		}
	}
	return inv
}

// everything treats every inventory name as registered — the tests
// that need the registry's say-so pass a real predicate.
func everything(string) bool { return true }

type recordingLog struct {
	kinds []string
	data  []any
}

func (r *recordingLog) Log(kind string, data any) error {
	r.kinds = append(r.kinds, kind)
	r.data = append(r.data, data)
	return nil
}

// commit applies a stage made outside the loop the way the loop would.
func commit(adv *mcpAdvertiser, id string) bool { return adv.AfterTool(llm.ToolCall{ID: id}, false) }

// ADR-0083 §1: the predicate. Built-ins always; under "all" every MCP
// tool; under "on-request" only loaded ones; withheld never.
func TestAdvertisePredicate(t *testing.T) {
	inv := advInventory(map[string][]string{"whois-lookup": {"lookup"}, "slack": {"send", "read"}})
	all := newMCPAdvertiser(false, nil, nil, nil)
	all.setInventory(inv, everything)
	for _, n := range []string{"read_file", "mcp__whois-lookup__lookup", "mcp__slack__send"} {
		if !all.Advertise(n) {
			t.Errorf("all: %s not advertised", n)
		}
	}

	adv := newMCPAdvertiser(true, []string{"whois-lookup"}, nil, nil)
	adv.setInventory(inv, everything)
	if !adv.Advertise("read_file") {
		t.Error("on-request: built-in must always be advertised")
	}
	if !adv.Advertise("mcp__whois-lookup__lookup") {
		t.Error("on-request: preloaded server not advertised")
	}
	if adv.Advertise("mcp__slack__send") {
		t.Error("on-request: unloaded server advertised")
	}
	if adv.Advertise("mcp__ghost__x") {
		t.Error("on-request: an unregistered mcp name must not be advertised")
	}
	adv.Stage("s1", nil, []string{"mcp__slack__send"}, nil)
	commit(adv, "s1")
	if !adv.Advertise("mcp__slack__send") || adv.Advertise("mcp__slack__read") {
		t.Error("per-tool load must advertise exactly that tool")
	}
	adv.Stage("s2", nil, nil, map[string]string{"mcp__whois-lookup__lookup": "addresses the assistant"})
	commit(adv, "s2")
	if adv.Advertise("mcp__whois-lookup__lookup") {
		t.Error("withheld must beat loaded")
	}
	if got := adv.Status("whois-lookup"); !strings.Contains(got, "withheld") {
		t.Errorf("status = %q, want the withheld count", got)
	}
	if reg, ad, held := adv.Counts(); reg != 3 || ad != 1 || held != 1 {
		t.Errorf("counts = %d/%d/%d, want 3 registered, 1 advertised, 1 withheld", reg, ad, held)
	}
}

// Names an exclusion removed are in the inventory but not in the
// registry (ADR-0077): they are not tools, and must not be counted,
// listed, or make a wholly-excluded server look loadable.
func TestInventoryKeepsOnlyRegisteredNames(t *testing.T) {
	inv := advInventory(map[string][]string{"obsidian": {"read", "patch"}, "gone": {"x"}})
	registered := map[string]bool{"mcp__obsidian__read": true}
	adv := newMCPAdvertiser(true, nil, nil, nil)
	adv.setInventory(inv, func(n string) bool { return registered[n] })
	if lines := adv.PromptLines(); len(lines) != 1 || lines[0] != "- obsidian (1 tools)" {
		t.Errorf("prompt lines = %v", lines)
	}
	if err := adv.LoadServerNow("gone"); err == nil {
		t.Error("a wholly-excluded server must not be loadable")
	}
	if err := adv.LoadServerNow("obsidian"); err != nil {
		t.Fatal(err)
	}
	if got := adv.Status("obsidian"); got != "loaded" {
		t.Errorf("status = %q, want loaded (the excluded name must not count)", got)
	}
	if reg, ad, _ := adv.Counts(); reg != 1 || ad != 1 {
		t.Errorf("counts = %d/%d, want 1/1", reg, ad)
	}
}

// --allow mcp__<server>__* preloads the server and mcp__<server>__<tool>
// preloads that tool (ADR-0083 §7); other grants are not declarations.
func TestGrantsFromAllow(t *testing.T) {
	servers, toolNames := grantsFromAllow([]string{"mcp__tor-exit-lookup__*", "read_file", "mcp__x__check_ip", "mcp__a__b__*", " mcp__doh-lookup__* "})
	if len(servers) != 2 || servers[0] != "tor-exit-lookup" || servers[1] != "doh-lookup" {
		t.Errorf("servers = %v", servers)
	}
	if len(toolNames) != 1 || toolNames[0] != "mcp__x__check_ip" {
		t.Errorf("tools = %v", toolNames)
	}
	adv := newMCPAdvertiser(true, nil, []string{"mcp__github__search_code"}, nil)
	adv.setInventory(advInventory(map[string][]string{"github": {"search_code", "prs"}}), everything)
	if !adv.Advertise("mcp__github__search_code") || adv.Advertise("mcp__github__prs") {
		t.Error("an exact --allow grant must preload exactly that tool")
	}
}

// ADR-0083 §3: a staged load is applied when the loop accepts the call
// and dropped when the call was abandoned; a committed load is recorded
// once, with only what it newly advertised.
func TestStagedLoadCommitsOrDiscardsWithTheCall(t *testing.T) {
	log := &recordingLog{}
	adv := newMCPAdvertiser(true, nil, nil, log)
	adv.setInventory(advInventory(map[string][]string{"slack": {"send", "read"}, "github": {"prs"}}), everything)

	adv.Stage("c1", nil, []string{"mcp__slack__send", "mcp__nope__x"}, nil)
	if adv.Advertise("mcp__slack__send") {
		t.Fatal("a staged load must not be visible before commit")
	}
	if !commit(adv, "c1") {
		t.Error("commit reported no change")
	}
	if !adv.Advertise("mcp__slack__send") {
		t.Error("committed load not advertised")
	}
	if len(log.kinds) != 1 || log.kinds[0] != mcpAdvertiseKind {
		t.Errorf("records = %v, want one %s", log.kinds, mcpAdvertiseKind)
	}
	if rec := log.data[0].(mcpAdvertiseRecord); len(rec.Tools) != 1 || rec.Tools[0] != "mcp__slack__send" {
		t.Errorf("record = %+v: unknown names must not be recorded", rec)
	}
	// The same load again is a no-op and writes nothing.
	adv.Stage("c1b", nil, []string{"mcp__slack__send"}, nil)
	if commit(adv, "c1b") || len(log.kinds) != 1 {
		t.Error("a repeated load reported a change or wrote a record")
	}

	adv.Stage("c2", []string{"github"}, nil, nil)
	if adv.AfterTool(llm.ToolCall{ID: "c2"}, true) {
		t.Error("an abandoned call reported a change")
	}
	if adv.Advertise("mcp__github__prs") {
		t.Error("an abandoned call's load was applied")
	}
	if commit(adv, "c2") {
		t.Error("a discarded stage was applied on a second hook call")
	}
	if commit(adv, "unknown") {
		t.Error("a call that staged nothing reported a change")
	}
	// A stage that arrives after its call was abandoned is dropped.
	adv.AfterTool(llm.ToolCall{ID: "late"}, true)
	adv.Stage("late", []string{"github"}, nil, nil)
	if len(adv.pending) != 0 {
		t.Error("a late stage of an abandoned call was kept")
	}
	// The empty id commits like any other — nothing applies by itself.
	adv.Stage("", []string{"github"}, nil, nil)
	if adv.Advertise("mcp__github__prs") {
		t.Error("an empty-id stage applied before commit")
	}
	if !commit(adv, "") || !adv.Advertise("mcp__github__prs") {
		t.Error("an empty-id stage did not commit")
	}
}

// /clear starts unloaded (preloads aside) with no flags; a reconnect
// keeps loads by name and clears flags (ADR-0083 §8).
func TestResetAndReconnect(t *testing.T) {
	adv := newMCPAdvertiser(true, []string{"whois-lookup"}, nil, nil)
	inv := advInventory(map[string][]string{"whois-lookup": {"lookup"}, "slack": {"send"}})
	adv.setInventory(inv, everything)
	adv.Stage("a", []string{"slack"}, nil, map[string]string{"mcp__slack__send": "lobbies"})
	commit(adv, "a")
	adv.Reset()
	if adv.Advertise("mcp__slack__send") || !adv.Advertise("mcp__whois-lookup__lookup") {
		t.Error("reset must drop loads and flags but keep preloads")
	}
	adv.Stage("b", []string{"slack"}, nil, map[string]string{"mcp__slack__send": "lobbies"})
	commit(adv, "b")
	adv.setInventory(inv, everything) // reconnect
	if !adv.Advertise("mcp__slack__send") {
		t.Error("reconnect must keep the load by name and clear the flag")
	}
}

// /mcp load is the operator's override: it loads the server and lifts
// the librarian's flags on it (ADR-0083 §6).
func TestOperatorLoadLiftsFlags(t *testing.T) {
	adv := newMCPAdvertiser(true, nil, nil, nil)
	adv.setInventory(advInventory(map[string][]string{"slack": {"send"}}), everything)
	adv.Stage("f", nil, nil, map[string]string{"mcp__slack__send": "lobbies"})
	commit(adv, "f")
	if err := adv.LoadServerNow("slack"); err != nil {
		t.Fatal(err)
	}
	if !adv.Advertise("mcp__slack__send") {
		t.Error("operator load did not lift the flag")
	}
	if err := adv.LoadServerNow("ghost"); err == nil || !strings.Contains(err.Error(), "slack") {
		t.Errorf("unknown server must be refused with the names that exist: %v", err)
	}
}

// mcp_load stages under the call id the loop put in the context and
// answers with the server's tools — never a withheld one, whose text
// the flag exists to keep out; an unknown name is refused with the
// servers that exist, in existence-shaped wording (ADR-0083 §4, §6).
func TestMCPLoadToolStagesAndAnswers(t *testing.T) {
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"mcp__slack__send", "mcp__slack__read"} {
		if err := reg.Register(&tools.Tool{Name: n, Description: "[MCP:slack] Does a thing. More.", Parameters: map[string]any{}, Mutating: true,
			Run: func(context.Context, map[string]any) (string, error) { return "", nil }}); err != nil {
			t.Fatal(err)
		}
	}
	adv := newMCPAdvertiser(true, nil, nil, nil)
	adv.setInventory(advInventory(map[string][]string{"slack": {"send", "read"}}), everything)
	adv.Stage("w", nil, nil, map[string]string{"mcp__slack__read": "addresses the assistant"})
	commit(adv, "w")
	if err := registerMCPLoadTool(reg, adv); err != nil {
		t.Fatal(err)
	}
	load, _ := reg.Get(MCPLoadName)
	out, err := load.Run(tools.WithCallID(context.Background(), "c9"), map[string]any{"server": "slack"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "slack loaded") || !strings.Contains(out, "mcp__slack__send: Does a thing.") {
		t.Errorf("result = %q", out)
	}
	if strings.Contains(out, "mcp__slack__read") || !strings.Contains(out, "1 tool(s) of this server are withheld") {
		t.Errorf("a withheld tool must be neither listed nor described:\n%s", out)
	}
	if adv.Advertise("mcp__slack__send") {
		t.Error("visible before the loop committed the call")
	}
	commit(adv, "c9")
	if !adv.Advertise("mcp__slack__send") || adv.Advertise("mcp__slack__read") {
		t.Error("after commit: send advertised, read still withheld")
	}
	if _, err := load.Run(context.Background(), map[string]any{"server": "ghost"}); err == nil ||
		!strings.Contains(err.Error(), "the servers are: slack") || strings.Contains(err.Error(), "exclud") {
		t.Errorf("unknown server: %v", err)
	}
}

// A resumed session re-advertises what its transcript recorded, and
// names what is gone, writing no new record (ADR-0083 §8).
func TestReplayAdvertisedFromTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"ts":"2026-09-10T00:00:00Z","kind":"session","data":{}}
{"ts":"2026-09-10T00:00:01Z","kind":"mcp_advertise","data":{"servers":["slack"]}}
{"ts":"2026-09-10T00:00:02Z","kind":"mcp_advertise","data":{"tools":["mcp__github__prs","mcp__gone__x"]}}
`
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &recordingLog{}
	adv := newMCPAdvertiser(true, nil, nil, log)
	adv.setInventory(advInventory(map[string][]string{"slack": {"send"}, "github": {"prs", "issues"}}), everything)
	loaded, missing, err := replayAdvertised(path, adv)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 2 || len(missing) != 1 || missing[0] != "mcp__gone__x" {
		t.Errorf("loaded=%d missing=%v", loaded, missing)
	}
	if !adv.Advertise("mcp__slack__send") || !adv.Advertise("mcp__github__prs") || adv.Advertise("mcp__github__issues") {
		t.Error("replay advertised the wrong set")
	}
	for _, k := range log.kinds {
		if k == mcpAdvertiseKind {
			t.Error("a replay must not write new mcp_advertise records")
		}
	}
}

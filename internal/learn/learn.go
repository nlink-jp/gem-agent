// Package learn turns the operator's recorded approval decisions into
// proposed approval rules (ADR-0045).
//
// Two properties define it. It is DETERMINISTIC: it parses structured
// records and never shows transcript text to a model. Transcripts are
// full of attacker-influenceable prose — tool output, file contents, web
// pages — and a model that read them and then proposed policy would be a
// prompt-injection-to-policy pipeline, the persistence vector ADR-0020
// §4 closes for memory rebuilt one layer up. The only inputs that carry
// weight are the operator's own decisions.
//
// And it only PROPOSES. Nothing here writes policy; the caller confirms
// each rule with the operator first. A learner that changed approval
// behaviour on its own would be the unauditable loosening channel the
// ADR-0008 asymmetry exists to prevent.
package learn

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/session"
)

// Thresholds for proposing a rule (ADR-0045 §5). They are constants, not
// configuration: there is no evidence yet that tuning is needed, and a
// knob here would be a knob on how readily approvals are given away.
const (
	// approvalsForNever is how many times the operator must have typed
	// an approval for a key — across any number of sessions — before
	// "never" is proposed. Typed answers count individually because
	// each one is a decision the operator actually made (ADR-0048 §1).
	approvalsForNever = 5
	// approveSessionsForNever is the other route to the same bar, for
	// the operator who answers the same thing across days rather than
	// repeatedly within one session.
	approveSessionsForNever = 3
	// denySessionsForAlways is the bar for proposing "always".
	// Tightening gets the lower one, in the spirit of the Block
	// patterns: a generous match costs one approval prompt.
	denySessionsForAlways = 2
	// serverToolsForNever is how many DISTINCT tools of one MCP server
	// must have been approved before that server is proposed
	// (ADR-0048 §2). Diversity is the right counter here and it is
	// structurally immune to the allowlist inflation that motivated
	// §1: the allowlist is keyed by tool name, so one 'a' can only ever
	// add one tool to this count.
	serverToolsForNever = 2
	// examplesKept bounds the evidence shown per proposal.
	examplesKept = 3
	// mcpPrefix marks a tool provided by an MCP server.
	mcpPrefix = "mcp__"
)

// memoryTools never earn a proposal. Where the risk lives in a call's
// content rather than its shape, frequency is not evidence: twelve
// harmless saves say nothing about the thirteenth, and memory is a
// persistence vector for injected instructions (ADR-0020 §4). Command
// keys aggregate calls with stable semantics; memory writes are the
// opposite.
var memoryTools = map[string]bool{
	"save_memory": true, "delete_memory": true,
}

// KeyStats is what the transcripts say about one aggregation key.
//
// The counts are SESSIONS, not calls. A session allowlist ('a') turns
// one keystroke into any number of approvals, and repeating a command
// inside one session is one decision made once — counting calls would
// let a single 'a' clear the bar for a rule the operator never
// considered.
type KeyStats struct {
	Key  string
	Tool string
	// ApproveSessions and DenySessions count distinct sessions in which
	// the operator answered that way at the gate.
	ApproveSessions int
	DenySessions    int
	// TypedApprovals counts approvals the operator typed, each one
	// individually (ADR-0048 §1). Answers the session allowlist gave
	// are NOT counted here: one 'a' stands in for any number of calls,
	// and counting it like a decision would inflate the evidence — the
	// defect that made /learn propose nothing on its first real run.
	TypedApprovals int
	// ModelApproved and ModelEscalated count auto-mode verdicts on this
	// key. They are not evidence of what the operator wants; they are
	// how the ladder behaved, and a key split between them is a
	// per-call judgement wobbling — the case for replacing it with a
	// rule.
	ModelApproved  int
	ModelEscalated int
	// Examples are calls the operator saw and approved, most recent
	// first.
	Examples []string
	// LastSeen is the newest decision on this key.
	LastSeen time.Time
}

// Report is the aggregate over one project's transcripts.
type Report struct {
	Sessions   int
	Scanned    int
	Unreadable int
	Keys       map[string]*KeyStats
}

// gateRecord is the decoded shape of a gate_decision record. Only the
// fields the learner needs are decoded; a transcript full of base64
// images stays cheap because attachment payloads are never unmarshalled.
type gateRecord struct {
	Name     string `json:"name"`
	Decision string `json:"decision"`
	Key      string `json:"key"`
	Detail   string `json:"detail"`
	// Source is "operator" or "allowlist" (ADR-0048 §1). Records
	// written before that distinction existed have none; those are
	// treated as allowlist answers — the conservative reading, since
	// counting an unknown as a typed decision is the direction that
	// hands out permissions.
	Source string `json:"source"`
}

func (r gateRecord) typed() bool { return r.Source == "operator" }

// autoRecord is the decoded shape of an auto_decision record.
type autoRecord struct {
	Name     string `json:"name"`
	Approved bool   `json:"approved"`
	Key      string `json:"key"`
}

// Scan aggregates the operator's decisions across one project's
// transcripts. dir is the sessions root; projectDir bounds the scan to
// this project — a command settled in one repository says nothing about
// another (ADR-0045 §1).
func Scan(dir, projectDir string) (Report, error) {
	rep := Report{Keys: map[string]*KeyStats{}}
	metas, err := session.List(dir, projectDir)
	if err != nil {
		return rep, err
	}
	for _, m := range metas {
		if err := rep.scanOne(m.Path); err != nil {
			// One unreadable transcript is a gap in the evidence, not a
			// failure of the command: report it and keep going.
			rep.Unreadable++
			continue
		}
		rep.Scanned++
	}
	rep.Sessions = len(metas)
	return rep, nil
}

// scanOne folds one transcript into the report, collapsing repeats
// within the session to one vote per key and outcome.
func (r *Report) scanOne(path string) error {
	approved := map[string]bool{}
	denied := map[string]bool{}
	return session.Scan(path, func(kind string, ts time.Time, data json.RawMessage) error {
		switch kind {
		case "gate_decision":
			var rec gateRecord
			if json.Unmarshal(data, &rec) != nil || rec.Key == "" {
				// An empty key marks a call no rule could ever match
				// (a compound or dynamic command). It must not produce
				// one either.
				return nil
			}
			st := r.stats(rec.Key, rec.Name)
			switch rec.Decision {
			case "approved":
				if !approved[rec.Key] {
					approved[rec.Key] = true
					st.ApproveSessions++
				}
				if rec.typed() {
					st.TypedApprovals++
				}
				st.addExample(rec.Detail)
			case "denied":
				if !denied[rec.Key] {
					denied[rec.Key] = true
					st.DenySessions++
				}
			default:
				return nil
			}
			if ts.After(st.LastSeen) {
				st.LastSeen = ts
			}
		case "auto_decision":
			var rec autoRecord
			if json.Unmarshal(data, &rec) != nil || rec.Key == "" {
				return nil
			}
			st := r.stats(rec.Key, rec.Name)
			if rec.Approved {
				st.ModelApproved++
			} else {
				st.ModelEscalated++
			}
		}
		return nil
	})
}

func (r *Report) stats(key, tool string) *KeyStats {
	st, ok := r.Keys[key]
	if !ok {
		st = &KeyStats{Key: key, Tool: tool}
		r.Keys[key] = st
	}
	return st
}

func (s *KeyStats) addExample(detail string) {
	if detail == "" {
		return
	}
	for _, e := range s.Examples {
		if e == detail {
			return
		}
	}
	if len(s.Examples) < examplesKept {
		s.Examples = append(s.Examples, detail)
	}
}

// Proposal is one rule offered to the operator.
type Proposal struct {
	KeyStats
	// Decision is the policy value to write: "never" or "always".
	Decision string
	// Pattern is what gets written to the policy — the key itself for a
	// command or built-in tool, and `mcp__<server>__*` for an MCP
	// server (ADR-0048 §2).
	Pattern string
	// Global says the rule goes to the global [tools] table rather than
	// this project's. True only for MCP servers, whose binary and
	// behaviour are identical in every project.
	Global bool
	// Server is the MCP server this proposal is about, empty otherwise.
	Server string
	// CoveredApproved and CoveredUnused are the tools an MCP server
	// rule would cover: the ones the operator has already approved, and
	// the ones they have not used. The second list is the disclosure
	// that makes the rule honest — it grants more than the evidence for
	// it, and the operator must see exactly how much (ADR-0048 §3).
	CoveredApproved []string
	CoveredUnused   []string
}

// ServerOf returns the MCP server a tool belongs to, and whether the
// name is an MCP tool at all. `mcp__asn-lookup__lookup_ip` →
// `asn-lookup`.
func ServerOf(tool string) (string, bool) {
	if !strings.HasPrefix(tool, mcpPrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(tool, mcpPrefix)
	i := strings.Index(rest, "__")
	if i <= 0 {
		return "", false
	}
	return rest[:i], true
}

// ServerPattern is the ADR-0008 wildcard covering one server's tools.
func ServerPattern(server string) string { return mcpPrefix + server + "__*" }

// serverStats aggregates one MCP server's keys.
type serverStats struct {
	server   string
	approved map[string]*KeyStats // tool name → its stats
	denied   bool
}

// proposeServers derives the per-server MCP rules (ADR-0048 §2).
//
// isGlobal reports whether a server came from the operator's own global
// MCP config; a server a project supplied through .mcp.json is that
// project's, and gets no global rule. toolsOf lists a server's
// currently connected tools, which is how the covered-tool disclosure
// can name tools the operator has never called.
func proposeServers(rep Report, current policy.Policy,
	isGlobal func(server string) bool, toolsOf func(server string) []string) []Proposal {

	servers := map[string]*serverStats{}
	for _, key := range sortedKeys(rep.Keys) {
		st := rep.Keys[key]
		name, ok := ServerOf(st.Tool)
		if !ok {
			continue
		}
		s, seen := servers[name]
		if !seen {
			s = &serverStats{server: name, approved: map[string]*KeyStats{}}
			servers[name] = s
		}
		if st.DenySessions > 0 {
			// One denial anywhere in the server disqualifies it: the
			// rule covers every tool, so the evidence has to as well.
			s.denied = true
		}
		if st.ApproveSessions > 0 {
			s.approved[st.Tool] = st
		}
	}

	var out []Proposal
	for _, name := range sortedServerNames(servers) {
		s := servers[name]
		if s.denied || len(s.approved) < serverToolsForNever || !isGlobal(name) {
			continue
		}
		pattern := ServerPattern(name)
		// Already answered — by this pattern or by any rule covering
		// its tools — means there is nothing to propose.
		if settled(current, s.approved) {
			continue
		}
		p := Proposal{
			Decision: "never",
			Pattern:  pattern,
			Global:   true,
			Server:   name,
			KeyStats: KeyStats{Key: pattern, Tool: mcpPrefix + name},
		}
		for _, tool := range sortedToolNames(s.approved) {
			st := s.approved[tool]
			p.CoveredApproved = append(p.CoveredApproved, tool)
			p.ApproveSessions += st.ApproveSessions
			p.TypedApprovals += st.TypedApprovals
			p.ModelApproved += st.ModelApproved
			p.ModelEscalated += st.ModelEscalated
		}
		for _, tool := range toolsOf(name) {
			if _, used := s.approved[tool]; !used {
				p.CoveredUnused = append(p.CoveredUnused, tool)
			}
		}
		sort.Strings(p.CoveredUnused)
		out = append(out, p)
	}
	return out
}

// settled reports whether the policy already answers every approved
// tool of a server.
func settled(current policy.Policy, approved map[string]*KeyStats) bool {
	for tool := range approved {
		if current.For(tool) == policy.Default {
			return false
		}
	}
	return true
}

func sortedServerNames(m map[string]*serverStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedToolNames(m map[string]*KeyStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Propose derives the rules the record supports, most-evidenced first.
//
// current is the policy in force: a key it already answers is skipped,
// because proposing what is already settled is noise — and after
// backfilling from old transcripts, where a gate answer cannot always be
// told from a policy that was in force at the time, it is also the
// correction for that ambiguity.
//
// projectDir is the confinement root the Block check classifies against.
// isGlobal and toolsOf describe the connected MCP servers (ADR-0048 §2);
// both may be nil, in which case no server proposal is made.
func Propose(rep Report, current policy.Policy, projectDir string,
	isGlobal func(server string) bool, toolsOf func(server string) []string) []Proposal {

	var out []Proposal
	if isGlobal != nil && toolsOf != nil {
		out = append(out, proposeServers(rep, current, isGlobal, toolsOf)...)
	}
	for _, key := range sortedKeys(rep.Keys) {
		st := rep.Keys[key]
		if memoryTools[st.Tool] {
			continue
		}
		// MCP tools are proposed per server, in the global scope, and
		// never per tool: a rule about one lookup tool while its
		// siblings still ask describes no decision an operator makes
		// (ADR-0048 §4).
		if _, isMCP := ServerOf(st.Tool); isMCP {
			continue
		}
		switch {
		case st.DenySessions >= denySessionsForAlways:
			if current.ForCall(st.Tool, commandOf(st)) == policy.AlwaysAsk {
				continue
			}
			out = append(out, Proposal{KeyStats: *st, Decision: "always", Pattern: st.Key})
		case st.DenySessions == 0 &&
			(st.TypedApprovals >= approvalsForNever || st.ApproveSessions >= approveSessionsForNever):
			if current.ForCall(st.Tool, commandOf(st)) != policy.Default {
				continue
			}
			// A key whose calls the rule tier blocks cannot be taken off
			// the gate — "never" does not lift the Block floor — so
			// offering it would be offering a rule that changes nothing.
			if blocksAnyExample(st, projectDir) {
				continue
			}
			out = append(out, Proposal{KeyStats: *st, Decision: "never", Pattern: st.Key})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].evidence() > out[j].evidence()
	})
	return out
}

// evidence ranks proposals for display: the strongest record first. A
// server proposal ranks by how many of its tools the operator approved,
// which is the count its threshold is about.
func (p Proposal) evidence() int {
	switch {
	case p.Decision == "always":
		return p.DenySessions
	case p.Server != "":
		return len(p.CoveredApproved)
	default:
		return max(p.TypedApprovals, p.ApproveSessions)
	}
}

// commandOf returns a representative command for policy resolution.
// Non-shell keys have none — their key is the tool name.
func commandOf(s *KeyStats) string {
	if len(s.Examples) == 0 {
		return s.Key
	}
	return s.Examples[0]
}

// blocksAnyExample reports whether the rule tier blocks any recorded
// example. A Block verdict is decided per call, so one blocked example
// is enough to say this key is not one the gate can be taken off.
func blocksAnyExample(s *KeyStats, projectDir string) bool {
	for _, ex := range s.Examples {
		if risk.Classify(s.Tool, true, map[string]any{"command": ex}, projectDir).Tier == risk.Block {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]*KeyStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SessionsDir is the transcript root Scan reads, resolved the same way
// the rest of gem-agent resolves it.
func SessionsDir() (string, error) {
	dir, err := session.DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Clean(dir), nil
}

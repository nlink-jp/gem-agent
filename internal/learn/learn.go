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
	"time"

	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/session"
)

// Thresholds for proposing a rule (ADR-0045 §5). They are constants, not
// configuration: there is no evidence yet that tuning is needed, and a
// knob here would be a knob on how readily approvals are given away.
const (
	// approveSessionsForNever is how many separate sessions must have
	// approved a key, with no denial anywhere, before "never" is
	// proposed.
	approveSessionsForNever = 3
	// denySessionsForAlways is the bar for proposing "always".
	// Tightening gets the lower one, in the spirit of the Block
	// patterns: a generous match costs one approval prompt.
	denySessionsForAlways = 2
	// examplesKept bounds the evidence shown per proposal.
	examplesKept = 3
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
}

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
func Propose(rep Report, current policy.Policy, projectDir string) []Proposal {
	var out []Proposal
	for _, key := range sortedKeys(rep.Keys) {
		st := rep.Keys[key]
		if memoryTools[st.Tool] {
			continue
		}
		switch {
		case st.DenySessions >= denySessionsForAlways:
			if current.ForCall(st.Tool, commandOf(st)) == policy.AlwaysAsk {
				continue
			}
			out = append(out, Proposal{KeyStats: *st, Decision: "always"})
		case st.DenySessions == 0 && st.ApproveSessions >= approveSessionsForNever:
			if current.ForCall(st.Tool, commandOf(st)) != policy.Default {
				continue
			}
			// A key whose calls the rule tier blocks cannot be taken off
			// the gate — "never" does not lift the Block floor — so
			// offering it would be offering a rule that changes nothing.
			if blocksAnyExample(st, projectDir) {
				continue
			}
			out = append(out, Proposal{KeyStats: *st, Decision: "never"})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].evidence() > out[j].evidence()
	})
	return out
}

// evidence ranks proposals for display: the strongest record first.
func (p Proposal) evidence() int {
	if p.Decision == "always" {
		return p.DenySessions
	}
	return p.ApproveSessions
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

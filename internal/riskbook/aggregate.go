// Package riskbook implements the risk rulebook (ADR-0050): layered,
// operator-authored guidance the risk-evaluation model reads on every
// Review-tier call. A hand-written global base and a per-project layer
// stack; learning is one authoring tool for the project layer, a text
// editor is the other.
//
// This file is the learning tool's deterministic half: it aggregates
// the operator's recorded gate decisions against the ladder's recorded
// verdicts, and renders the enumeration a summary model will read. It
// never proposes and never writes — the summarizer drafts, and nothing
// takes effect until the operator has read all of it (ADR-0050 §3).
package riskbook

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nlink-jp/gem-agent/internal/session"
)

// examplesKept bounds the sample call details carried per key. The
// operator chose to include full command lines in the summarizer's
// input (ADR-0050 §3) — the samples are that context.
const examplesKept = 5

// KeyStats is what the record says about one aggregation key: the
// ladder's verdicts and the operator's answers, side by side.
//
// Counting is ADR-0048 §1: typed answers count individually (each one
// is a decision the operator actually made), allowlist answers collapse
// to one vote per session per key (one keystroke stands in for any
// number of calls).
type KeyStats struct {
	Key  string
	Tool string
	// TypedApprovals counts approvals the operator typed, individually.
	TypedApprovals int
	// ApproveSessions / DenySessions count distinct sessions with that
	// outcome at the gate (any source).
	ApproveSessions int
	DenySessions    int
	// TypedDenials counts denials the operator typed, individually.
	TypedDenials int
	// ModelApproved / ModelEscalated count the auto-mode ladder's own
	// verdicts — how the judge behaved, not what the operator wants.
	ModelApproved  int
	ModelEscalated int
	// Examples are call details the operator saw at the gate.
	Examples []string
	// LastSeen is the newest decision on this key.
	LastSeen time.Time
}

// Report is the aggregate over one project's transcripts.
type Report struct {
	Sessions   int
	Scanned    int
	Unreadable int
	Decisions  int
	Keys       map[string]*KeyStats
}

// gateRecord / autoRecord are the decoded shapes of the transcript
// diagnostics (ADR-0045 §7, ADR-0048 §1). Only needed fields decode;
// attachment payloads are never touched.
type gateRecord struct {
	Name     string `json:"name"`
	Decision string `json:"decision"`
	Key      string `json:"key"`
	Detail   string `json:"detail"`
	// Source is "operator" or "allowlist". Records from before the
	// distinction existed are read as allowlist — the conservative
	// direction (an unknown must not count as a typed decision).
	Source string `json:"source"`
}

func (r gateRecord) typed() bool { return r.Source == "operator" }

type autoRecord struct {
	Name     string `json:"name"`
	Approved bool   `json:"approved"`
	Key      string `json:"key"`
}

// Scan aggregates the operator's decisions across one project's
// transcripts. Scoped to this project: risk differs per project, which
// is the reason the rulebook layers exist at all (ADR-0050 §1).
func Scan(dir, projectDir string) (Report, error) {
	rep := Report{Keys: map[string]*KeyStats{}}
	metas, err := session.List(dir, projectDir)
	if err != nil {
		return rep, err
	}
	for _, m := range metas {
		if err := rep.scanOne(m.Path); err != nil {
			// One unreadable transcript is a gap in the evidence, not a
			// failure of the command: count it, keep going.
			rep.Unreadable++
			continue
		}
		rep.Scanned++
	}
	rep.Sessions = len(metas)
	return rep, nil
}

func (r *Report) scanOne(path string) error {
	approved := map[string]bool{}
	denied := map[string]bool{}
	return session.Scan(path, func(kind string, ts time.Time, data json.RawMessage) error {
		switch kind {
		case "gate_decision":
			var rec gateRecord
			if json.Unmarshal(data, &rec) != nil || rec.Key == "" {
				// An empty key marks a call too complex to name (a
				// compound or dynamic command); no summary should
				// generalize from what cannot be named.
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
				if rec.typed() {
					st.TypedDenials++
				}
				st.addExample(rec.Detail)
			default:
				return nil
			}
			r.Decisions++
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

// HasDecisions reports whether the record holds any operator decision —
// the learning tool has nothing to say without one.
func (r Report) HasDecisions() bool { return r.Decisions > 0 }

// RenderEnumeration renders the per-key record as the text the summary
// model reads: for each key, the ladder's verdicts beside the
// operator's answers, with sample call details. Deterministic — same
// records, same bytes.
func RenderEnumeration(rep Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "decision record: %d sessions scanned, %d gate decisions\n\n",
		rep.Scanned, rep.Decisions)
	for _, key := range sortedKeys(rep.Keys) {
		st := rep.Keys[key]
		if st.ApproveSessions == 0 && st.DenySessions == 0 &&
			st.ModelApproved == 0 && st.ModelEscalated == 0 {
			continue
		}
		fmt.Fprintf(&b, "pattern: %s (tool %s)\n", st.Key, st.Tool)
		fmt.Fprintf(&b, "  auto-mode verdicts: approved %d, escalated %d\n",
			st.ModelApproved, st.ModelEscalated)
		fmt.Fprintf(&b, "  operator at the gate: typed approvals %d, typed denials %d, approving sessions %d, denying sessions %d\n",
			st.TypedApprovals, st.TypedDenials, st.ApproveSessions, st.DenySessions)
		for _, ex := range st.Examples {
			fmt.Fprintf(&b, "  example: %s\n", ex)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeys(m map[string]*KeyStats) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

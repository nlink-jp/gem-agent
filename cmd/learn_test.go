package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/learn"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// learnFixture writes n sessions that each approve the same command, and
// returns a runner wired to a scratch policy file.
func learnFixture(t *testing.T, n int, answer func(int) (bool, error)) (*learnRunner, string, *int) {
	t.Helper()
	dir := t.TempDir()
	proj := "/work/proj"
	ids := []string{"20260820-100000", "20260821-100000", "20260822-100000", "20260823-100000"}
	for i := range n {
		var b strings.Builder
		add := func(kind string, data any) {
			rec, err := json.Marshal(map[string]any{
				"ts": "2026-08-26T10:00:00Z", "kind": kind, "data": data,
			})
			if err != nil {
				t.Fatal(err)
			}
			b.Write(rec)
			b.WriteByte('\n')
		}
		add(session.KindHeader, map[string]any{
			"schema": session.SchemaVersion, "version": "test", "model": "m", "project": proj,
		})
		add(session.KindMessage, map[string]any{"role": "user", "content": "go"})
		add("gate_decision", map[string]any{
			"name": "shell_exec", "decision": "approved",
			"key": "go test", "detail": "go test ./...",
		})
		if err := os.WriteFile(filepath.Join(dir, ids[i]+".jsonl"), []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	policyPath := filepath.Join(t.TempDir(), config.PolicyFileName)
	asked := 0
	r := &learnRunner{
		sessionsDir: dir,
		projectDir:  proj,
		policyPath:  policyPath,
		reload:      func() error { return nil },
		current: func() policy.Policy {
			p, _, err := policy.Build(nil, nil, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			return p
		},
		msgs: uitext.For(uitext.EN),
		ask: func(ctx context.Context, q string, ev []string, accept, skip string) (bool, error) {
			asked++
			return answer(asked)
		},
	}
	return r, policyPath, &asked
}

func runLearn(t *testing.T, r *learnRunner, ctx context.Context) string {
	t.Helper()
	var out strings.Builder
	r.Run(ctx, &out)
	return out.String()
}

// The accepted rule reaches the machine-owned policy file, under this
// project (ADR-0045 §6).
func TestLearnWritesAnAcceptedRule(t *testing.T) {
	r, policyPath, asked := learnFixture(t, 3, func(int) (bool, error) { return true, nil })
	out := runLearn(t, r, context.Background())

	if *asked != 1 {
		t.Fatalf("asked %d times, want 1", *asked)
	}
	pf, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := pf.CommandsFor("/work/proj")["go test"]; got != "never" {
		t.Errorf("saved rule = %q, want never", got)
	}
	if !strings.Contains(out, "go test = never") {
		t.Errorf("output does not report the saved rule:\n%s", out)
	}
}

// Declining writes nothing: a proposal is an offer, not a plan.
func TestLearnSkipWritesNothing(t *testing.T) {
	r, policyPath, _ := learnFixture(t, 3, func(int) (bool, error) { return false, nil })
	runLearn(t, r, context.Background())

	pf, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.CommandsFor("/work/proj")) != 0 {
		t.Errorf("a skipped proposal was written: %v", pf.Projects)
	}
}

// A declined dialog (Esc) stops the pass and says so, rather than
// carrying on asking about the rest.
func TestLearnStopsWhenTheOperatorDeclines(t *testing.T) {
	r, _, asked := learnFixture(t, 3, func(int) (bool, error) {
		return false, errors.New("declined")
	})
	out := runLearn(t, r, context.Background())
	if *asked != 1 {
		t.Errorf("asked %d times after a decline", *asked)
	}
	if !strings.Contains(out, "stopped") {
		t.Errorf("a stopped pass did not say so:\n%s", out)
	}
}

// An interrupted pass asks nothing further.
func TestLearnStopsOnCancellation(t *testing.T) {
	r, _, asked := learnFixture(t, 3, func(int) (bool, error) { return true, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runLearn(t, r, ctx)
	if *asked != 0 {
		t.Errorf("asked %d times despite a cancelled context", *asked)
	}
}

// Nothing to propose is a normal outcome, and it reports how much was
// read: "nothing yet" and "nothing looked at" must not read alike.
func TestLearnReportsAnEmptyResultWithItsScope(t *testing.T) {
	r, _, asked := learnFixture(t, 1, func(int) (bool, error) { return true, nil })
	out := runLearn(t, r, context.Background())
	if *asked != 0 {
		t.Errorf("asked about a key below the threshold")
	}
	if !strings.Contains(out, "1 sessions") {
		t.Errorf("output does not say how much was read:\n%s", out)
	}
}

// The evidence shown is what the record says, including the auto-mode
// wobble when the model tier judged the same key both ways.
func TestLearnEvidenceIncludesTheWobble(t *testing.T) {
	r, _, _ := learnFixture(t, 3, func(int) (bool, error) { return false, nil })
	lines := r.evidence(proposalWithWobble())
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "approved 4 and escalated 7") {
		t.Errorf("evidence lacks the wobble line:\n%s", joined)
	}
	if !strings.Contains(joined, "go test ./...") {
		t.Errorf("evidence lacks the example calls:\n%s", joined)
	}
}

func proposalWithWobble() learn.Proposal {
	return learn.Proposal{
		Decision: "never",
		KeyStats: learn.KeyStats{
			Key: "go test", Tool: "shell_exec",
			ApproveSessions: 3,
			ModelApproved:   4,
			ModelEscalated:  7,
			Examples:        []string{"go test ./..."},
		},
	}
}

// ADR-0046 §4: an MCP proposal shows the server's own description, as
// the server's claim — it informs the operator, and decided nothing.
func TestLearnEvidenceShowsMCPDescription(t *testing.T) {
	r, _, _ := learnFixture(t, 3, func(int) (bool, error) { return false, nil })
	r.describe = func(tool string) string {
		if tool == "mcp__asn__lookup_ip" {
			return "Resolves an IP to its AS from a local database, fully offline."
		}
		return ""
	}
	mcp := learn.Proposal{Decision: "never", KeyStats: learn.KeyStats{
		Key: "mcp__asn__lookup_ip", Tool: "mcp__asn__lookup_ip", ApproveSessions: 3,
	}}
	joined := strings.Join(r.evidence(mcp), "\n")
	if !strings.Contains(joined, "fully offline") {
		t.Errorf("MCP evidence lacks the description:\n%s", joined)
	}
	if !strings.Contains(joined, "the server describes") {
		t.Errorf("the description is not attributed to the server:\n%s", joined)
	}
	// A shell key has no server to quote.
	shell := learn.Proposal{Decision: "never", KeyStats: learn.KeyStats{
		Key: "go test", Tool: "shell_exec", ApproveSessions: 3,
	}}
	if strings.Contains(strings.Join(r.evidence(shell), "\n"), "the server describes") {
		t.Error("a shell proposal carried a server description")
	}
}

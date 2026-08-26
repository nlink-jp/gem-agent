package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/learn"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// learnRunner drives /learn: read this project's transcripts, propose
// rules, and write the ones the operator confirms (ADR-0045 §1, §6).
//
// It is UI-free by construction — asking is an injected function, and
// output goes to a writer — because the two surfaces differ in how they
// may ask. In the TUI the question is a dialog driven from a goroutine;
// in the plain REPL it is a numbered prompt on the same goroutine that
// owns stdin.
type learnRunner struct {
	sessionsDir string
	projectDir  string
	policyPath  string
	// reload re-reads the policy file and pushes the rebuilt policy into
	// the live agent, so an accepted rule takes effect immediately —
	// the same contract /settings has.
	reload func() error
	// current returns the policy in force, for skipping settled keys.
	current func() policy.Policy
	// describe returns a tool's published description, empty for
	// anything but a connected MCP tool. The operator judging
	// "lookup or messenger?" should not have to recall it (ADR-0046 §4).
	describe func(tool string) string
	// ask presents one proposal and returns true to accept it.
	ask  func(ctx context.Context, question string, evidence []string, accept, skip string) (bool, error)
	msgs *uitext.Messages
}

// Run executes one /learn pass, writing progress to out.
//
// Interruption and a declined dialog stop the pass rather than failing
// it: rules already written stay written, and the closing line says so.
// Silently continuing to ask after an interrupt would be worse — the
// operator asked to stop.
func (r *learnRunner) Run(ctx context.Context, out *strings.Builder) {
	rep, err := learn.Scan(r.sessionsDir, r.projectDir)
	if err != nil {
		fmt.Fprintf(out, "%s\n", err)
		return
	}
	proposals := learn.Propose(rep, r.current(), r.projectDir)
	if rep.Unreadable > 0 {
		fmt.Fprintf(out, r.msgs.LearnUnreadableFmt+"\n", rep.Unreadable)
	}
	if len(proposals) == 0 {
		fmt.Fprintf(out, r.msgs.LearnNoneFmt+"\n", rep.Sessions)
		return
	}
	fmt.Fprintf(out, r.msgs.LearnScannedFmt+"\n", rep.Sessions, len(proposals))

	written := 0
	for _, p := range proposals {
		if ctx.Err() != nil {
			break
		}
		ok, err := r.ask(ctx, r.question(p), r.evidence(p), r.msgs.LearnAccept, r.msgs.LearnSkip)
		if err != nil {
			// Declined (Esc) or no operator to ask: stop the pass.
			fmt.Fprintf(out, "%s\n", r.msgs.LearnStopped)
			r.summarise(out, written)
			return
		}
		if !ok {
			continue
		}
		if err := r.save(p); err != nil {
			fmt.Fprintf(out, r.msgs.LearnSaveFailedFmt+"\n", err)
			continue
		}
		written++
		fmt.Fprintf(out, r.msgs.LearnSavedFmt+"\n", p.Key, p.Decision)
	}
	if ctx.Err() != nil {
		fmt.Fprintf(out, "%s\n", r.msgs.LearnStopped)
	}
	r.summarise(out, written)
}

func (r *learnRunner) summarise(out *strings.Builder, written int) {
	if written > 0 {
		fmt.Fprintf(out, r.msgs.LearnDoneFmt+"\n", written)
	}
}

func (r *learnRunner) question(p learn.Proposal) string {
	if p.Decision == "always" {
		return fmt.Sprintf(r.msgs.LearnAskAlwaysFmt, p.Key)
	}
	return fmt.Sprintf(r.msgs.LearnAskNeverFmt, p.Key)
}

// evidence renders what the record says, so the operator decides on
// facts rather than on the proposal's say-so. The wobble line is the
// case for the rule where the model tier has been inconsistent.
func (r *learnRunner) evidence(p learn.Proposal) []string {
	var lines []string
	if p.Decision == "always" {
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceDeniedFmt, p.DenySessions))
	} else {
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceApprovedFmt, p.ApproveSessions))
	}
	if p.ModelApproved > 0 && p.ModelEscalated > 0 {
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceWobbleFmt, p.ModelApproved, p.ModelEscalated))
	}
	// The server's claim about its own tool, shown as such. It informs
	// the operator's judgement; it is not aggregated and it never
	// decided the proposal (ADR-0046 §3).
	if r.describe != nil {
		if desc := strings.TrimSpace(r.describe(p.Tool)); desc != "" {
			lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceDescFmt, clipLine(desc, 200)))
		}
	}
	if len(p.Examples) > 0 {
		lines = append(lines, r.msgs.LearnEvidenceExamples)
		for _, ex := range p.Examples {
			lines = append(lines, "  "+clipLine(ex, 100))
		}
	}
	return lines
}

// save writes one accepted rule through the same locked read-modify-write
// path /settings uses, then reloads so it takes effect at once.
func (r *learnRunner) save(p learn.Proposal) error {
	if _, err := config.MutatePolicyFile(r.policyPath, func(pf *config.PolicyFile) {
		pf.SetCommand(r.projectDir, p.Key, p.Decision)
	}); err != nil {
		return err
	}
	return r.reload()
}

func clipLine(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

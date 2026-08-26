package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/learn"
	"github.com/nlink-jp/gem-agent/internal/policy"
	"github.com/nlink-jp/gem-agent/internal/tools"
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
	// isGlobalServer and serverTools describe the connected MCP servers
	// (ADR-0048 §2): whether a server came from the operator's own
	// config rather than this project's .mcp.json, and which tools a
	// server rule would cover.
	isGlobalServer func(server string) bool
	serverTools    func(server string) []string
	// ask presents one proposal and returns true to accept it. The
	// evidence is emitted separately, so the dialog stays short enough
	// that nothing it shows gets clipped.
	ask func(ctx context.Context, question string, accept, skip string) (bool, error)
	// emit writes lines where the operator will see them as they
	// happen — scrollback in the TUI, stderr in the plain REPL.
	emit func(lines []string)
	msgs *uitext.Messages
}

// Run executes one /learn pass, writing progress to out.
//
// Interruption and a declined dialog stop the pass rather than failing
// it: rules already written stay written, and the closing line says so.
// Silently continuing to ask after an interrupt would be worse — the
// operator asked to stop.
func (r *learnRunner) Run(ctx context.Context) {
	say := func(format string, args ...any) { r.emit([]string{fmt.Sprintf(format, args...)}) }

	rep, err := learn.Scan(r.sessionsDir, r.projectDir)
	if err != nil {
		say("%s", err)
		return
	}
	proposals := learn.Propose(rep, r.current(), r.projectDir, r.isGlobalServer, r.serverTools)
	if rep.Unreadable > 0 {
		say(r.msgs.LearnUnreadableFmt, rep.Unreadable)
	}
	if len(proposals) == 0 {
		say(r.msgs.LearnNoneFmt, rep.Sessions)
		return
	}
	say(r.msgs.LearnScannedFmt, rep.Sessions, len(proposals))

	written := 0
	for _, p := range proposals {
		if ctx.Err() != nil {
			break
		}
		// The evidence goes to the scrollback, not into the dialog: a
		// server rule's covered-tool list is the disclosure that makes
		// it honest (ADR-0048 §3), and a dialog clips to its height
		// budget — exactly the lines that must not be clipped. The
		// dialog asks the question; the operator reads the evidence
		// above it.
		r.emit(append([]string{r.question(p)}, r.evidence(p)...))
		ok, err := r.ask(ctx, r.question(p), r.msgs.LearnAccept, r.msgs.LearnSkip)
		if err != nil {
			// Declined (Esc) or no operator to ask: stop the pass.
			say("%s", r.msgs.LearnStopped)
			r.summarise(written)
			return
		}
		if !ok {
			continue
		}
		if err := r.save(p); err != nil {
			say(r.msgs.LearnSaveFailedFmt, err)
			continue
		}
		written++
		// Reported as it happens: answering five proposals before
		// learning whether any of them saved is a silent wait, and a
		// failed write has to surface next to the answer that caused it.
		say(r.msgs.LearnSavedFmt, p.Pattern, p.Decision)
	}
	if ctx.Err() != nil {
		say("%s", r.msgs.LearnStopped)
	}
	r.summarise(written)
}

func (r *learnRunner) summarise(written int) {
	if written > 0 {
		r.emit([]string{fmt.Sprintf(r.msgs.LearnDoneFmt, written)})
	}
}

func (r *learnRunner) question(p learn.Proposal) string {
	switch {
	case p.Server != "":
		return fmt.Sprintf(r.msgs.LearnAskServerFmt, p.Server)
	case p.Decision == "always":
		return fmt.Sprintf(r.msgs.LearnAskAlwaysFmt, p.Key)
	default:
		return fmt.Sprintf(r.msgs.LearnAskNeverFmt, p.Key)
	}
}

// evidence renders what the record says, so the operator decides on
// facts rather than on the proposal's say-so. The wobble line is the
// case for the rule where the model tier has been inconsistent.
func (r *learnRunner) evidence(p learn.Proposal) []string {
	var lines []string
	switch {
	case p.Server != "":
		// Diversity is what cleared this proposal's bar, so it is what
		// the evidence line reports (ADR-0048 §2).
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceServerFmt, len(p.CoveredApproved)))
		// A rule that reaches beyond this project says so before it is
		// agreed to, not after.
		lines = append(lines, r.msgs.LearnScopeGlobal)
	case p.Decision == "always":
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceDeniedFmt, p.DenySessions))
	default:
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceApprovedFmt, p.ApproveSessions))
	}
	if p.ModelApproved > 0 && p.ModelEscalated > 0 {
		lines = append(lines, fmt.Sprintf(r.msgs.LearnEvidenceWobbleFmt, p.ModelApproved, p.ModelEscalated))
	}
	// The server's claim about its own tool, shown as such. It informs
	// the operator's judgement; it is not aggregated and it never
	// decided the proposal (ADR-0046 §3).
	if p.Server == "" && r.describe != nil {
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
	// A server rule grants more than the evidence for it: it covers
	// tools the operator has never called. They must see exactly how
	// much before agreeing (ADR-0048 §3).
	if p.Server != "" {
		lines = append(lines, r.coverage(p)...)
	}
	return lines
}

// coveredToolsShown bounds each covered-tool list. A longer list is
// clipped with its remainder counted, never silently.
const coveredToolsShown = 8

// coverage renders what a server rule would cover, split into the tools
// already approved and the ones not yet used.
func (r *learnRunner) coverage(p learn.Proposal) []string {
	lines := []string{fmt.Sprintf(r.msgs.LearnCoversApprovedFmt, len(p.CoveredApproved))}
	lines = append(lines, r.toolLines(p.CoveredApproved)...)
	if len(p.CoveredUnused) > 0 {
		lines = append(lines, fmt.Sprintf(r.msgs.LearnCoversUnusedFmt, len(p.CoveredUnused)))
		lines = append(lines, r.toolLines(p.CoveredUnused)...)
	}
	return lines
}

// toolLines renders tool names with their server-published description
// (ADR-0046: a claim, and the operator reads it as one).
func (r *learnRunner) toolLines(names []string) []string {
	var lines []string
	for i, name := range names {
		if i == coveredToolsShown {
			lines = append(lines, "  "+fmt.Sprintf(r.msgs.LearnCoversMoreFmt, len(names)-i))
			break
		}
		line := "  " + name
		if r.describe != nil {
			if d := strings.TrimSpace(r.describe(name)); d != "" {
				// One sentence per tool: the full description of twenty
				// tools is a wall, and the first sentence answers "what
				// does this one do".
				line += " — " + clipLine(firstSentence(d), 90)
			}
		}
		lines = append(lines, line)
	}
	return lines
}

// save writes one accepted rule through the same locked read-modify-write
// path /settings uses, then reloads so it takes effect at once.
func (r *learnRunner) save(p learn.Proposal) error {
	if _, err := config.MutatePolicyFile(r.policyPath, func(pf *config.PolicyFile) {
		switch {
		case p.Global:
			// An MCP server's behaviour does not vary by project, so
			// its rule does not either (ADR-0048 §2). This is the
			// ADR-0008 tool table, not the command vocabulary.
			pf.Set("", p.Pattern, p.Decision)
		case p.Tool == tools.ShellExecName:
			pf.SetCommand(r.projectDir, p.Pattern, p.Decision)
		default:
			pf.Set(r.projectDir, p.Pattern, p.Decision)
		}
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

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// What the model is told when a skill is not loaded (ADR-0119 §10).
// load_skill results are sent unwrapped, as the operator's own
// instructions, so these are fixed sentences that take the skill's
// validated name and nothing else — never the directory entry, a path or
// the text of an error. Each one says not to fetch the files another
// way: a denied load must not become a read_file of the same SKILL.md.
const (
	skillRefusalDisabled   = "The skill %q is disabled for this session: its content is not what the operator trusted, and the operator chose not to use it. Do not read its files by other means. Continue without it."
	skillRefusalLater      = "The skill %q was not loaded: its content is not what the operator trusted, and the operator has not decided about it. Do not read its files by other means. Continue without it; asking for the skill again later puts the question to them again."
	skillRefusalUnattended = "The skill %q was not loaded: its content is not what the operator trusted, and nobody is at the terminal to confirm it. Do not read its files by other means. Continue without it; the operator can re-trust it with `gem-agent trust --accept`."
	skillRefusalChanging   = "The skill %q was not loaded: its files are still changing. Do not read its files by other means. Continue without it, or ask for it again later."
	skillErrGone           = "skill %q: its directory is gone"
	skillErrRecord         = "skill %q: the operator trusted it, but the trust could not be recorded, so it was not loaded"
)

// skillGuard is the compare-before-consuming rule applied where a
// project skill is actually consumed (ADR-0119). The pin was compared
// when the list of skills was built; the body and the supporting files
// are read from disk each time the skill is used, and until this guard
// nothing compared them then: a skill rewritten after the session
// started reached the model, unwrapped, through load_skill and through
// /skill (measured on the sibling runtime, lagent v0.27.4; the reads
// here are the same calls).
//
// Every read goes through attempt. A skill that is what the record
// holds is read. One that is not becomes the operator's question, with
// three outcomes: trust (that content is recorded and read), disable
// (off for the session) and no answer (this one read is refused,
// nothing changes). Where nobody is at a terminal nothing is asked.
//
// A nil guard reads without comparing: the tests of the reads
// themselves, and a tool registered before a session exists.
type skillGuard struct {
	cfg        *config.Config
	policyFile *config.PolicyFile
	policyPath string
	projectDir string
	// trusted: the project is trusted this session. interactive:
	// somebody is at a terminal — the predicate the pin check at
	// startup uses, so a one-shot run and a piped run are both "nobody".
	trusted, interactive bool
	// include takes a name out of the session's exclusion set: a skill
	// the operator has just trusted is no longer one the session left
	// out (§4).
	include func(pin string)
	// ask puts the question and returns the chosen option, or -1 for
	// every way of not answering.
	ask    func(ctx context.Context, heading, question string, options []string, def int) int
	notice func(string)
	log    func(kind string, data any)
	msgs   *uitext.Messages

	mu       sync.Mutex
	disabled map[string]bool // by pin name
}

type skillState int

const (
	skillCurrent skillState = iota
	skillDisabled
	skillGone
	skillChanged
	skillUnrecorded
)

func (g *skillGuard) isDisabled(pin string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.disabled[pin]
}

// forget drops every disable answer. It is called where the list of
// skills is rebuilt and nowhere else (§5): from there on the pin
// re-check decides which skills are listed at all.
func (g *skillGuard) forget() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.disabled = nil
}

// check compares s with the trust record. The disable answer is read
// before anything else. The digest is taken through the skill's own
// root — the tree Body and File read — by the implementation that took
// the pin. Pins off, an untrusted project or a project with nothing
// recorded compare nothing: that is the state the list was built in.
// The exclusion set is not consulted: a listed skill is one the session
// loaded.
func (g *skillGuard) check(s skills.Skill) (skillState, string) {
	if g == nil || s.Scope != "project" {
		return skillCurrent, ""
	}
	pin := trustpin.SkillPinName(s.Entry)
	if g.isDisabled(pin) {
		return skillDisabled, ""
	}
	if !g.trusted || !g.cfg.Approval.PinTrustedFiles {
		return skillCurrent, ""
	}
	rec, _ := recordedPolicy(g.policyPath, g.projectDir, g.policyFile)
	if !rec.HasPins(g.projectDir) {
		return skillCurrent, ""
	}
	digest, gone := "", false
	err := s.WithRoot(func(root *os.Root) error {
		if _, err := root.Stat("SKILL.md"); err != nil {
			gone = true
			return nil
		}
		digest, _ = trustpin.SkillDigest(root)
		return nil
	})
	if err != nil || gone {
		return skillGone, ""
	}
	switch recorded := rec.PinsFor(g.projectDir)[pin]; {
	case recorded == "":
		return skillUnrecorded, digest
	case recorded != digest:
		return skillChanged, digest
	}
	return skillCurrent, digest
}

// event writes one record of its own kind. A trust answer is not the
// approval of a call, so it is not a gate decision (§12).
func (g *skillGuard) event(s skills.Skill, event string) {
	if g.log != nil {
		g.log("skill_trust", map[string]any{"name": s.Name, "entry": s.Entry, "event": event})
	}
}

func (g *skillGuard) say(format string, s skills.Skill) {
	if g.notice != nil {
		g.notice(fmt.Sprintf(format, s.Name))
	}
}

type skillTrustKey struct{}

type skillTrustAnswer struct{ entry, digest string }

// withSkillTrust carries the operator's trust answer to the read that
// follows it: this entry, as it was when the question was put.
func withSkillTrust(ctx context.Context, entry, digest string) context.Context {
	return context.WithValue(ctx, skillTrustKey{}, skillTrustAnswer{entry, digest})
}

func skillTrustFrom(ctx context.Context, entry string) (string, bool) {
	a, ok := ctx.Value(skillTrustKey{}).(skillTrustAnswer)
	if !ok || a.entry != entry {
		return "", false
	}
	return a.digest, true
}

// attempt reads s through load, or says why not. refusal, when set, is
// the fixed sentence for the model and nothing was read. An error that
// is a *skillQuestion means the operator has to be asked first.
//
// Every read is bracketed: the directory is digested before it and
// once more after it, and what was read is returned only when both
// digests are the one the record holds. A check before the read alone
// leaves the read itself open — content swapped in between would be
// returned under the name of the content that was compared.
//
// With a trust answer in ctx the order is the one §4 fixes: the
// directory is digested again and must still be what was asked about;
// only then is that digest recorded; the read runs; and the directory
// is digested once more.
func (g *skillGuard) attempt(ctx context.Context, s skills.Skill, load func() (string, error)) (out, refusal string, err error) {
	state, digest := g.check(s)
	if state == skillCurrent {
		out, err = load()
		after, d := g.check(s)
		if after == skillCurrent && d == digest {
			return out, "", err
		}
		// It moved during the read. What was read is discarded — every
		// return below names its own result — and the skill is handled
		// as what it is now.
		if after == skillCurrent {
			// The record moved with it (a re-trust elsewhere, mid-read):
			// what was read may be either content. Not this call.
			return "", "", &skillQuestion{g: g, s: s, moved: true}
		}
		state, digest = after, d
	}
	switch state {
	case skillDisabled:
		g.event(s, "refused")
		g.say(g.msgs.SkillDisabledFmt, s)
		return "", fmt.Sprintf(skillRefusalDisabled, s.Name), nil
	case skillGone:
		g.event(s, "gone")
		g.say(g.msgs.SkillGoneFmt, s)
		return "", "", fmt.Errorf(skillErrGone, s.Name)
	}
	if trusted, ok := skillTrustFrom(ctx, s.Entry); ok {
		if trusted != digest {
			return "", "", &skillQuestion{g: g, s: s, digest: digest, unrecorded: state == skillUnrecorded}
		}
		if err := g.record(s, digest); err != nil {
			return "", "", fmt.Errorf(skillErrRecord, s.Name)
		}
		out, err = load()
		if after, d := g.check(s); after != skillCurrent || d != digest {
			return "", "", &skillQuestion{g: g, s: s, moved: true}
		}
		return out, "", err
	}
	if !g.interactive {
		g.event(s, "unattended")
		g.say(g.msgs.SkillUnattendedFmt, s)
		return "", fmt.Sprintf(skillRefusalUnattended, s.Name), nil
	}
	return "", "", &skillQuestion{g: g, s: s, digest: digest, unrecorded: state == skillUnrecorded}
}

// record writes the pin of this one name as the digest the operator was
// asked about, under the policy file's lock, and takes the name out of
// the session's exclusion set.
func (g *skillGuard) record(s skills.Skill, digest string) error {
	pin := trustpin.SkillPinName(s.Entry)
	if err := mutatePins(g.policyPath, g.projectDir, g.policyFile, func(pins trustpin.Pins) { pins[pin] = digest }); err != nil {
		if g.notice != nil {
			g.notice(fmt.Sprintf("could not record the trust of skill %s: %v", s.Name, err))
		}
		return err
	}
	if g.include != nil {
		g.include(pin)
	}
	g.event(s, "trusted")
	g.say(g.msgs.SkillTrustedFmt, s)
	return nil
}

// skillQuestion is the operator's question about one skill, as the
// agent sees it (tools.NeedsOperator): it is put outside the floor that
// abandons a slow tool, and the tool then runs again.
type skillQuestion struct {
	g *skillGuard
	s skills.Skill
	// digest is what the question is about.
	digest     string
	unrecorded bool
	// moved: the content changed while it was being read after a trust
	// answer. There is nothing left to ask about this call.
	moved bool
}

func (q *skillQuestion) Error() string {
	return "skill " + q.s.Name + " is not what the trust record holds"
}

// Resolve puts the question. It opens on "not now", so a key pressed
// too soon changes nothing; every way of not answering — Esc, an
// interrupted turn, a closed input — is the same outcome. The directory
// is shown quoted: it is the one piece of the dialog the project names.
func (q *skillQuestion) Resolve(ctx context.Context) (context.Context, string) {
	g, s := q.g, q.s
	if q.moved {
		return nil, q.StillUnresolved()
	}
	if ctx.Err() != nil {
		g.event(s, "unanswered")
		return nil, fmt.Sprintf(skillRefusalLater, s.Name)
	}
	g.event(s, "question")
	line := g.msgs.SkillTrustChangedFmt
	if q.unrecorded {
		line = g.msgs.SkillTrustUnrecordedFmt
	}
	question := fmt.Sprintf(line, s.Name, strconv.Quote(s.Dir)) + "\n" + g.msgs.SkillTrustExplain
	options := []string{g.msgs.SkillTrustOptTrust, g.msgs.SkillTrustOptDisable, g.msgs.SkillTrustOptLater}
	switch g.ask(ctx, g.msgs.SkillTrustHeading, question, options, len(options)-1) {
	case 0:
		return withSkillTrust(ctx, s.Entry, q.digest), ""
	case 1:
		g.mu.Lock()
		if g.disabled == nil {
			g.disabled = map[string]bool{}
		}
		g.disabled[trustpin.SkillPinName(s.Entry)] = true
		g.mu.Unlock()
		g.event(s, "disabled")
		g.say(g.msgs.SkillDisabledFmt, s)
		return nil, fmt.Sprintf(skillRefusalDisabled, s.Name)
	}
	g.event(s, "unanswered")
	g.say(g.msgs.SkillLaterFmt, s)
	return nil, fmt.Sprintf(skillRefusalLater, s.Name)
}

// StillUnresolved is the end of a call whose skill kept changing: after
// one more question, or during the read that followed a trust answer.
func (q *skillQuestion) StillUnresolved() string {
	q.g.event(q.s, "changing")
	q.g.say(q.g.msgs.SkillChangingFmt, q.s)
	return fmt.Sprintf(skillRefusalChanging, q.s.Name)
}

var _ tools.NeedsOperator = (*skillQuestion)(nil)

// read is load_skill's read: a refusal is the call's result.
func (g *skillGuard) read(ctx context.Context, s skills.Skill, load func() (string, error)) (string, error) {
	if g == nil {
		return load()
	}
	out, refusal, err := g.attempt(ctx, s, load)
	if refusal != "" {
		return refusal, nil
	}
	return out, err
}

// readForOperator is /skill's read (§9). The operator typed the
// command, so there is no agent between them and the question: the same
// compare and the same question run here, with the same bound on how
// often it is put. loaded false with a nil error means the skill was
// refused and the operator has been told why; the turn does not start.
func (g *skillGuard) readForOperator(ctx context.Context, s skills.Skill, load func() (string, error)) (out string, loaded bool, err error) {
	if g == nil {
		out, err = load()
		return out, err == nil, err
	}
	run := ctx
	for resolved := 0; ; resolved++ {
		out, refusal, err := g.attempt(run, s, load)
		if refusal != "" {
			return "", false, nil
		}
		var q *skillQuestion
		if !errors.As(err, &q) {
			return out, err == nil, err
		}
		if resolved == tools.OperatorResolves {
			q.StillUnresolved()
			return "", false, nil
		}
		next, _ := q.Resolve(ctx)
		if next == nil {
			return "", false, nil
		}
		run = next
	}
}

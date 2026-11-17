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
	skillRefusalDisabled   = "The skill %q is disabled for this session at the operator's choice. Do not read its files by other means. Say that the skill was not loaded, and continue only with what does not depend on it."
	skillRefusalLater      = "The skill %q was not loaded: its content is not what the operator's trust record holds, and the operator has not decided about it. Do not read its files by other means, and do not ask for the skill again in this turn. Say that the skill was not loaded, and continue only with what does not depend on it."
	skillRefusalUncovered  = "The skill %q was not loaded: it is past what the operator's trust check covers. Do not read its files by other means. Say that the skill was not loaded, and continue only with what does not depend on it."
	skillRefusalUnattended = "The skill %q was not loaded: its content is not what the operator's trust record holds, and nobody is at the terminal to confirm it. Do not read its files by other means. Say that the skill was not loaded, and continue only with what does not depend on it; the operator can re-trust it with `gem-agent trust --accept`."
	skillRefusalChanging   = "The skill %q was not loaded: its files are still changing. Do not read its files by other means, and do not ask for the skill again in this turn. Say that the skill was not loaded, and continue only with what does not depend on it."
	skillRefusalGone       = "The skill %q was not loaded: it is gone or was replaced. Do not read its files by other means. Say that the skill was not loaded, and continue only with what does not depend on it."
	skillRefusalRecord     = "The skill %q was not loaded: the operator trusted it, but the trust could not be recorded. Do not read its files by other means. Say that the skill was not loaded, and continue only with what does not depend on it."
)

// errSkillMoved: the bytes a read returned are not the bytes the trust
// record's tree holds for that file — the content moved between the
// compare and the read, or was never what was compared.
var errSkillMoved = errors.New("the skill's content is not what was compared")

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
	ask    func(ctx context.Context, heading, question, hint string, options []string, def int) int
	notice func(string)
	log    func(kind string, data any)
	msgs   *uitext.Messages

	mu       sync.Mutex
	disabled map[string]bool // by pin name
	// later holds the skills whose question went unanswered in the
	// current turn. They are not asked about again until the next one:
	// a model that keeps calling load_skill must not be able to keep a
	// dialog coming, and "not now" has to mean it (§6). The value says
	// why, so a later refusal in the turn says what happened: true for a
	// skill that kept changing, false for a question left unanswered.
	later map[string]bool
}

type skillState int

const (
	skillCurrent skillState = iota
	skillDisabled
	skillGone
	// skillUncovered: the directory is past what a pin covers — too many
	// files, too many entries of every kind, or nested too deep.
	// What a pin does not cover is not served (§1).
	skillUncovered
	skillChanged
	skillUnrecorded
)

func (g *skillGuard) isDisabled(pin string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.disabled[pin]
}

// newTurn forgets which skills went unanswered: the next turn asks
// again. Every interactive turn calls it before the model runs.
func (g *skillGuard) newTurn() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.later = nil
}

func (g *skillGuard) declined(pin string) (declined, changing bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	changing, declined = g.later[pin]
	return declined, changing
}

func (g *skillGuard) decline(pin string, changing bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.later == nil {
		g.later = map[string]bool{}
	}
	g.later[pin] = changing
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
	g.disabled, g.later = nil, nil
}

// check compares s with the trust record. The disable answer is read
// before anything else. The digest is taken through the skill's own
// root — the tree Body and File read — by the implementation that took
// the pin, and it comes with its manifest, so a read can be held to the
// files of that tree. Pins off, an untrusted project or a project with
// nothing recorded compare nothing: that is the state the list was
// built in, and the manifest is nil. The exclusion set is not
// consulted: a listed skill is one the session loaded.
func (g *skillGuard) check(s skills.Skill) (skillState, string, trustpin.Manifest) {
	if g == nil || s.Scope != "project" {
		return skillCurrent, "", nil
	}
	pin := trustpin.SkillPinName(s.Entry)
	if g.isDisabled(pin) {
		return skillDisabled, "", nil
	}
	if !g.trusted || !g.cfg.Approval.PinTrustedFiles {
		return skillCurrent, "", nil
	}
	rec, _ := recordedPolicy(g.policyPath, g.projectDir, g.policyFile)
	if !rec.HasPins(g.projectDir) {
		return skillCurrent, "", nil
	}
	var (
		digest   string
		manifest trustpin.Manifest
		cut      bool
		gone     bool
	)
	err := s.WithRoot(func(root *os.Root) error {
		// The root is the tree the skill was discovered in. If the
		// directory was moved aside and another put at its path, the
		// root still reads the old one while the base-directory line
		// sends the model's commands to the new one: that skill is gone.
		held, herr := root.Stat(".")
		at, aerr := os.Stat(s.Dir)
		if herr != nil || aerr != nil || !os.SameFile(held, at) {
			gone = true
			return nil
		}
		if _, err := root.Stat("SKILL.md"); err != nil {
			gone = true
			return nil
		}
		digest, manifest, cut = trustpin.SkillManifest(root)
		return nil
	})
	switch {
	case err != nil || gone:
		return skillGone, "", nil
	case cut:
		return skillUncovered, "", nil
	}
	switch recorded := rec.PinsFor(g.projectDir)[pin]; {
	case recorded == "":
		return skillUnrecorded, digest, manifest
	case recorded != digest:
		return skillChanged, digest, manifest
	}
	return skillCurrent, digest, manifest
}

// verifier holds a read to the tree a manifest describes: the bytes
// returned for a file must be the bytes that tree holds for it, and a
// listing shows only entries the digest covers. A nil manifest — nothing
// is compared for this skill — verifies nothing.
func verifier(manifest trustpin.Manifest) *skills.Verify {
	if manifest == nil {
		return nil
	}
	return &skills.Verify{
		Content: func(rel string, data []byte) error {
			if !manifest.Holds(rel, data) {
				return errSkillMoved
			}
			return nil
		},
		Listed: manifest.Covers,
	}
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
// What is returned is bound to what was compared: load is handed a
// verifier built from the manifest of the digest that matched the
// record, and the bytes it reads are returned only if they are that
// tree's bytes for that file. A compare before the read alone leaves the
// read open — content swapped in between, or swapped in and back again,
// would be returned under the digest of something else.
//
// With a trust answer in ctx the order is the one §4 fixes: the
// directory is digested again and must still be what was asked about;
// only then is that digest recorded; and the read is held to that
// digest's manifest. A cancelled call records nothing: the write must
// not land after the operator has moved on.
func (g *skillGuard) attempt(ctx context.Context, s skills.Skill, load func(*skills.Verify) (string, error)) (out, refusal string, err error) {
	state, digest, manifest := g.check(s)
	if state == skillCurrent {
		out, err = load(verifier(manifest))
		if !errors.Is(err, errSkillMoved) {
			return out, "", err
		}
		// What was read is not what was compared. Handle the skill as
		// what it is now; if it compares as current again, it is being
		// switched back and forth, and this call ends here.
		if state, digest, manifest = g.check(s); state == skillCurrent {
			return "", "", &skillQuestion{g: g, s: s, moved: true}
		}
	}
	switch state {
	case skillDisabled:
		g.event(s, "refused")
		g.say(g.msgs.SkillDisabledFmt, s)
		return "", fmt.Sprintf(skillRefusalDisabled, s.Name), nil
	case skillGone:
		g.event(s, "gone")
		g.say(g.msgs.SkillGoneFmt, s)
		return "", fmt.Sprintf(skillRefusalGone, s.Name), nil
	case skillUncovered:
		g.event(s, "uncovered")
		g.say(g.msgs.SkillUncoveredFmt, s)
		return "", fmt.Sprintf(skillRefusalUncovered, s.Name), nil
	}
	if trusted, ok := skillTrustFrom(ctx, s.Entry); ok {
		if trusted != digest {
			return "", "", &skillQuestion{g: g, s: s, digest: digest, unrecorded: state == skillUnrecorded}
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if err := g.record(s, digest); err != nil {
			return "", fmt.Sprintf(skillRefusalRecord, s.Name), nil // record has told the operator
		}
		out, err = load(verifier(manifest))
		if errors.Is(err, errSkillMoved) {
			return "", "", &skillQuestion{g: g, s: s, moved: true}
		}
		return out, "", err
	}
	if !g.interactive {
		g.event(s, "unattended")
		g.say(g.msgs.SkillUnattendedFmt, s)
		return "", fmt.Sprintf(skillRefusalUnattended, s.Name), nil
	}
	if declined, changing := g.declined(trustpin.SkillPinName(s.Entry)); declined {
		g.event(s, "refused")
		if changing {
			g.say(g.msgs.SkillChangingFmt, s)
			return "", fmt.Sprintf(skillRefusalChanging, s.Name), nil
		}
		g.say(g.msgs.SkillLaterFmt, s)
		return "", fmt.Sprintf(skillRefusalLater, s.Name), nil
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
			g.notice(fmt.Sprintf(g.msgs.SkillRecordFailedFmt, s.Name, err))
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
	pin := trustpin.SkillPinName(s.Entry)
	if ctx.Err() != nil {
		g.decline(pin, false)
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
	answer := g.ask(ctx, g.msgs.SkillTrustHeading, question, g.msgs.SkillTrustHint, options, len(options)-1)
	if ctx.Err() != nil {
		// It arrived after the turn was interrupted: on the plain REPL
		// that is the next line the operator typed, meant for the
		// prompt. Not an answer (§3).
		answer = -1
	}
	switch answer {
	case 0:
		return withSkillTrust(ctx, s.Entry, q.digest), ""
	case 1:
		g.mu.Lock()
		if g.disabled == nil {
			g.disabled = map[string]bool{}
		}
		g.disabled[pin] = true
		g.mu.Unlock()
		g.event(s, "disabled")
		g.say(g.msgs.SkillDisabledFmt, s)
		return nil, fmt.Sprintf(skillRefusalDisabled, s.Name)
	}
	g.decline(pin, false)
	g.event(s, "unanswered")
	g.say(g.msgs.SkillLaterFmt, s)
	return nil, fmt.Sprintf(skillRefusalLater, s.Name)
}

// StillUnresolved is the end of a call whose skill kept changing: after
// one more question, or during the read that followed a trust answer.
func (q *skillQuestion) StillUnresolved() string {
	// Remembered for the turn, like an unanswered question: a model that
	// asks again at once must not raise the dialog again while the files
	// keep moving.
	q.g.decline(trustpin.SkillPinName(q.s.Entry), true)
	q.g.event(q.s, "changing")
	q.g.say(q.g.msgs.SkillChangingFmt, q.s)
	return fmt.Sprintf(skillRefusalChanging, q.s.Name)
}

var _ tools.NeedsOperator = (*skillQuestion)(nil)

// read is load_skill's read: a refusal is the call's result.
func (g *skillGuard) read(ctx context.Context, s skills.Skill, load func(*skills.Verify) (string, error)) (string, error) {
	if g == nil {
		return load(nil)
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
func (g *skillGuard) readForOperator(ctx context.Context, s skills.Skill, load func(*skills.Verify) (string, error)) (out string, loaded bool, err error) {
	if g == nil {
		out, err = load(nil)
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

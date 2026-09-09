// Package banner composes the lines gem-agent prints before the
// operator has typed anything (ADR-0078).
//
// It exists as a package, rather than as a stretch of runREPL, for two
// reasons. The rule — a line earns a place at startup only if nothing
// else will say it — needs somewhere to be stated and tested as a rule.
// And the operator-text read-through has to be able to render the
// assembled banner: `make labels` collected the fragments and scattered
// them through a flat list, so the one screen every operator meets first
// was the one thing the read-through could not read, which is how these
// lines accumulated to twenty-eight rows in the first place.
package banner

import (
	"github.com/nlink-jp/gem-agent/internal/sandbox"

	"fmt"
	"strings"
)

// Facts is everything the banner is allowed to know. Anything not in
// here cannot reach the banner, which is the point: a new feature that
// wants a line has to add a field and answer why nothing else says it.
type Facts struct {
	Version string
	Model   string
	// Instructions are the agent-facing files discovered on disk, as
	// the operator would name them. Nothing else lists these.
	Instructions []string
	// The session's inventory. Counts, not names: `/mcp`, `/skills` and
	// `/memory` print the names, one per line, better.
	Servers, Tools, Skills, Memories int
	// ResumedID and Restored are set when a session was resumed.
	ResumedID string
	Restored  int
	// The sandbox as this machine actually established it (ADR-0073).
	SandboxOn       bool
	ReadLane        bool
	ReadLanePrompts bool
	// AutoApprove reports that the session begins running mutating
	// tools unattended.
	AutoApprove bool
	// ReadOnly is the session's lane ceiling and its watcher (ADR-0080).
	// It earns a line because nothing else says it at start: the footer
	// carries the ceiling in force but shows nothing for a watcher that
	// has not fired, and one-shot has no footer at all. The zero value
	// is the default and says nothing.
	ReadOnly sandbox.Ceiling
	// Notes are startup warnings already phrased by their own subsystem.
	Notes []string
}

// Lines composes the banner. The order is the order an operator reads:
// what this is, what it loaded that they did not type, what it has,
// what came back, then everything abnormal — last, because it is
// closest to the prompt.
func Lines(f Facts) []string {
	out := []string{fmt.Sprintf("gem-agent %s — %s", f.Version, f.Model)}
	if len(f.Instructions) > 0 {
		out = append(out, "instructions: "+strings.Join(f.Instructions, ", "))
	}
	if line := inventory(f); line != "" {
		out = append(out, line)
	}
	if f.ResumedID != "" {
		out = append(out, fmt.Sprintf("resumed: session %s (%d messages restored)", f.ResumedID, f.Restored))
	}
	if line := SandboxLine(f.SandboxOn, f.ReadLane, f.ReadLanePrompts); line != "" {
		out = append(out, line)
	}
	if f.AutoApprove {
		out = append(out, AutoApproveLine())
	}
	out = append(out, ReadOnlyLines(f.ReadOnly, f.SandboxOn)...)
	for _, n := range f.Notes {
		out = append(out, "warning: "+n)
	}
	// The "/help for commands" hint is NOT here: the TUI has its own
	// chrome for it and only the plain REPL prints it, so a banner that
	// carried it would be wrong in one of the two modes.
	return out
}

// inventory is the one row that replaced the enumerations (ADR-0078
// §2): what came up, and the commands that expand it. The count survives
// the cut because "did my toolset come up as expected" is a question the
// operator has before typing — a server that fails to start warns, but
// one missing from the configuration warns nobody.
func inventory(f Facts) string {
	var parts, cmds []string
	if f.Servers > 0 {
		parts = append(parts, fmt.Sprintf("mcp: %d servers, %d tools", f.Servers, f.Tools))
		cmds = append(cmds, "/mcp")
	}
	if f.Skills > 0 {
		parts = append(parts, fmt.Sprintf("skills: %d", f.Skills))
		cmds = append(cmds, "/skills")
	}
	if f.Memories > 0 {
		parts = append(parts, fmt.Sprintf("memory: %d", f.Memories))
		cmds = append(cmds, "/memory")
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ") + " (" + strings.Join(cmds, " ") + ")"
}

// AutoApproveLine says the session begins running mutating tools
// unattended. A change, not a status: the TUI footer carries it
// continuously, the plain REPL has no footer, and one-shot has neither —
// which is the mode where it matters most, since the ladder answers and
// nobody is at a prompt.
//
// Exported for the same reason as SandboxLine: one-shot prints it
// without the rest of the banner, and the two modes must not disagree.
func AutoApproveLine() string {
	return "auto-approve: ON at start — /auto or shift+tab turns it off"
}

// AutoApproveOneShotLine is the same fact for `-p`, where neither /auto
// nor shift+tab exists: there is no REPL to type into and no TUI to
// press. The next command is the flag.
func AutoApproveOneShotLine() string {
	return "auto-approve: ON — this run approves its own mutating tools; drop --auto to restore the gate"
}

// ReadOnlyLines says what the session starts with, one line per setting
// and nothing for a default. The shape is `/readonly`'s: the ceiling,
// then the watcher when it is armed — an operator who has read one
// should not have to learn the other.
//
// Each earns its line for its own reason. The ceiling, because a
// session that refuses changes should say so before the operator asks
// for one. The watcher, because the footer cannot: it carries the
// ceiling in force, and an armed watcher has none yet, so without this
// the fact is invisible until the turn it fires on.
// confined is the sandbox as this machine established it. With it off,
// the ceiling still refuses the file tools and a shell call that
// declares write or operator, but a read-lane declaration is no longer
// bounded by anything, so the unqualified sentence would be false
// (independent review).
//
// It is Registry.Confined() alone, never Confined() && ReadLane(): an
// unverified read lane — the operator's own read_lane_prompts, or a
// failed startup probe — still applies the read profile AND gates the
// call, so the guarantee holds harder there, not less. The first
// version of this used the conjunction and told the most cautious
// configuration in the tool that its sandbox was off (second
// independent review).
func ReadOnlyLines(c sandbox.Ceiling, confined bool) []string {
	var out []string
	switch {
	case c.ReadOnly && !confined:
		out = append(out, "read-only: ON at start — but the sandbox is off, so a shell command declaring the read lane is bounded by nothing; /readonly off lifts the rest")
	case c.ReadOnly:
		out = append(out, "read-only: ON at start — this session changes nothing outside its scratch; /readonly off lifts it")
	}
	if c.Auto {
		out = append(out, "auto read-only: ON — it turns read-only on by itself when you ask for a read-only session")
	}
	return out
}

// ReadOnlyOneShotLine is the same fact for `-p`, where there is no
// footer to carry it and no /readonly to type. The next command is the
// flag — the same shape as the auto-approve pair above. It names
// --writable, not "drop --read-only": the ceiling may have come from
// config, where there is no flag to drop (independent review).
func ReadOnlyOneShotLine(confined bool) string {
	if !confined {
		return "read-only: ON — but the sandbox is off, so a shell command declaring the read lane is bounded by nothing; pass --writable to allow the rest"
	}
	return "read-only: ON — this run changes nothing outside its scratch; pass --writable to allow changes"
}

// SandboxLine returns the sandbox line only when the sandbox is not in
// its ordinary state. Enabled with a verified read lane is the normal
// case and says nothing (ADR-0078 §3); the three exceptions each change
// what a shell command will do, so each still prints.
//
// Exported because one-shot mode prints it without the rest of the
// banner: the same sentence in both, or the two modes disagree about
// what the sandbox is doing.
func SandboxLine(on, readLane, readLanePrompts bool) string {
	switch {
	case !on:
		// The confinement fact and the state to restore, and nothing
		// about approvals: the clause that used to be here said every
		// command asks, which is false in one-shot, where a gated call
		// is denied rather than asked. (Interactively it was true even
		// under --auto — an unconfined shell is OperatorOnly and the
		// gate prompts.) "Gated" is the word true in both modes, and the
		// approval regime has its own lines (pre-release review).
		return "sandbox: DISABLED — shell commands run unconfined; restart with the sandbox enabled to confine them"
	case readLanePrompts:
		return "sandbox: enabled (read_lane_prompts: read-lane commands are gated too — unset it in config.toml to run them unasked)"
	case !readLane:
		return "sandbox: enabled (read lane unverified on this machine — every shell_exec is gated; run `gem-agent` again to re-probe)"
	}
	return ""
}

// State is the same four-way answer in the settings panel's vocabulary:
// a row, not a sentence. Both surfaces read the same booleans, so they
// cannot tell the operator different stories about the same session —
// which they did while the row took two booleans and the runtime had
// four states (pre-release review).
func State(on, readLane, readLanePrompts bool) string {
	switch {
	case !on:
		return "DISABLED — shell commands run unconfined"
	case readLanePrompts:
		return "enabled (read_lane_prompts: read-lane commands are gated too)"
	case !readLane:
		return "enabled (read lane unverified — every shell_exec is gated)"
	}
	return "enabled"
}

// Sample is a banner with every optional line present, for the operator
// text read-through. It is not test data: it is what the document has
// to show, because a document that omits the first screen is the one
// the four explanatory-banner releases were shipped past.
func Sample() Facts {
	return Facts{
		// Not a real version: a hardcoded one goes stale on the next
		// release and the document then asserts it (pre-release review).
		Version: "vX.Y.Z", Model: "gemini-3.8-flash",
		Instructions: []string{"~/.config/gem-agent/AGENTS.md", "../CLAUDE.md", "AGENTS.md"},
		Servers:      24, Tools: 249, Skills: 5, Memories: 3,
		ResumedID: "2acb328c", Restored: 42,
		SandboxOn: true, ReadLane: false,
		AutoApprove: true,
		// The real note, not a shortened invention: a reader judging the
		// banner's last row was judging a line gem-agent never prints,
		// and the real one wraps to three rows and carries a command
		// (pre-release review).
		Notes: []string{"project policy ignored for read_file, shell_exec: a project file may not remove approvals unless the project is trusted. To allow it, add the project path to [approval].trusted_projects in your own config"},
	}
}

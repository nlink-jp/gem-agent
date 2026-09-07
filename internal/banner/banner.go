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
		// A change, not a status: the session begins running mutating
		// tools unattended. The TUI footer carries it continuously, but
		// the plain REPL has no footer, and it is the only
		// approval-regime fact with no other startup surface.
		out = append(out, "auto-approve: ON at start — /auto or shift+tab turns it off")
	}
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
		// The clause about auto-approve is more load-bearing
		// interactively, not less: it is the only place that says the
		// prompts will not be silenced.
		return "sandbox: DISABLED — shell commands run unconfined and every one asks for your approval (auto-approve does not skip this)"
	case readLanePrompts:
		return "sandbox: enabled (read_lane_prompts: read-lane commands ask too)"
	case !readLane:
		return "sandbox: enabled (read lane unverified on this machine — every shell_exec asks)"
	}
	return ""
}

// Sample is a banner with every optional line present, for the operator
// text read-through. It is not test data: it is what the document has
// to show, because a document that omits the first screen is the one
// the four explanatory-banner releases were shipped past.
func Sample() Facts {
	return Facts{
		Version: "v0.72.0", Model: "gemini-3.8-flash",
		Instructions: []string{"~/.config/gem-agent/AGENTS.md", "../CLAUDE.md", "AGENTS.md"},
		Servers:      24, Tools: 249, Skills: 5, Memories: 3,
		ResumedID: "2acb328c", Restored: 42,
		SandboxOn: true, ReadLane: false,
		AutoApprove: true,
		Notes:       []string{"project .gem-agent.toml entry ignored: not a trusted project"},
	}
}

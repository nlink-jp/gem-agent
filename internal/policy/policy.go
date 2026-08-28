// Package policy resolves the per-tool approval policy of ADR-0008: for
// each tool, whether the human gate always applies, never applies, or is
// left to the default behaviour.
//
// It is pure. Loading files belongs to internal/config; this package
// takes the two scopes' raw entries and answers questions about them, so
// the direction rule — a project may tighten freely, and may loosen only
// where the operator has trusted it — is decided in one testable place.
package policy

import (
	"fmt"
	"sort"
	"strings"
)

// Decision is the effective approval policy for one tool.
type Decision int

const (
	// Default keeps the built-in behaviour: mutating tools ask, and
	// auto-approve (ADR-0004) may run them through its ladder.
	Default Decision = iota
	// AlwaysAsk gates the tool in every mode — an operator-set floor,
	// the counterpart of the rule tier's Block verdict.
	AlwaysAsk
	// NeverAsk runs the tool without the gate in every mode. It does not
	// lift the rule tier's Block floor; see ADR-0008 §2.
	NeverAsk
)

func (d Decision) String() string {
	switch d {
	case AlwaysAsk:
		return "always"
	case NeverAsk:
		return "never"
	default:
		return "default"
	}
}

// Values accepted in a config file.
const (
	valueAlways = "always"
	valueNever  = "never"
)

// Parse converts a config value to a Decision.
func Parse(v string) (Decision, error) {
	switch strings.TrimSpace(v) {
	case valueAlways:
		return AlwaysAsk, nil
	case valueNever:
		return NeverAsk, nil
	default:
		return Default, fmt.Errorf("approval policy must be %q or %q (got %q)", valueAlways, valueNever, v)
	}
}

// rule is one parsed pattern.
type rule struct {
	pattern string // exact name, or a prefix ending in "*"
	prefix  string // set when the pattern is a wildcard
	d       Decision
}

// Policy answers the approval question for a tool name.
type Policy struct {
	rules []rule // most specific first
	// commands is the per-command table of ADR-0045 §4, keyed by
	// CommandKey. It exists only in the project scope: `make build`
	// being settled in one repository says nothing about another, and a
	// global entry would auto-run inside the next hostile clone.
	commands map[string]Decision
}

// Note is an operator-facing message produced while building a Policy —
// most importantly, a project entry that was ignored because it would
// have weakened the gate in an untrusted project. Silently dropping it
// would leave the operator believing a policy is in force when it is not.
type Note string

// Build combines the two scopes. globalTools and projectTools map a tool
// pattern to a config value; commands maps a CommandKey to one (ADR-0045
// §4); trusted says whether the project directory is listed in the
// operator's global trusted_projects.
//
// A project entry that tightens (always) is honoured unconditionally. A
// project entry that loosens (never) is honoured only when trusted, and
// otherwise dropped with a Note naming it.
//
// commands carries no trust condition because it does not come from the
// project directory at all: /learn writes it into the machine-owned
// policy file after the operator confirmed each rule, which is the same
// standing as a policy set from /settings.
func Build(globalTools, projectTools map[string]string, commands map[string]string, trusted bool) (Policy, []Note, error) {
	var (
		p       Policy
		notes   []Note
		ignored []string
	)

	// buildScope parses and sorts one scope's rules: exact matches
	// first, then longer wildcards before shorter ones, so For can
	// return the first match it finds.
	buildScope := func(tools map[string]string, label string, project bool) ([]rule, error) {
		var rules []rule
		for _, pattern := range sortedKeys(tools) {
			d, err := Parse(tools[pattern])
			if err != nil {
				return nil, fmt.Errorf("%s %q: %w", label, pattern, err)
			}
			if err := ValidateEntry(label, pattern); err != nil {
				return nil, err
			}
			if project && d == NeverAsk && !trusted {
				// The one rule that matters: a directory whose contents
				// the operator may not have written cannot switch the
				// gate off.
				ignored = append(ignored, pattern)
				continue
			}
			r := rule{pattern: pattern, d: d}
			if strings.HasSuffix(pattern, "*") {
				r.prefix = strings.TrimSuffix(pattern, "*")
			}
			rules = append(rules, r)
		}
		sort.SliceStable(rules, func(i, j int) bool {
			a, b := rules[i], rules[j]
			if (a.prefix == "") != (b.prefix == "") {
				return a.prefix == "" // exact beats wildcard
			}
			return len(a.pattern) > len(b.pattern)
		})
		return rules, nil
	}

	globalRules, err := buildScope(globalTools, "[approval.tools]", false)
	if err != nil {
		return Policy{}, nil, err
	}
	projectRules, err := buildScope(projectTools, "project [approval.tools]", true)
	if err != nil {
		return Policy{}, nil, err
	}
	// Scope before specificity (ADR-0021 §6): the nearest scope wins,
	// whatever the pattern shapes. A single cross-scope list sorted by
	// specificity let a global exact rule beat a project wildcard
	// TIGHTEN — breaking ADR-0008's "a project may tighten freely".
	p.rules = append(projectRules, globalRules...)

	for _, key := range sortedKeys(commands) {
		d, err := Parse(commands[key])
		if err != nil {
			return Policy{}, nil, fmt.Errorf("[commands] %q: %w", key, err)
		}
		// A stored key that today's derivation would not produce can
		// never match a live call, so accepting it would mean an entry
		// the operator sees in /settings and that silently does
		// nothing. Rejecting loudly is the ADR-0021 §6 discipline.
		if k, ok := CommandKey(key); !ok || k != key {
			return Policy{}, nil, fmt.Errorf("[commands] %q is not a command key: entries are a command name, optionally with a subcommand (for example \"go test\")", key)
		}
		if p.commands == nil {
			p.commands = map[string]Decision{}
		}
		p.commands[key] = d
	}

	if len(ignored) > 0 {
		notes = append(notes, Note(fmt.Sprintf(
			"project policy ignored for %s: a project file may not remove approvals unless the project is trusted. To allow it, add the project path to [approval].trusted_projects in your own config",
			strings.Join(ignored, ", "))))
	}
	return p, notes, nil
}

// ValidateEntry rejects patterns that cannot mean anything useful, and
// the one that means too much: a bare "*" would disarm every gate at
// once, which must not be reachable by a one-character entry. label
// names the entry's source in the error — a config table, or the
// --allow flag (ADR-0053), which carries the same vocabulary.
func ValidateEntry(label, pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return fmt.Errorf("%s has an empty tool name", label)
	case pattern == "*":
		return fmt.Errorf(`%s "*" is not allowed: name the tools, or a prefix like "mcp__server__*" — switching off every gate at once is not a policy entry`, label)
	case strings.Contains(strings.TrimSuffix(pattern, "*"), "*"):
		return fmt.Errorf("%s %q: %q is only allowed as a trailing wildcard", label, pattern, "*")
	}
	return nil
}

// For returns the policy for a tool name.
func (p Policy) For(tool string) Decision {
	for _, r := range p.rules {
		if r.prefix == "" {
			if r.pattern == tool {
				return r.d
			}
			continue
		}
		if strings.HasPrefix(tool, r.prefix) {
			return r.d
		}
	}
	return Default
}

// ForCall answers for one concrete call: the tool's policy, refined by
// the per-command policy when the call is a shell command whose key is
// known and listed (ADR-0045 §4).
//
// Two rules combine them, in this order:
//
//  1. AlwaysAsk from either table wins. It is a floor in both
//     directions: an operator who pinned `shell_exec = "always"` said
//     every shell call is theirs to see, and a learned rule — which
//     only ever means "I approved this repeatedly" — must not take that
//     back; while a learned `"always"` tightens a blanket
//     `shell_exec = "never"`, and tightening is always free (ADR-0008).
//  2. Otherwise the command entry answers, being the more specific
//     statement about this call. An entry exists only because the
//     operator confirmed it, so this is their decision either way.
func (p Policy) ForCall(tool, command string) Decision {
	d := p.For(tool)
	if len(p.commands) == 0 || command == "" {
		return d
	}
	key, ok := CommandKey(command)
	if !ok {
		return d
	}
	c, ok := p.commands[key]
	if !ok {
		return d
	}
	if d == AlwaysAsk || c == AlwaysAsk {
		return AlwaysAsk
	}
	return c
}

// Commands returns the per-command table, keyed by CommandKey.
func (p Policy) Commands() map[string]Decision {
	out := make(map[string]Decision, len(p.commands))
	for k, v := range p.commands {
		out[k] = v
	}
	return out
}

// Configured reports whether any policy is in force (banner display).
func (p Policy) Configured() bool { return len(p.rules) > 0 || len(p.commands) > 0 }

// Describe renders the rules in match order, for /tools and the banner.
func (p Policy) Describe() []string {
	out := make([]string, 0, len(p.rules))
	for _, r := range p.rules {
		out = append(out, r.pattern+" = "+r.d.String())
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

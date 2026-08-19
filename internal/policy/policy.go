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
}

// Note is an operator-facing message produced while building a Policy —
// most importantly, a project entry that was ignored because it would
// have weakened the gate in an untrusted project. Silently dropping it
// would leave the operator believing a policy is in force when it is not.
type Note string

// Build combines the two scopes. globalTools and projectTools map a tool
// pattern to a config value; trusted says whether the project directory
// is listed in the operator's global trusted_projects.
//
// A project entry that tightens (always) is honoured unconditionally. A
// project entry that loosens (never) is honoured only when trusted, and
// otherwise dropped with a Note naming it.
func Build(globalTools, projectTools map[string]string, trusted bool) (Policy, []Note, error) {
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
			if err := validPattern(pattern); err != nil {
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

	if len(ignored) > 0 {
		notes = append(notes, Note(fmt.Sprintf(
			"project policy ignored for %s: a project file may not remove approvals unless the project is trusted. To allow it, add the project path to [approval].trusted_projects in your own config",
			strings.Join(ignored, ", "))))
	}
	return p, notes, nil
}

// validPattern rejects patterns that cannot mean anything useful, and
// the one that means too much: a bare "*" would disarm every gate at
// once, which must not be reachable by a one-character entry.
func validPattern(pattern string) error {
	switch {
	case strings.TrimSpace(pattern) == "":
		return fmt.Errorf("[approval.tools] has an empty tool name")
	case pattern == "*":
		return fmt.Errorf(`[approval.tools] "*" is not allowed: name the tools, or a prefix like "mcp__server__*" — switching off every gate at once is not a policy entry`)
	case strings.Contains(strings.TrimSuffix(pattern, "*"), "*"):
		return fmt.Errorf("[approval.tools] %q: %q is only allowed as a trailing wildcard", pattern, "*")
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

// Configured reports whether any policy is in force (banner display).
func (p Policy) Configured() bool { return len(p.rules) > 0 }

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

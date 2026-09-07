// Package mcpfilter answers one question, in one place: is this MCP
// server — or this function of it — excluded from the session's declared
// tools (ADR-0077)?
//
// The whole mechanism is a filter on names applied at two points: the
// declarations the model is given, and the dispatch of a call. This
// package is the predicate. It is pure: loading files belongs to
// internal/config, and the two call sites live in cmd and the executor.
//
// MCP declares no capability to filter on — a Tool is a name, a title, a
// description and schemas, plus optional annotations a client must treat
// as untrusted — so the name is the only expressible form the operator's
// intent has. That bounds what this can be worth: it removes the name,
// not the capability, and a function that returns under a different name
// is a function this package cannot notice.
package mcpfilter

import (
	"fmt"
	"sort"
	"strings"
)

// Separator divides a server from one of its functions in an entry.
const Separator = "/"

// Scope labels where an entry came from, for the unmatched report.
type Scope string

// The three scopes, nearest last for everything but composition (§2:
// per server, the nearest scope decides whole; the project may only add).
const (
	FromConfig Scope = "config.toml"
	FromPolicy Scope = "policy.toml"
	// The file, not the word "project": two of the three scopes told the
	// operator which file to open and the third did not. The name is
	// config.ProjectFileName's, spelled here because a pure predicate
	// does not import the loader.
	FromProject Scope = ".gem-agent.toml"
)

// Entry is one parsed exclusion: a server, or one function of it.
type Entry struct {
	Server string
	Func   string // empty means the whole server
	Scope  Scope
}

// String renders the entry as the operator wrote it.
func (e Entry) String() string {
	if e.Func == "" {
		return e.Server
	}
	return e.Server + Separator + e.Func
}

// Parse reads one entry. Syntax errors are errors, not notes: an
// exclusion that does not do what it says is worse than no exclusion,
// and this file is strict-decoded like every other setting.
func Parse(raw string, scope Scope) (Entry, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Entry{}, fmt.Errorf("empty entry")
	}
	if strings.Contains(s, "*") {
		// The two levels supply the grouping a wildcard was faking
		// (ADR-0077 §2): naming a server already means all of it.
		return Entry{}, fmt.Errorf("%q: no patterns — name a server, or %q", s, "server"+Separator+"function")
	}
	parts := strings.Split(s, Separator)
	switch len(parts) {
	case 1:
		return Entry{Server: parts[0], Scope: scope}, nil
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return Entry{}, fmt.Errorf("%q: both sides of %q must be named", s, Separator)
		}
		return Entry{Server: parts[0], Func: parts[1], Scope: scope}, nil
	default:
		return Entry{}, fmt.Errorf("%q: one %q at most (server%sfunction)", s, Separator, Separator)
	}
}

// Split is Parse for an entry already known to be well formed — the
// panel's rows carry entries this package produced. It never errors:
// anything without a separator is a whole server.
func Split(entry string) (server, fn string) {
	if i := strings.Index(entry, Separator); i >= 0 {
		return entry[:i], entry[i+len(Separator):]
	}
	return entry, ""
}

// bucket is one server's exclusions from one scope.
type bucket struct {
	whole bool
	fns   map[string]bool
}

func (b *bucket) add(e Entry) {
	if e.Func == "" {
		b.whole = true
		return
	}
	if b.fns == nil {
		b.fns = map[string]bool{}
	}
	b.fns[e.Func] = true
}

// Filter is the composed answer for a session.
type Filter struct {
	byServer map[string]*bucket
	entries  []Entry // every entry that survived composition, for Unmatched
	// shadowed are the entries a nearer scope overrode. They are kept
	// because "a name that matches nothing is reported, not ignored"
	// (ADR-0077 §2) covers a line that does nothing because another file
	// speaks about the same server, not only a misspelled one — and
	// `decided` widened the silent set to every server ever touched in
	// the panel (pre-release re-review).
	shadowed []Entry
}

// PolicyScope is the machine-owned file's word (ADR-0077 §2). Decided
// names the servers it has an opinion about, and it is carried
// separately from Entries on purpose: "this server has nothing
// excluded" is an opinion that shadows config.toml, and inferring the
// opinion from the presence of an entry made that state unrepresentable
// — a server excluded in config.toml could then never be turned back on
// from the panel (pre-release review).
type PolicyScope struct {
	Entries []string
	Decided []string
}

// Build composes the three scopes into one filter (ADR-0077 §2).
//
// Per server, the nearest scope decides whole: if policy.toml says
// anything about a server, config.toml's word about that server is not
// consulted. There is no set arithmetic across those two — what the
// operator last said in the panel replaces the file they wrote last
// month, for that server and no other.
//
// The project file may only add. Union is the only composition it gets,
// so ADR-0008 §4's direction rule holds by construction rather than by a
// trust check.
func Build(configEntries []string, policy PolicyScope, projectEntries []string) (Filter, error) {
	parseAll := func(raw []string, scope Scope) (map[string]*bucket, []Entry, error) {
		byServer := map[string]*bucket{}
		var entries []Entry
		for _, r := range raw {
			e, err := Parse(r, scope)
			if err != nil {
				return nil, nil, fmt.Errorf("[mcp] exclude (%s): %w", scope, err)
			}
			b := byServer[e.Server]
			if b == nil {
				b = &bucket{}
				byServer[e.Server] = b
			}
			b.add(e)
			entries = append(entries, e)
		}
		return byServer, entries, nil
	}

	cfgB, cfgE, err := parseAll(configEntries, FromConfig)
	if err != nil {
		return Filter{}, err
	}
	polB, polE, err := parseAll(policy.Entries, FromPolicy)
	if err != nil {
		return Filter{}, err
	}
	// A decided server shadows config.toml even with no entries.
	shadowed := make(map[string]bool, len(polB)+len(policy.Decided))
	for server := range polB {
		shadowed[server] = true
	}
	for _, server := range policy.Decided {
		shadowed[strings.TrimSpace(server)] = true
	}
	_, prjE, err := parseAll(projectEntries, FromProject)
	if err != nil {
		return Filter{}, err
	}

	f := Filter{byServer: map[string]*bucket{}}
	keep := func(e Entry) { f.entries = append(f.entries, e) }

	// config, except where policy speaks about the same server
	for server, b := range cfgB {
		if shadowed[server] {
			continue
		}
		f.byServer[server] = b
	}
	for _, e := range cfgE {
		if shadowed[e.Server] {
			f.shadowed = append(f.shadowed, e)
			continue
		}
		keep(e)
	}
	for server, b := range polB {
		f.byServer[server] = b
	}
	f.entries = append(f.entries, polE...)

	// the project adds
	for _, e := range prjE {
		b := f.byServer[e.Server]
		if b == nil {
			b = &bucket{}
			f.byServer[e.Server] = b
		}
		b.add(e)
	}
	f.entries = append(f.entries, prjE...)

	return f, nil
}

// Server reports whether the whole server is excluded. A server excluded
// at this level is never started: no process, no credentials touched,
// nothing of it in the declarations.
func (f Filter) Server(name string) bool {
	b := f.byServer[name]
	return b != nil && b.whole
}

// Func reports whether one function of a server is excluded — either by
// its own entry or because the whole server is.
func (f Filter) Func(server, fn string) bool {
	b := f.byServer[server]
	if b == nil {
		return false
	}
	return b.whole || b.fns[fn]
}

// FunctionEntries returns one server's function-level entries, ignoring
// any whole-server exclusion. It answers the question "turn this server
// on — what stays off?": collapsing to the whole-server entry, as For
// does, would discard the functions a lower scope excluded by hand.
func (f Filter) FunctionEntries(server string) []string {
	b := f.byServer[server]
	if b == nil {
		return nil
	}
	out := make([]string, 0, len(b.fns))
	for fn := range b.fns {
		out = append(out, server+Separator+fn)
	}
	sort.Strings(out)
	return out
}

// Knows reports whether this filter has any opinion about a server —
// used to tell "the panel is overriding a lower scope" from "the panel
// is the only thing that ever excluded this", which decides whether
// turning a server on should record an opinion or withdraw one.
func (f Filter) Knows(server string) bool { return f.byServer[server] != nil }

// For returns one server's exclusions as full entries — "obsidian" when
// the whole server is excluded, otherwise "obsidian/patch_vault_file"
// for each excluded function, sorted. The settings panel writes a
// server's whole state rather than a delta (ADR-0077 §2), and this is
// the state it starts from.
func (f Filter) For(server string) []string {
	b := f.byServer[server]
	if b == nil {
		return nil
	}
	if b.whole {
		return []string{server}
	}
	out := make([]string, 0, len(b.fns))
	for fn := range b.fns {
		out = append(out, server+Separator+fn)
	}
	sort.Strings(out)
	return out
}

// Empty reports whether nothing is excluded, which is the default and
// keeps `.mcp.json` meaning exactly what it means today.
func (f Filter) Empty() bool { return len(f.byServer) == 0 }

// Unmatched returns the entries that named nothing.
//
// configured is every server name in the merged .mcp.json — an excluded
// server is configured but never started, and naming it is correct, not
// stale. listed maps the servers that actually answered tools/list to
// their function names; a server that is configured but absent from
// listed was excluded or unreachable, and a function entry under it says
// nothing about whether the operator's line is right. complete says
// whether every server list was read this run: when it is false, a name
// missing from configured proves nothing.
//
// A name that matches nothing is reported, not ignored (ADR-0037's
// rule): a server or function renamed upstream must not leave a line
// that quietly does nothing.
func (f Filter) Unmatched(configured map[string]bool, listed map[string][]string, complete bool) []string {
	var out []string
	for _, e := range f.entries {
		if !configured[e.Server] {
			// Only when every server list was actually read. A project
			// whose .mcp.json was not loaded (untrusted, changed pin),
			// a different project, or a file that failed to parse all
			// leave servers out of `configured` — and telling the
			// operator to delete a correct line is worse than silence.
			if complete {
				out = append(out, fmt.Sprintf("%s (%s): no such MCP server — remove it or correct the name", e, e.Scope))
			}
			continue
		}
		if e.Func == "" {
			continue // the server exists; the entry did its work
		}
		fns, ok := listed[e.Server]
		if !ok {
			continue // not started this run — nothing to check the name against
		}
		found := false
		for _, fn := range fns {
			if fn == e.Func {
				found = true
				break
			}
		}
		if !found {
			out = append(out, fmt.Sprintf("%s (%s): server %q offers no such function — remove it or correct the name", e, e.Scope, e.Server))
		}
	}
	for _, e := range f.shadowed {
		out = append(out, fmt.Sprintf("%s (%s): not in force — %s decides this server; remove it, or turn that server's functions off in /settings",
			e, e.Scope, FromPolicy))
	}
	sort.Strings(out)
	return dedupe(out)
}

func dedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	out := in[:1]
	for _, s := range in[1:] {
		if s != out[len(out)-1] {
			out = append(out, s)
		}
	}
	return out
}

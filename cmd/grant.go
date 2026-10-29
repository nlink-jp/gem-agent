package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// dirGranter is ADR-0099 §12: the one grant path the operator's
// /add-dir and /read-dir (§11) and the model's approved
// request_directory (§4) share. A grant changes the shell runner, the
// registry's roots, the watch and the system prompt while a call the
// floor abandoned (ADR-0065) may still be running and /clear rebuilds
// the runner on its own; mu orders them, and epoch — advanced by /clear
// under mu — drops a late grant from a session that has moved on.
type dirGranter struct {
	mu    sync.Mutex
	epoch uint64

	registry        *tools.Registry
	shellExec       *liveExec
	sandboxOn       bool
	projectDir      string
	workDir         func() string
	denyExec        []string
	caches          map[string]string
	readLanePrompts bool
	parents         func() []string
	// base is RefuseRoot's context without the roots and the work
	// directory, which are read at each grant.
	base  sandbox.RootContext
	watch *addedWatch
	// log writes to the transcript in use (it changes at /clear).
	log func(kind string, data any) error
	// sources remembers where each added root came from, for the
	// records and /dirs.
	sources map[string]string
	// promptStale asks for the system prompt's rebuild at the next turn
	// boundary: SetSystem is a between-turns operation (§12 step 7).
	promptStale atomic.Bool
	msgs        *uitext.Messages
	// build is buildExecFn; a test replaces it to act while the probes
	// run.
	build func(sandboxOn bool, projectDir, workDir string, denyExec []string, caches map[string]string, persistentParents []string, added []sandbox.AddedRoot) (tools.LaneExecFunc, sandbox.Enforcement, []string, error)
}

func (g *dirGranter) text() *uitext.Messages {
	if g.msgs == nil {
		return uitext.For(uitext.EN)
	}
	return g.msgs
}

func (g *dirGranter) access(write bool) string {
	if write {
		return g.text().AccessWrite
	}
	return g.text().AccessRead
}

func (g *dirGranter) source(s string) string {
	m := g.text()
	switch s {
	case "config":
		return m.SourceConfig
	case "slash":
		return m.SourceSlash
	case "request":
		return m.SourceRequest
	}
	return m.SourceFlag
}

// errDropped is a grant whose call was cancelled or whose session moved
// on before it could commit.
var errDropped = errors.New("dropped: the session moved on before the grant could apply")

// currentEpoch is read when a request's Run begins.
func (g *dirGranter) currentEpoch() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.epoch
}

// beginClear takes the grant lock for /clear's whole swap — the new
// transcript, the work directory, the record and the runner's rebuild —
// and advances the epoch, so a grant from the old session is dropped
// and none interleaves with the swap. endClear releases it.
func (g *dirGranter) beginClear() {
	g.mu.Lock()
	g.epoch++
}

func (g *dirGranter) endClear() { g.mu.Unlock() }

// rotate is beginClear and endClear around fn, for a caller whose swap
// is one call.
func (g *dirGranter) rotate(fn func()) {
	g.beginClear()
	defer g.endClear()
	fn()
}

// resolveTyped turns what was typed (or requested) into an absolute,
// cleaned path: a leading ~ is the home, a relative path is relative to
// the project (§11's grammar).
func (g *dirGranter) resolveTyped(path string) string {
	p := strings.TrimSpace(path)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(g.projectDir, p)
	}
	return filepath.Clean(p)
}

// grant adds path as a root at the given level. It returns the
// operator's note, or an error that names why nothing changed. A
// covered path is a note, not an error. epoch is the session the grant
// belongs to: the request's at the start of its Run, the current one
// for a slash command.
func (g *dirGranter) grant(ctx context.Context, path string, write bool, source string, epoch uint64, extra map[string]any) (string, error) {
	m := g.text()
	typed := g.resolveTyped(path)
	// Resolving keeps each typed component that is not a link as typed,
	// so a difference here is a link and nothing else — not the case or
	// the Unicode form, which the volume folds (review of v0.94.0, #2).
	resolved, err := sandbox.ResolveWriteDir(typed)
	if err != nil {
		return "", fmt.Errorf(m.GrantNotFoundFmt, typed)
	}
	// The kernel's name for it (case and stored form): every check below
	// judges that, never the typed spelling.
	real, err := sandbox.CanonicalDir(resolved)
	if err != nil {
		return "", fmt.Errorf(m.GrantCannotOpenFmt, resolved, err)
	}
	// A path that resolves through a link names one place and grants
	// another: the model can plant the link and suggest the command.
	// The real path is named, and the grant happens when that path is
	// given (§11; for a request, asked again).
	if resolved != typed {
		if source == "request" {
			return "", fmt.Errorf("%s resolves to %s — request the real path to add it", typed, real)
		}
		return "", fmt.Errorf(m.GrantLinkFmt, typed, real, map[bool]string{true: "/add-dir", false: "/read-dir"}[write], real)
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if ctx.Err() != nil || epoch != g.epoch {
		return "", errDropped
	}
	current := g.registry.AddedDirs()
	rc := g.base
	rc.ProjectDir, rc.WorkDir, rc.Roots = g.projectDir, realOrSelf(g.workDir()), current
	judged, err := sandbox.RefuseRoot(real, write, rc)
	if err != nil {
		var cov *sandbox.CoveredError
		if errors.As(err, &cov) {
			return fmt.Sprintf(m.GrantCoveredFmt, cov.Path, cov.By.Dir, g.access(cov.By.Write)), nil
		}
		return "", err
	}
	// What is installed is what was judged, byte for byte: both names
	// come from the kernel, so any difference is the path changing in
	// between (review of v0.94.0, #3).
	if judged != real {
		return "", fmt.Errorf(m.GrantChangedFmt, real, judged)
	}

	// Open first and read the real path back from the descriptor.
	root, err := os.OpenRoot(real)
	if err != nil {
		return "", fmt.Errorf(m.GrantCannotOpenFmt, real, err)
	}
	canonical, err := descriptorPathOf(root)
	if err != nil {
		_ = root.Close()
		return "", fmt.Errorf(m.GrantCannotOpenFmt, real, err)
	}
	if canonical != real {
		_ = root.Close()
		return "", fmt.Errorf(m.GrantChangedFmt, real, canonical)
	}

	absorbed := sandbox.Absorbed(canonical, write, current)
	absorbedDirs := make([]string, 0, len(absorbed))
	for _, a := range absorbed {
		absorbedDirs = append(absorbedDirs, a.Dir)
	}
	next := []sandbox.AddedRoot{}
	for _, a := range current {
		if !containsFold(absorbedDirs, a.Dir) {
			next = append(next, a)
		}
	}
	granted := sandbox.AddedRoot{Dir: canonical, Write: write}
	next = append(next, granted)

	// Build and verify against the current project and work directory;
	// the grant must not change what the runtime established about the
	// sandbox — never the unconfined fallback, never the read lane
	// switched on or off. An unconfined session has no cage to verify:
	// its runner stays, every command asks, and the grant reaches the
	// file tools only (§12 step 4).
	cur := g.registry.Enforcement()
	var fn tools.LaneExecFunc
	enf := cur
	if cur.Confined {
		build := g.build
		if build == nil {
			build = buildExecFn
		}
		fn, enf, _, err = build(g.sandboxOn, g.projectDir, g.workDir(), g.denyExec, g.caches, g.parents(), next)
		if err != nil {
			_ = root.Close()
			return "", fmt.Errorf(m.GrantNotAddedFmt, canonical, err)
		}
		if g.readLanePrompts {
			enf.ReadLane = false
		}
		if enf != cur {
			_ = root.Close()
			return "", fmt.Errorf(m.GrantStateFmt, canonical)
		}
	}
	// The probes take time; a call cancelled meanwhile, or a session
	// that moved on, does not commit (review of step 2, #3).
	if ctx.Err() != nil || epoch != g.epoch {
		_ = root.Close()
		return "", errDropped
	}

	var watchNotes []string
	if write {
		watchNotes = g.watch.add(canonical, absorbedDirs, g.log)
	}
	if cur.Confined {
		g.shellExec.set(fn)
		g.registry.SetLaneExec(g.shellExec.run, enf)
	}
	g.registry.GrantAddedDir(granted, root, absorbedDirs)
	for _, d := range absorbedDirs {
		delete(g.sources, d)
	}
	g.sources[canonical] = source
	g.promptStale.Store(true)

	rec := g.record()
	rec["granted"] = map[string]string{"dir": canonical, "access": granted.Access(), "source": source}
	if len(absorbedDirs) > 0 {
		rec["absorbed"] = absorbedDirs
	}
	for k, v := range extra {
		rec[k] = v
	}
	if g.log != nil {
		_ = g.log("added_directories", rec)
	}

	note := fmt.Sprintf(m.GrantAddedFmt, canonical, g.access(write))
	for _, d := range absorbedDirs {
		if d != canonical {
			note += fmt.Sprintf(m.GrantAbsorbedFmt, d)
		}
	}
	if !enf.Confined {
		note += m.GrantUnconfined
	}
	if source == "slash" {
		note += fmt.Sprintf(m.GrantThisProcessFmt, map[bool]string{true: "add-dir", false: "read-dir"}[write])
	}
	// An absorbed root's unreported changes, and a cut walk, ride the
	// note: it is what reaches the operator in the TUI, where a stray
	// stderr line is painted over (review of v0.94.0, #4).
	for _, n := range watchNotes {
		note += "\n" + n
	}
	return note, nil
}

// record is the added_directories record of the roots in force, each
// with its level and source.
func (g *dirGranter) record() map[string]any {
	roots := []map[string]string{}
	for _, a := range g.registry.AddedDirs() {
		src := g.sources[a.Dir]
		if src == "" {
			src = "flag"
		}
		roots = append(roots, map[string]string{"dir": a.Dir, "access": a.Access(), "source": src})
	}
	return map[string]any{"roots": roots}
}

// listing is /dirs: the roots in force with their level and source.
func (g *dirGranter) listing() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	m := g.text()
	var b strings.Builder
	fmt.Fprintf(&b, m.DirsProjectFmt+"\n", g.projectDir, m.AccessWrite)
	if wd := g.workDir(); wd != "" {
		fmt.Fprintf(&b, m.DirsWorkFmt+"\n", wd, m.AccessWrite)
	}
	added := g.registry.AddedDirs()
	if len(added) == 0 {
		b.WriteString(m.DirsNone + "\n")
		return b.String()
	}
	for _, a := range added {
		fmt.Fprintf(&b, m.DirsAddedFmt+"\n", a.Dir, g.access(a.Write), g.source(g.sources[a.Dir]))
	}
	return b.String()
}

func descriptorPathOf(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	return sandbox.DescriptorPath(f)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// requestDirectoryName is the model's entrance (ADR-0099 §4).
const requestDirectoryName = "request_directory"

// registerRequestDirectory adds the model's request. It is registered
// only where a human answers — never under -p — and its verdict is
// operator-only in every mode (risk.Classify), so the model tier, the
// session allowlist and a policy never answer it. The gate's answer is
// approve or deny; a directory wanted read-only is denied and added
// with /read-dir.
func registerRequestDirectory(registry *tools.Registry, g *dirGranter) error {
	return registry.Register(&tools.Tool{
		Name: requestDirectoryName,
		Description: "Ask the user to add a directory outside the project to this session, when their task needs files there. " +
			"The user always decides; nothing is added until they approve. access \"write\": the file tools and write-lane shell commands may change it like the project; \"read\": read only. " +
			"reason: one sentence the user reads — why the task needs it. Give the path exactly; a path that resolves through a symlink is refused with its real path, which you may ask for instead. " +
			"Instruction files there (AGENTS.md, CLAUDE.md) are not loaded. If the user has already named the directory, suggest /add-dir or /read-dir rather than asking.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "absolute path of the directory (~ is the home)"},
				"access": map[string]any{"type": "string", "enum": []string{"read", "write"}},
				"reason": map[string]any{"type": "string", "description": "why the task needs it, for the user"},
			},
			"required": []string{"path", "access", "reason"},
		},
		Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			epoch := g.currentEpoch()
			path, _ := args["path"].(string)
			access, _ := args["access"].(string)
			reason, _ := args["reason"].(string)
			if access != "read" && access != "write" {
				return "", fmt.Errorf("access must be \"read\" or \"write\"")
			}
			note, err := g.grant(ctx, path, access == "write", "request", epoch, map[string]any{"reason": reason, "answer": "approved"})
			if err != nil {
				return "", fmt.Errorf("not added: %w", err)
			}
			return note + " (the session's list of roots is updated from the next turn)", nil
		},
	})
}

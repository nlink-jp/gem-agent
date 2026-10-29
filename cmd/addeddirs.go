package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// addedDir is one added root (ADR-0099) with where the operator gave it:
// "config" ([directories] in the global file) or "flag" (--add-dir /
// --read-dir). The source rides the session log so the record says who
// granted what.
type addedDir struct {
	sandbox.AddedRoot
	Source string
}

// resolveAddedDirs judges the operator's added directories by the one
// rule (sandbox.RefuseRoot) against this session's project, work
// directory and state, in a fixed order — the write lists before the
// read ones, the configuration before the flags — so a read root that
// lies inside a write root is "already covered" whichever way it was
// given. A refusal is an error naming where the path came from.
func resolveAddedDirs(cfg *config.Config, projectDir, workDir, cfgPath string) ([]addedDir, error) {
	ctx := rootBaseContext(cfgPath)
	ctx.ProjectDir, ctx.WorkDir = realOrSelf(projectDir), realOrSelf(workDir)
	cfgWrite, cfgRead := cfg.Directories.Paths()
	type given struct {
		path, source, label string
		write               bool
	}
	var all []given
	for _, p := range cfgWrite {
		all = append(all, given{p, "config", "[directories].write", true})
	}
	for _, p := range flagAddDir {
		all = append(all, given{p, "flag", "--add-dir", true})
	}
	for _, p := range cfgRead {
		all = append(all, given{p, "config", "[directories].read", false})
	}
	for _, p := range flagReadDir {
		all = append(all, given{p, "flag", "--read-dir", false})
	}
	var out []addedDir
	for _, g := range all {
		// The same directory given twice — in the file and by a flag —
		// is one root, not a contradiction (review of the step 1
		// implementation, #8). Writes are judged first, so a directory
		// given as both is already the write root when its read entry
		// comes.
		if indexOfRoot(out, realOrSelf(g.path)) >= 0 {
			continue
		}
		real, err := sandbox.RefuseRoot(g.path, g.write, ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", g.label, err)
		}
		r := sandbox.AddedRoot{Dir: real, Write: g.write}
		ctx.Roots = append(ctx.Roots, r)
		out = append(out, addedDir{AddedRoot: r, Source: g.source})
	}
	return out, nil
}

func indexOfRoot(roots []addedDir, dir string) int {
	for i, r := range roots {
		if r.Dir == dir {
			return i
		}
	}
	return -1
}

// rootBaseContext is RefuseRoot's context without the project, the work
// directory and the roots: what stays the same for the whole process,
// at startup and for every mid-session grant.
func rootBaseContext(cfgPath string) sandbox.RootContext {
	ctx := sandbox.RootContext{ConfigFile: realOrSelf(cfgPath)}
	if home, err := os.UserHomeDir(); err == nil {
		ctx.Home = realOrSelf(home)
	}
	if p, err := config.DefaultPath(); err == nil {
		ctx.ConfigHome = realOrSelf(filepath.Dir(p))
	}
	if d, err := statedir.Root(); err == nil {
		ctx.StateDir = realOrSelf(d)
	}
	return ctx
}

// realOrSelf resolves an existing directory to the real path Seatbelt
// matches, leaving one that does not exist (or "") as given.
func realOrSelf(dir string) string {
	if dir == "" {
		return ""
	}
	// The kernel's name, so the context compares like with like with
	// what RefuseRoot judges (case included).
	if real, err := sandbox.CanonicalDir(dir); err == nil {
		return real
	}
	if real, err := sandbox.ResolveWriteDir(dir); err == nil {
		return real
	}
	return dir
}

func rootsOf(added []addedDir) []sandbox.AddedRoot {
	out := make([]sandbox.AddedRoot, 0, len(added))
	for _, a := range added {
		out = append(out, a.AddedRoot)
	}
	return out
}

// addedDirsRecord is the session log's record of the grants (ADR-0099
// §4: every grant is written with its source, real path and level).
func addedDirsRecord(added []addedDir) map[string]any {
	roots := make([]map[string]string, 0, len(added))
	for _, a := range added {
		roots = append(roots, map[string]string{"dir": a.Dir, "access": a.Access(), "source": a.Source})
	}
	return map[string]any{"roots": roots}
}

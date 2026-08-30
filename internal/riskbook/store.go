package riskbook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// BaseFileName is the hand-written global layer, beside the operator's
// config (ADR-0050 §1). gem-agent reads it and never writes it — the
// config.toml discipline: writing it is the operator's deliberate act,
// which is the whole trust story of the hand-written route.
const BaseFileName = "risk-rules.md"

// projectFileName is the per-project layer, under the state dir.
const projectFileName = "rules.md"

// layerBudget bounds each layer, in runes. The rulebook rides every
// Review-tier evaluation (per-call side calls, no cache prefix), so
// each layer pays its way; a clip is always disclosed, never silent.
const layerBudget = 4000

// BasePath returns the base layer's path for a given config path.
func BasePath(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), BaseFileName)
}

// projectDirFor returns the state directory holding projectDir's layer.
func projectDirFor(projectDir string) (string, error) {
	root, err := statedir.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "risk-rules", "projects",
		statedir.EscapeProject(projectDir)), nil
}

// ProjectPath returns the project layer's path (whether or not it
// exists yet).
func ProjectPath(projectDir string) (string, error) {
	dir, err := projectDirFor(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, projectFileName), nil
}

// Book is the rulebook as loaded from disk: both layers plus where they
// came from, so every display can carry provenance.
type Book struct {
	Base        string
	BasePath    string
	Project     string
	ProjectPath string
}

// InForce reports whether any layer has content.
func (b Book) InForce() bool {
	return strings.TrimSpace(b.Base) != "" || strings.TrimSpace(b.Project) != ""
}

// Load reads both layers. A missing file is an empty layer, not an
// error; an unreadable file surfaces, because silently judging without
// rules the operator wrote would be worse than failing.
func Load(cfgPath, projectDir string) (Book, error) {
	b := Book{BasePath: BasePath(cfgPath)}
	data, err := os.ReadFile(b.BasePath)
	switch {
	case err == nil:
		b.Base = string(data)
	case !os.IsNotExist(err):
		return b, fmt.Errorf("read %s: %w", b.BasePath, err)
	}

	pp, err := ProjectPath(projectDir)
	if err != nil {
		return b, err
	}
	b.ProjectPath = pp
	// A .project marker mismatch means the escaped name collides with
	// another project: that layer belongs to someone else, so it is
	// not loaded (misattribution is worse than absence).
	if dir := filepath.Dir(pp); dirExists(dir) {
		if ok, _ := statedir.MarkerMatches(dir, projectDir); !ok {
			return b, nil
		}
	}
	data, err = os.ReadFile(pp)
	switch {
	case err == nil:
		b.Project = string(data)
	case !os.IsNotExist(err):
		return b, fmt.Errorf("read %s: %w", pp, err)
	}
	return b, nil
}

func dirExists(dir string) bool {
	fi, err := os.Stat(dir)
	return err == nil && fi.IsDir()
}

// SaveProject writes the project layer atomically (tmp + rename): a
// half-written rulebook would bias every later evaluation with a
// truncated sentence nobody wrote.
func SaveProject(projectDir, text string) error {
	dir, err := projectDirFor(projectDir)
	if err != nil {
		return err
	}
	if err := statedir.EnsureProjectDir(dir, projectDir); err != nil {
		return err
	}
	path := filepath.Join(dir, projectFileName)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// DeleteProject removes the project layer. Removing guidance only ever
// tightens (the judge falls back to its unassisted judgment), so a
// missing file is success, not an error.
func DeleteProject(projectDir string) error {
	path, err := ProjectPath(projectDir)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Clip bounds a layer to the budget, disclosing what was cut — a
// partial rulebook must not masquerade as the whole one.
func Clip(text string) string {
	r := []rune(text)
	if len(r) <= layerBudget {
		return text
	}
	return string(r[:layerBudget]) + fmt.Sprintf("\n[clipped: %d more runes not shown]", len(r)-layerBudget)
}

// Compose renders the injection text the judge reads: both layers,
// each under a provenance header, each clipped with disclosure. The
// layering rule itself (project is the more specific statement) lives
// in the prompt addendum, not here — prose does not override prose
// mechanically (ADR-0050 §1).
func (b Book) Compose() string {
	var parts []string
	if s := strings.TrimSpace(b.Base); s != "" {
		parts = append(parts, "== base rules (hand-written by the operator) ==\n"+Clip(s))
	}
	if s := strings.TrimSpace(b.Project); s != "" {
		parts = append(parts, "== project rules (this project) ==\n"+Clip(s))
	}
	return strings.Join(parts, "\n\n")
}

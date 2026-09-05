package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The default APFS volume folds case: agents.md, AGENTS.md and Agents.md
// are one file, and a name that does not exist yet is created under the
// case the writer typed — which git, the instruction loader and the
// runtime then find under their own spelling. A deny keyed on exact
// case protects nothing on such a volume (external finding after
// v0.70.1). Every persistent-file rule, credential rule and program
// denial must therefore hold for any case of the name.
func TestLanesFoldCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	proj, err := os.MkdirTemp(home, ".gem-agent-case-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(proj) }()
	proj, _ = ResolveWriteDir(proj)
	// Is this volume case-insensitive? The probes below only mean
	// something there; on a case-sensitive volume the variants are
	// unrelated files and the assertions still hold (denied by the
	// folded rule) — but say which world the run was in.
	probe := filepath.Join(proj, "Case.txt")
	_ = os.WriteFile(probe, []byte("x"), 0o600)
	_, foldErr := os.Stat(filepath.Join(proj, "case.txt"))
	t.Logf("volume folds case: %v", foldErr == nil)

	if err := os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(proj, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".git", "config"), []byte("[core]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch, _ := ResolveWriteDir(t.TempDir())
	spec := Spec{ProjectDir: proj, Home: filepath.Join(proj, "nohome"), DenyExec: DefaultDenyExec, ReadScratch: scratch}
	writeProfile, err := LaneProfile(LaneWrite, spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(profile, command string) string {
		argv := Wrap(profile, "/bin/bash", "cd "+shellQuote(proj)+" && "+command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	// Controls first: the same commands against ordinary names succeed,
	// so a denial below is the rule and not a broken harness.
	if out := run(writeProfile, "echo x > plain.md && cat plain.md"); !strings.Contains(out, "x") {
		t.Fatalf("control write in the write lane failed: %s", strings.TrimSpace(out))
	}
	// Write lane: case variants of the persistent names.
	writes := map[string]string{ // command → path that must not exist / change
		"echo x > agents.md":                  "AGENTS.md",
		"echo x > Claude.md":                  "Claude.md",
		"echo x > .git/hooks/PRE-COMMIT":      ".git/hooks/PRE-COMMIT",
		"echo x > .GIT/config":                ".git/config",
		"echo x > .Mcp.json":                  ".Mcp.json",
		"mkdir -p .Claude/skills/x":           ".Claude",
		"echo x > .Gem-Agent.toml":            ".Gem-Agent.toml",
		"mkdir sub && echo x > sub/Gemini.MD": "sub/Gemini.MD",
	}
	for command, path := range writes {
		before, _ := os.ReadFile(filepath.Join(proj, path))
		out := run(writeProfile, command)
		after, err := os.ReadFile(filepath.Join(proj, path))
		if before == nil && err == nil {
			t.Errorf("write lane: %q created %s (%s)", command, path, strings.TrimSpace(out))
		} else if before != nil && string(before) != string(after) {
			t.Errorf("write lane: %q changed %s (%s)", command, path, strings.TrimSpace(out))
		}
	}
	// Read lane: case variants of credential names and denied programs.
	if err := os.MkdirAll(filepath.Join(proj, "nohome", ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"nohome/.ssh/id_rsa", "sub2/.env", "sub2/service-account.json"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(proj, f)), 0o755)
		if err := os.WriteFile(filepath.Join(proj, f), []byte("SECRET\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	readProfile, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "sub2", "plain.txt"), []byte("PLAIN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out := run(readProfile, "cat sub2/plain.txt"); !strings.Contains(out, "PLAIN") {
		t.Fatalf("control read in the read lane failed: %s", strings.TrimSpace(out))
	}
	if out := run(readProfile, "/usr/bin/true && echo RAN"); !strings.Contains(out, "RAN") {
		t.Fatalf("control exec in the read lane failed: %s", strings.TrimSpace(out))
	}
	for _, command := range []string{
		"cat nohome/.SSH/id_rsa", "cat nohome/.ssh/ID_RSA", "cat sub2/.ENV", "cat sub2/Service-Account.json",
	} {
		if out := run(readProfile, command); strings.Contains(out, "SECRET") {
			t.Errorf("read lane: %q read the credential", command)
		}
	}
	if out := run(readProfile, "/usr/bin/OSASCRIPT -e 'return 1' && echo RAN"); strings.Contains(out, "RAN") {
		t.Errorf("read lane: osascript ran under a case variant of its path: %s", strings.TrimSpace(out))
	}
}

// The file-tool side of the same rules must fold case too: on the
// default volume write_file("agents.md") lands in AGENTS.md, and a
// created ".git/hooks/PRE-COMMIT" is the hook git runs.
func TestPersistentFileFoldsCase(t *testing.T) {
	for _, rel := range []string{
		"agents.md", "Agents.md", "sub/claude.MD", ".Mcp.json", ".GEM-AGENT.toml",
		".GIT", ".git/HOOKS/pre-commit", ".git/hooks/PRE-COMMIT", ".GIT/config", ".git/CONFIG.lock", ".Claude/skills/x/SKILL.md",
	} {
		if !PersistentFile(rel) {
			t.Errorf("PersistentFile(%q) = false; the volume folds case", rel)
		}
	}
	for _, rel := range []string{"agents.txt", "readme.md", "src/gitconfig", ".github/x"} {
		if PersistentFile(rel) {
			t.Errorf("PersistentFile(%q) = true", rel)
		}
	}
}

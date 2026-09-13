package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// TestFileChildUnderTheRealProfile drives the shipped child, built from
// this tree, under the profile the runtime installs. It exists because
// its absence is why ADR-0086 shipped with `search_files` answering
// "no matches (0 files scanned)" for any project holding a `.env`:
// every other test stubbed the child or simulated the refusal with
// `chmod 000`, which denies `open` while leaving `lstat` working —
// the one difference that mattered (independent review).
//
// Whatever else is mocked, this test runs the real binary, the real
// profile and the real kernel.
func TestFileChildUnderTheRealProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "gem-agent-child-test")
	build := exec.Command("go", "build", "-o", bin, "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	proj := t.TempDir()
	for name, body := range map[string]string{
		"plain.txt":    "needle here\n",
		"sub/deep.txt": "needle here\n",
		".env":         "needle SECRET=hunter2\n",
		".env.example": "needle SECRET=\n",
	} {
		full := filepath.Join(proj, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	profile := sandbox.FileReadProfile(filepath.Dir(proj))
	run := func(tool string, args map[string]any) (string, int) {
		req, err := json.Marshal(fileChildRequest{ProjectDir: proj, Tool: tool, Args: args})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sandbox.Executable, "-p", profile, bin, fileChildCommand)
		cmd.Env = sandbox.ChildEnv(os.Environ())
		cmd.Stdin = bytes.NewReader(req)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		code := 0
		if err := cmd.Run(); err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatalf("%s: %v", tool, err)
			}
			code = ee.ExitCode()
		}
		if code != 0 {
			return errb.String(), code
		}
		return out.String(), 0
	}

	// The defect this test exists for: a walk over a project holding a
	// credential file must scan the project, not report it empty.
	out, code := run("search_files", map[string]any{"pattern": "needle"})
	if code != 0 {
		t.Fatalf("search_files exited %d: %s", code, out)
	}
	for _, want := range []string{"plain.txt:1", "sub/deep.txt:1", ".env.example:1", "[not read: .env"} {
		if !strings.Contains(out, want) {
			t.Errorf("search_files missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "hunter2") || strings.Contains(out, "no matches") {
		t.Errorf("search_files read the credential file or reported an empty project:\n%s", out)
	}

	// A single-file read of credential material is the operator's
	// question, carried by the exit code and no bytes.
	for _, tool := range []string{"read_file", "file_info"} {
		out, code := run(tool, map[string]any{"path": ".env"})
		if code != credentialExit {
			t.Errorf("%s .env exited %d, want %d: %s", tool, code, credentialExit, out)
		}
		if strings.Contains(out, "hunter2") {
			t.Errorf("%s leaked the content: %s", tool, out)
		}
	}
	// The committed template and ordinary files read as before.
	for _, p := range []string{".env.example", "plain.txt"} {
		if out, code := run("read_file", map[string]any{"path": p}); code != 0 || !strings.Contains(out, "needle") {
			t.Errorf("read_file %s exited %d: %q", p, code, out)
		}
	}
	// The hidden subcommand runs the covered reads and nothing else.
	if out, code := run("list_files", map[string]any{}); code == 0 {
		t.Errorf("the child ran a tool outside its set: %q", out)
	}
}

// TestInstallFileChildSucceedsOnThisMachine is the test whose absence
// shipped a release whose headline feature never switched on: the probe
// asked the child for a tool the child refuses, so it failed on every
// start, the cage was never installed, and the operator got a warning
// telling them to restart. Driving the tools directly was not enough —
// the installer is its own path, and it is the one that decides whether
// any of this is in force.
func TestInstallFileChildSucceedsOnThisMachine(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if testing.Short() {
		t.Skip("spawns the child")
	}
	if err := sandbox.Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "child-installer-test")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	reg, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if reg.KernelReads() {
		t.Fatal("a fresh registry claims kernel reads before one is installed")
	}
	probeDir, done := fileProbeDir("")
	if probeDir == "" {
		t.Skip("no probe directory")
	}
	defer done()
	home, _ := os.UserHomeDir()
	if note := installFileChildWith(reg, bin, home, probeDir); note != "" {
		t.Fatalf("the probe refused to install the cage: %s", note)
	}
	if !reg.KernelReads() {
		t.Error("installFileChild returned no note but installed nothing")
	}
	if _, err := os.Stat(probeDir); err == nil {
		done()
		if _, err := os.Stat(probeDir); err == nil {
			t.Errorf("the probe directory outlives the probe: %s", probeDir)
		}
	}
}

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

func TestProfileContainsResolvedDirs(t *testing.T) {
	p, err := Profile([]string{"/private/tmp/proj", "/private/tmp/scratch"}, []string{"/dev/null"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"(deny file-write*)",
		`(subpath "/private/tmp/proj")`,
		`(subpath "/private/tmp/scratch")`,
	} {
		if !strings.Contains(p, want) {
			t.Errorf("profile missing %q:\n%s", want, p)
		}
	}
}

func TestProfileRejectsRelativeDir(t *testing.T) {
	if _, err := Profile([]string{"relative/path"}, nil); err == nil {
		t.Fatal("relative dir should be rejected")
	}
}

func TestProfileRejectsEmpty(t *testing.T) {
	if _, err := Profile(nil, nil); err == nil {
		t.Fatal("empty write list should be rejected")
	}
}

func TestSBPLStringEscaping(t *testing.T) {
	got := sbplString(`/path/with"quote\back`)
	want := `"/path/with\"quote\\back"`
	if got != want {
		t.Errorf("sbplString = %s, want %s", got, want)
	}
}

// TestSandboxExecEnforcement runs real sandbox-exec (darwin only): a write
// inside the allowed project dir must succeed, a write outside must fail.
// This is the load-bearing test — the profile text being well-formed means
// nothing unless Seatbelt actually enforces it.
func TestSandboxExecEnforcement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here (nested sandbox?): %v", err)
	}
	inside, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	profile, err := Profile([]string{inside}, ScratchFiles())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	run := func(command string) error {
		argv := Wrap(profile, "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		return cmd.Run()
	}

	if err := run("echo ok > " + filepath.Join(inside, "allowed.txt")); err != nil {
		t.Errorf("write inside allowed dir should succeed: %v", err)
	}
	if err := run("echo bad > " + filepath.Join(outside, "denied.txt")); err == nil {
		t.Error("write outside allowed dir should be denied by sandbox-exec")
	}
	// Reads outside the write allowlist must still work (allow default).
	if err := run("ls / > " + filepath.Join(inside, "ls.txt")); err != nil {
		t.Errorf("read outside + write inside should succeed: %v", err)
	}
}

// ADR-0073: the lanes. The profile text is checked for the rules each
// lane must carry; the live test below checks that Seatbelt enforces
// them.
func TestLaneProfiles(t *testing.T) {
	spec := Spec{ProjectDir: "/private/tmp/proj", WorkDir: "/private/tmp/work", Home: "/Users/op", DenyExec: []string{"osascript"}}
	read, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(deny network*)", "(deny user-preference-write)", "(deny signal)", "(deny process-exec", `/osascript"`, `(subpath "/Users/op/.ssh")`, `\.env`} {
		if !strings.Contains(read, want) {
			t.Errorf("read lane lacks %q:\n%s", want, read)
		}
	}
	if !strings.Contains(read, "(deny file-write*\n    (subpath \"/private/tmp/proj\")") {
		t.Error("read lane must deny project writes by name (after the scratch allow)")
	}
	if strings.Contains(read, "(allow file-write*\n    (subpath \"/private/tmp/proj\")") {
		t.Error("read lane must not allow project writes")
	}
	write, err := LaneProfile(LaneWrite, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`(subpath "/private/tmp/proj")`, `(subpath "/private/tmp/work")`, `AGENTS|AGENT|CLAUDE|GEMINI`, `\.git/(hooks|info)`, `(subpath "/Users/op/.ssh")`} {
		if !strings.Contains(write, want) {
			t.Errorf("write lane lacks %q:\n%s", want, write)
		}
	}
	if strings.Contains(write, "(deny network*)") {
		t.Error("write lane must not deny the network")
	}
	op, err := LaneProfile(LaneOperator, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(op, "AGENTS") || strings.Contains(op, ".ssh") {
		t.Error("operator lane must allow persistent files and credential reads")
	}
	if _, err := ParseLane("root"); err == nil {
		t.Error("unknown lane accepted")
	}
	if l, _ := ParseLane(""); l != LaneRead {
		t.Error("empty access is not the read lane")
	}
}

func TestPersistentAndCredentialRulesAgree(t *testing.T) {
	for _, rel := range []string{"AGENTS.md", "sub/CLAUDE.md", ".git/hooks/pre-commit", ".git/config", "vendor/x/.git/config.lock", ".claude/skills/a/SKILL.md", ".mcp.json", "docs/.gem-agent.toml"} {
		if !PersistentFile(rel) {
			t.Errorf("%q not persistent", rel)
		}
	}
	for _, rel := range []string{"README.md", ".git/index", ".git/objects/ab/cd", "src/agents.go", "gitconfig", ".github/workflows/x.yml"} {
		if PersistentFile(rel) {
			t.Errorf("%q wrongly persistent", rel)
		}
	}
	for _, p := range []string{"~/.ssh/id_rsa", ".env", ".env.local", "/x/credentials.json", "~/.aws/credentials", "sa-service-account.json", "~/.netrc"} {
		if !CredentialPath(p) {
			t.Errorf("%q not credential", p)
		}
	}
	for _, p := range []string{".env.example", "environment.go", "README.md", "src/main.go", ".envrc-notes.md"} {
		if CredentialPath(p) {
			t.Errorf("%q wrongly credential", p)
		}
	}
}

// TestLaneEnforcement runs real sandbox-exec: the read lane denies a
// project write, the network and a preference write; the write lane
// allows the project write but denies AGENTS.md, .git/hooks and a
// credential read through every spelling probed in ADR-0073; the
// operator lane allows all of them.
func TestLaneEnforcement(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here (nested sandbox?): %v", err)
	}
	// The project must not sit under a scratch root, or the read
	// lane's scratch allow would cover it: use the home directory.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	proj, err := os.MkdirTemp(home, ".gem-agent-lane-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(proj) }()
	proj, _ = ResolveWriteDir(proj)
	fakeHome := filepath.Join(proj, "home")
	for _, d := range []string{".git/hooks", "home/.ssh"} {
		if err := os.MkdirAll(filepath.Join(proj, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"AGENTS.md": "rules\n", ".git/config": "cfg\n", "home/.ssh/id_rsa": "secret\n", ".env": "K=v\n", ".env.example": "K=\n"} {
		if err := os.WriteFile(filepath.Join(proj, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	spec := Spec{ProjectDir: proj, Home: fakeHome, DenyExec: DefaultDenyExec}
	run := func(lane Lane, command string) error {
		profile, err := LaneProfile(lane, spec)
		if err != nil {
			t.Fatal(err)
		}
		argv := Wrap(profile, "/bin/bash", command)
		return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	}
	agents := filepath.Join(proj, "AGENTS.md")
	cases := []struct {
		lane    Lane
		command string
		allowed bool
		what    string
	}{
		{LaneRead, "cat " + agents, true, "read lane reads the project"},
		{LaneRead, "echo x > " + filepath.Join(proj, "new.txt"), false, "read lane denies a project write"},
		{LaneRead, "echo x > /dev/null", true, "read lane allows the device sink"},
		{LaneRead, "cat " + filepath.Join(fakeHome, ".ssh/id_rsa"), false, "read lane denies a credential read"},
		{LaneRead, "cat " + filepath.Join(proj, ".env"), false, "read lane denies .env"},
		{LaneRead, "cat " + filepath.Join(proj, ".env.example"), true, "read lane allows the .env template"},
		{LaneRead, "/usr/bin/osascript -e 'return 1'", false, "read lane denies osascript"},
		{LaneRead, "sleep 5 & kill $!", true, "read lane lets a command signal its own child"},
		{LaneWrite, "echo x > " + filepath.Join(proj, "new.txt"), true, "write lane allows a project write"},
		{LaneWrite, "echo pwned > " + agents, false, "write lane denies a redirect onto AGENTS.md"},
		{LaneWrite, "echo pwned > " + filepath.Join(proj, "t.txt") + " && mv " + filepath.Join(proj, "t.txt") + " " + agents, false, "write lane denies a rename onto AGENTS.md"},
		{LaneWrite, "rm " + agents, false, "write lane denies removing AGENTS.md"},
		{LaneWrite, "echo x > " + filepath.Join(proj, ".git/hooks/pre-commit"), false, "write lane denies a hook"},
		{LaneWrite, "echo x > " + filepath.Join(proj, ".git/index"), true, "write lane allows ordinary .git writes"},
		{LaneWrite, "cat " + filepath.Join(fakeHome, ".ssh/id_rsa"), false, "write lane denies a credential read"},
		{LaneOperator, "cat " + filepath.Join(fakeHome, ".ssh/id_rsa"), true, "operator lane allows a credential read"},
		{LaneOperator, "echo ok > " + agents, true, "operator lane allows AGENTS.md"},
	}
	for _, c := range cases {
		err := run(c.lane, c.command)
		if c.allowed && err != nil {
			t.Errorf("%s: %q failed: %v", c.what, c.command, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: %q succeeded", c.what, c.command)
		}
	}
	if got, _ := os.ReadFile(agents); string(got) != "ok\n" {
		t.Errorf("AGENTS.md = %q: the write lane changed it or the operator lane could not", got)
	}
	// A project under a scratch root (a checkout in /private/tmp) is
	// still not writable in the read lane: the deny by name follows the
	// scratch allow.
	tmpProj, _ := ResolveWriteDir(t.TempDir())
	if !strings.HasPrefix(tmpProj, "/private/") && !strings.HasPrefix(tmpProj, os.TempDir()) {
		t.Skip("TempDir is not under a scratch root")
	}
	tmpSpec := Spec{ProjectDir: tmpProj, Home: fakeHome}
	profile, err := LaneProfile(LaneRead, tmpSpec)
	if err != nil {
		t.Fatal(err)
	}
	argv := Wrap(profile, "/bin/bash", "echo x > "+filepath.Join(tmpProj, "new.txt"))
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err == nil {
		t.Error("read lane wrote into a project that lives under a scratch root")
	}
	argv = Wrap(profile, "/bin/bash", "echo x > "+filepath.Join(os.TempDir(), "gem-agent-lane-probe-"+filepath.Base(tmpProj))+" && rm "+filepath.Join(os.TempDir(), "gem-agent-lane-probe-"+filepath.Base(tmpProj)))
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err != nil {
		t.Errorf("read lane denied a scratch write beside the project: %v", err)
	}
}

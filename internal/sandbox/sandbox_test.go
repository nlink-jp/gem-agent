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
	spec := Spec{ProjectDir: "/private/tmp/proj", WorkDir: "/private/tmp/work", Home: "/Users/op", DenyExec: []string{"osascript"}, ReadScratch: "/private/tmp/work/scratch"}
	read, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"(deny network*)", "(deny mach-lookup)", "(deny appleevent-send)", "(deny ipc-posix*)", "(deny iokit-open)", "(deny user-preference-write)", "(deny lsopen)", "(deny signal)", "(deny process-exec", `/osascript"`, `(subpath "/Users/op/.ssh")`, `\.env`, "(allow file-write*\n    (subpath \"/private/tmp/work/scratch\")"} {
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
	if strings.Contains(read, `(subpath "/private/tmp")`) || strings.Contains(read, "(deny sysctl-write)") {
		t.Error("read lane must not allow the shared scratch roots, and must not deny sysctl-write (uname needs it)")
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
	scratch, _ := ResolveWriteDir(t.TempDir())
	spec := Spec{ProjectDir: proj, Home: fakeHome, DenyExec: DefaultDenyExec, ReadScratch: scratch}
	run := func(lane Lane, command string) error {
		profile, err := LaneProfile(lane, spec)
		if err != nil {
			t.Fatal(err)
		}
		argv := Wrap(profile, "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if lane == LaneRead {
			cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		}
		return cmd.Run()
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
		{LaneRead, `echo x > "$TMPDIR/probe" && rm "$TMPDIR/probe"`, true, "read lane allows its private scratch"},
		{LaneRead, "echo x > /private/tmp/gem-agent-lane-probe-$$", false, "read lane denies the shared /private/tmp"},
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
		{LaneWrite, "cd " + shellQuote(proj) + " && git init -q .", false, "write lane denies git init (it writes .git/config and hooks) — agent-board review"},
		{LaneOperator, "cd " + shellQuote(proj) + " && git init -q . && test -f .git/config", true, "operator lane allows git init"},
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
	tmpSpec := Spec{ProjectDir: tmpProj, Home: fakeHome, ReadScratch: scratch}
	profile, err := LaneProfile(LaneRead, tmpSpec)
	if err != nil {
		t.Fatal(err)
	}
	argv := Wrap(profile, "/bin/bash", "echo x > "+filepath.Join(tmpProj, "new.txt"))
	if err := exec.CommandContext(ctx, argv[0], argv[1:]...).Run(); err == nil {
		t.Error("read lane wrote into a project that lives under a scratch root")
	}
}

// TestReadLaneCorpus is the old shell corpus moved from the text tier
// to the kernel (design review of ADR-0073): every spelling that once
// needed a regex to catch — redirects, tee, sed -i, find -exec, xargs,
// env, awk system(), command substitution, python, dd, install, mv,
// truncate, chmod — is tried against the read lane, and the project
// file must be untouched afterwards. The capability, not the command
// name, is what the lane denies.
func TestReadLaneCorpus(t *testing.T) {
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
	proj, err := os.MkdirTemp(home, ".gem-agent-corpus-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(proj) }()
	proj, _ = ResolveWriteDir(proj)
	target := filepath.Join(proj, "f.txt")
	if err := os.WriteFile(target, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	scratch, _ := ResolveWriteDir(t.TempDir())
	profile, err := LaneProfile(LaneRead, Spec{ProjectDir: proj, Home: filepath.Join(proj, "nohome"), ReadScratch: scratch})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	q := shellQuote(target)
	corpus := []string{
		"echo x > " + q,
		"echo x >> " + q,
		"echo x | tee " + q,
		"sed -i '' s/keep/gone/ " + q,
		"find " + shellQuote(proj) + " -name f.txt -exec rm {} \\;",
		"find " + shellQuote(proj) + " -name f.txt -delete",
		"echo " + q + " | xargs rm",
		"env rm " + q,
		"awk 'BEGIN{system(\"rm " + target + "\")}'",
		"$(echo rm) " + q,
		"python3 -c 'open(" + shellQuote(target) + ",\"w\").write(\"x\")'",
		"perl -e 'open(F,\">\",\"" + target + "\"); print F 1'",
		"dd if=/dev/zero of=" + q + " bs=1 count=1",
		"install -m 600 /dev/null " + q,
		"mv " + q + " " + shellQuote(target+".moved"),
		"cp /etc/hosts " + q,
		"truncate -s 0 " + q,
		"chmod 000 " + q,
		"ln -sf /etc/hosts " + q,
		"touch " + shellQuote(filepath.Join(proj, "new.txt")),
		"mkdir " + shellQuote(filepath.Join(proj, "newdir")),
		"cd " + shellQuote(proj) + " && git init -q .",
	}
	for _, command := range corpus {
		argv := Wrap(profile, "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		_ = cmd.Run() // failing is the point; the assertion is on the file
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "keep\n" {
			t.Errorf("%q changed the project file: %q %v", command, got, err)
			_ = os.WriteFile(target, []byte("keep\n"), 0o600)
		}
		if st, err := os.Stat(target); err == nil && st.Mode().Perm() != 0o600 {
			t.Errorf("%q changed the mode: %v", command, st.Mode())
			_ = os.Chmod(target, 0o600)
		}
	}
	entries, _ := os.ReadDir(proj)
	if len(entries) != 1 {
		t.Errorf("the read lane created entries in the project: %v", entries)
	}
	// Other implementation paths for the network and preferences.
	for _, command := range []string{
		"exec 3<>/dev/tcp/127.0.0.1/9",
		"python3 -c 'import socket; s=socket.socket(); s.settimeout(2); s.connect((\"127.0.0.1\",9))'",
		"python3 -c 'import ctypes,ctypes.util; cf=ctypes.CDLL(ctypes.util.find_library(\"CoreFoundation\")); cf.CFStringCreateWithCString.restype=ctypes.c_void_p; s=lambda x: ctypes.c_void_p(cf.CFStringCreateWithCString(None,x.encode(),0x08000100)); cf.CFPreferencesSetAppValue(s(\"k\"),s(\"v\"),s(\"com.nlink.gem-agent.lanetest\")); import sys; sys.exit(0 if cf.CFPreferencesAppSynchronize(s(\"com.nlink.gem-agent.lanetest\")) else 1)'",
		"kill -0 1",
	} {
		argv := Wrap(profile, "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		if err := cmd.Run(); err == nil {
			t.Errorf("%q succeeded in the read lane", command)
		}
	}
}

// TestVerifyReadLane: the real read profile passes; a profile that
// allows everything is refused, so an environment where the kernel
// does not deny what the lane claims never gets an unasked read lane.
func TestVerifyReadLane(t *testing.T) {
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
	proj, err := os.MkdirTemp(home, ".gem-agent-verify-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(proj) }()
	proj, _ = ResolveWriteDir(proj)
	scratch, _ := ResolveWriteDir(t.TempDir())
	spec := Spec{ProjectDir: proj, Home: filepath.Join(proj, "nohome"), DenyExec: DefaultDenyExec, ReadScratch: scratch}
	profile, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyReadLane(profile, spec); err != nil {
		t.Errorf("the real read lane failed verification: %v", err)
	}
	if err := VerifyReadLane("(version 1)(allow default)", spec); err == nil {
		t.Error("an allow-everything profile passed verification")
	}
	if entries, _ := os.ReadDir(proj); len(entries) != 0 {
		t.Errorf("verification left files in the project: %v", entries)
	}
}

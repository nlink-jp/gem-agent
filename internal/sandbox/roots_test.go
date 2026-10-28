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

// TestLaneProfilesAddedRoots holds the shape of ADR-0099 §1/§7: a write
// root is in the write and operator lanes' write allow, a read root in
// none; the read lane re-allows reading both and denies writing both;
// the write lane denies a write root's persistent names; the credential
// block stays the last read rule.
func TestLaneProfilesAddedRoots(t *testing.T) {
	spec := Spec{ProjectDir: "/private/tmp/proj", WorkDir: "/private/tmp/work", Home: "/Users/op", ReadScratch: "/private/tmp/work/scratch",
		Added: []AddedRoot{{Dir: "/Users/op/notes", Write: true}, {Dir: "/Volumes/Archive/ref", Write: false}}}
	allowOf := func(p string) string {
		i := strings.Index(p, "(allow file-write*\n")
		j := strings.Index(p[i:], "\n)\n")
		return p[i : i+j]
	}
	for _, lane := range []Lane{LaneWrite, LaneOperator} {
		p, err := LaneProfile(lane, spec)
		if err != nil {
			t.Fatal(err)
		}
		allow := allowOf(p)
		if !strings.Contains(allow, `(subpath "/Users/op/notes")`) {
			t.Errorf("lane %v does not allow writing the write root:\n%s", lane, allow)
		}
		if strings.Contains(p, "/Volumes/Archive/ref") {
			t.Errorf("lane %v names the read root (it must write it nowhere):\n%s", lane, p)
		}
	}
	write, _ := LaneProfile(LaneWrite, spec)
	if !strings.Contains(write, `^/Users/op/notes/`) {
		t.Errorf("write lane does not anchor the persistent names at the write root:\n%s", write)
	}
	read, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(allowOf(read), "/Users/op/notes") {
		t.Error("read lane allows writing a write root")
	}
	deny := read[strings.Index(read, "(deny file-write*\n    (subpath \"/private/tmp/proj\")"):]
	deny = deny[:strings.Index(deny, ")\n)")+3]
	for _, d := range []string{"/Users/op/notes", "/Volumes/Archive/ref"} {
		if !strings.Contains(deny, d) {
			t.Errorf("read lane does not deny writing %s beside the project:\n%s", d, deny)
		}
	}
	reallow := "(allow file-read*\n    (subpath \"/Users/op/notes\")\n    (subpath \"/Volumes/Archive/ref\")\n)"
	i := strings.Index(read, reallow)
	if i < 0 {
		t.Fatalf("read lane lacks the added roots' read re-allow:\n%s", read)
	}
	if j := strings.Index(read, "(deny file-read*\n    (subpath \"/Volumes\")"); j < 0 || j > i {
		t.Error("the re-allow must follow the reach denials")
	}
	if c := strings.LastIndex(read, "\n    "+CredentialFilters(spec.Home)[0]+"\n"); c < i {
		t.Error("the credential block must follow the re-allow")
	}
}

func TestRefuseRoot(t *testing.T) {
	// Under the real home: t.TempDir sits inside TMPDIR, a shared
	// temporary directory RefuseRoot refuses by rule.
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	base, err := os.MkdirTemp(userHome, ".gem-agent-refuse-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(base) }()
	if base, err = ResolveWriteDir(base); err != nil {
		t.Fatal(err)
	}
	mk := func(rel string) string {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	home := mk("home")
	proj := mk("home/dev/proj")
	c := RootContext{Home: home, ProjectDir: proj, ConfigHome: mk("home/.config/gem-agent"), StateDir: mk("home/.local/state/gem-agent"),
		Roots: []AddedRoot{{Dir: mk("home/ref"), Write: false}, {Dir: mk("home/notes"), Write: true}}}
	mk("home/.ssh")
	mk("home/Library/LaunchAgents")
	mk("home/Library/Mobile Documents/x")
	if err := os.WriteFile(filepath.Join(base, "home/file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mk("home/vault"), filepath.Join(home, "vault-link")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		write bool
		want  string // "" = accepted; else a fragment of the error
	}{
		{filepath.Join(home, "vault"), true, ""},
		{filepath.Join(home, "vault-link"), true, ""}, // resolved: the real path is judged
		{filepath.Join(home, "Library/Mobile Documents/x"), false, ""},
		{filepath.Join(home, "missing"), false, "not found"},
		{filepath.Join(home, "file"), false, "not a directory"},
		{"/", false, "root of the filesystem"},
		{home, false, "home directory"},
		{base, false, "home directory"},
		{filepath.Join(home, "dev"), true, "contains the project"},
		{filepath.Join(home, ".config"), false, "contains the gem-agent configuration home"},
		{filepath.Join(home, ".config/gem-agent"), false, "configuration home"},
		{filepath.Join(home, ".local/state/gem-agent"), false, "state directory"},
		{filepath.Join(home, ".ssh"), false, "credential location"},
		{filepath.Join(home, "Library/LaunchAgents"), true, "~/Library"},
		{filepath.Join(home, "Library/Mobile Documents/x"), true, "~/Library"},
		{proj, true, "already covered"},
		{filepath.Join(home, "ref"), false, "already covered"},
		{filepath.Join(home, "notes"), false, "already covered"},
		{filepath.Join(home, "notes"), true, "already covered"},
	} {
		got, err := RefuseRoot(tc.path, tc.write, c)
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s (write=%v): refused: %v", tc.path, tc.write, err)
		case tc.want == "" && got != filepath.Join(home, "vault") && !strings.HasPrefix(got, home):
			t.Errorf("%s: real path %q", tc.path, got)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s (write=%v): want %q, got %v", tc.path, tc.write, tc.want, err)
		}
	}
	// A write request inside a read root is an upgrade, not covered.
	if _, err := RefuseRoot(mk("home/ref/sub"), true, c); err != nil {
		t.Errorf("a write root inside a read root was refused: %v", err)
	}
	if got, _ := RefuseRoot(filepath.Join(home, "vault-link"), true, c); got != filepath.Join(home, "vault") {
		t.Errorf("a link resolves to %q, want the real path", got)
	}
	// A shared temporary directory is refused (the root could be swapped).
	tmp, err := os.MkdirTemp("/private/tmp", "gem-agent-root-test-")
	if err != nil {
		t.Skip("cannot create under /private/tmp")
	}
	defer func() { _ = os.Remove(tmp) }()
	if _, err := RefuseRoot(tmp, false, c); err == nil || !strings.Contains(err.Error(), "shared temporary") {
		t.Errorf("a root under /private/tmp: %v", err)
	}
	// A root holding a scratch root or the devices (review of the step 1
	// implementation, #5).
	for _, p := range []string{"/private/var", "/dev"} {
		if _, err := RefuseRoot(p, false, c); err == nil || !strings.Contains(err.Error(), "contains a shared temporary directory or the devices") {
			t.Errorf("%s: %v", p, err)
		}
	}
	// A write root may not contain the configuration file in use (#2);
	// a read root may.
	dot := mk("home/dotfiles")
	cc := c
	cc.ConfigFile = filepath.Join(dot, "gem-agent.toml")
	if _, err := RefuseRoot(dot, true, cc); err == nil || !strings.Contains(err.Error(), "configuration file in use") {
		t.Errorf("a write root holding --config: %v", err)
	}
	if _, err := RefuseRoot(dot, false, cc); err != nil {
		t.Errorf("a read root holding --config was refused: %v", err)
	}
}

// TestAddedRootEnforcement measures ADR-0099 §1 under real sandbox-exec,
// and that VerifyAddedRoots passes on the same profiles. The read root
// sits under the fake home's Library, a tree the read lane denies for
// reach, so the re-allow is what lets it be read.
func TestAddedRootEnforcement(t *testing.T) {
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
	base, err := os.MkdirTemp(home, ".gem-agent-root-test-")
	if err != nil {
		t.Skip("cannot create a directory under home")
	}
	defer func() { _ = os.RemoveAll(base) }()
	base, _ = ResolveWriteDir(base)
	fakeHome := filepath.Join(base, "home")
	proj := filepath.Join(base, "proj")
	wroot := filepath.Join(base, "notes")
	rroot := filepath.Join(fakeHome, "Library", "Ref")
	other := filepath.Join(fakeHome, "Library", "Other")
	for _, d := range []string{proj, wroot, rroot, other} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(wroot, "plain"), filepath.Join(wroot, ".env"), filepath.Join(rroot, "plain"), filepath.Join(rroot, ".env"), filepath.Join(other, "plain")} {
		if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scratch, _ := ResolveWriteDir(t.TempDir())
	spec := Spec{ProjectDir: proj, Home: fakeHome, DenyExec: DefaultDenyExec, ReadScratch: scratch,
		Added: []AddedRoot{{Dir: wroot, Write: true}, {Dir: rroot, Write: false}}}
	profiles := map[Lane]string{}
	for _, l := range []Lane{LaneRead, LaneWrite, LaneOperator} {
		p, err := LaneProfile(l, spec)
		if err != nil {
			t.Fatal(err)
		}
		profiles[l] = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(lane Lane, command string) error {
		argv := Wrap(profiles[lane], "/bin/bash", command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		return cmd.Run()
	}
	q := shellQuote
	for _, c := range []struct {
		lane    Lane
		command string
		allowed bool
		what    string
	}{
		{LaneRead, "cat " + q(filepath.Join(rroot, "plain")), true, "read lane reads a read root under the denied Library"},
		{LaneRead, "cat " + q(filepath.Join(other, "plain")), false, "read lane still denies the rest of Library"},
		{LaneRead, "cat " + q(filepath.Join(rroot, ".env")), false, "read lane denies a credential name in a re-allowed read root"},
		{LaneRead, "cat " + q(filepath.Join(wroot, ".env")), false, "read lane denies a credential name in a write root"},
		{LaneRead, "echo x > " + q(filepath.Join(wroot, "r.txt")), false, "read lane does not write a write root"},
		{LaneRead, "echo x > " + q(filepath.Join(rroot, "r.txt")), false, "read lane does not write a read root"},
		{LaneWrite, "echo x > " + q(filepath.Join(wroot, "w.txt")), true, "write lane writes a write root"},
		{LaneWrite, "echo x > " + q(filepath.Join(wroot, "AGENTS.md")), false, "write lane denies a persistent name in a write root"},
		{LaneWrite, "mkdir -p " + q(filepath.Join(wroot, "sub/.git/hooks")) + " && echo x > " + q(filepath.Join(wroot, "sub/.git/hooks/pre-commit")), false, "write lane denies a hook in a write root"},
		{LaneWrite, "echo x > " + q(filepath.Join(rroot, "w.txt")), false, "write lane does not write a read root"},
		{LaneOperator, "echo x > " + q(filepath.Join(wroot, "o.txt")), true, "operator lane writes a write root"},
		{LaneOperator, "echo x > " + q(filepath.Join(rroot, "o.txt")), false, "operator lane does not write a read root"},
	} {
		err := run(c.lane, c.command)
		if c.allowed && err != nil {
			t.Errorf("%s: %q failed: %v", c.what, c.command, err)
		}
		if !c.allowed && err == nil {
			t.Errorf("%s: %q succeeded", c.what, c.command)
		}
	}
	notes, err := VerifyAddedRoots(profiles, spec, true)
	if err != nil || len(notes) != 0 {
		t.Errorf("VerifyAddedRoots on correct profiles: notes %v, err %v", notes, err)
	}
	// The probe must catch a profile that forgets the read root's
	// write denial in the operator lane: build one with the read root
	// promoted to a write root and hand VerifyAddedRoots the read spec.
	wrong := spec
	wrong.Added = []AddedRoot{{Dir: rroot, Write: true}}
	op, _ := LaneProfile(LaneOperator, wrong)
	bad := map[Lane]string{LaneRead: profiles[LaneRead], LaneWrite: profiles[LaneWrite], LaneOperator: op}
	if _, err := VerifyAddedRoots(bad, Spec{Added: []AddedRoot{{Dir: rroot, Write: false}}}, true); err == nil || !strings.Contains(err.Error(), "operator lane") {
		t.Errorf("VerifyAddedRoots missed an operator lane that writes a read root: %v", err)
	}
	// With the read lane unverified, a read-lane profile that would fail
	// is not blamed on the root: its probes are skipped with a note.
	broken := map[Lane]string{LaneRead: "(version 1)(allow default)", LaneWrite: profiles[LaneWrite], LaneOperator: profiles[LaneOperator]}
	if notes, err := VerifyAddedRoots(broken, spec, false); err != nil || len(notes) != 1 {
		t.Errorf("unverified read lane: notes %v, err %v", notes, err)
	}
	if _, err := VerifyAddedRoots(broken, spec, true); err == nil {
		t.Error("a read lane that writes an added root passed its probes")
	}
	if entries, _ := os.ReadDir(rroot); len(entries) != 2 {
		t.Errorf("probes left files behind in the read root: %v", entries)
	}
}

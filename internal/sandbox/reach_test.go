package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// ADR-0102 §1–§2: only a root strictly inside a tree denied for reach is
// re-allowed; one that is such a tree, or contains one, is not.
func TestOwnRootsUnderReach(t *testing.T) {
	home := "/Users/someone"
	cases := []struct {
		project string
		want    bool
	}{
		{home + "/Library/CloudStorage/GoogleDrive-x/My Drive/proj", true},
		{"/Volumes/share/works/proj", true},
		{"/Network/Servers/x/proj", true},
		{home + "/works/proj", false},
		{home + "/Library", false}, // is a denied tree
		{home, false},              // contains ~/Library
		{"/Volumes", false},
		{"/", false},
	}
	for _, c := range cases {
		got := ownRootsUnderReach(Spec{ProjectDir: c.project, Home: home})
		if (len(got) == 1) != c.want {
			t.Errorf("%s: re-allowed %v, want %v", c.project, got, c.want)
		}
	}
	both := ownRootsUnderReach(Spec{ProjectDir: home + "/Library/CloudStorage/d/p", WorkDir: "/Volumes/s/work", Home: home})
	if !slices.Equal(both, []string{home + "/Library/CloudStorage/d/p", "/Volumes/s/work"}) {
		t.Errorf("project and work dir: %v", both)
	}
}

// The re-allow sits after the reach denial and before the credential
// block, which stays the profile's last read rule.
func TestReachReallowOrdersBeforeCredentials(t *testing.T) {
	home := "/Users/someone"
	proj := home + "/Library/CloudStorage/d/p"
	prof, err := LaneProfile(LaneRead, Spec{ProjectDir: proj, Home: home, ReadScratch: "/private/tmp/s"})
	if err != nil {
		t.Fatal(err)
	}
	deny := strings.Index(prof, "(subpath \""+home+"/Library\")")
	allow := strings.Index(prof, "(allow file-read*\n    (subpath \""+proj+"\")")
	cred := strings.LastIndex(prof, "(deny file-read*\n")
	if deny < 0 || allow < 0 || deny >= allow || allow >= cred {
		t.Errorf("order: library deny %d, project re-allow %d, credential block %d", deny, allow, cred)
	}
}

// Live: a project under a reach-denied tree has a working read lane; the
// rest of the tree and the credentials inside the project stay denied;
// a project at the home itself does not re-open ~/Library.
func TestReadLaneRunsInAProjectUnderReach(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if err := Available(); err != nil {
		t.Skipf("sandbox-exec cannot apply a profile here: %v", err)
	}
	home, err := ResolveWriteDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(home, "Library", "CloudStorage", "Drive", "proj")
	mail := filepath.Join(home, "Library", "Mail")
	for _, d := range []string{proj, mail} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for p, body := range map[string]string{
		filepath.Join(proj, "data.txt"): "PROJECT-DATA\n",
		filepath.Join(proj, ".env"):     "SECRET=hunter2\n",
		filepath.Join(mail, "msg.txt"):  "MAIL-BODY\n",
	} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scratch, _ := ResolveWriteDir(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(projectDir, command string) string {
		prof, err := LaneProfile(LaneRead, Spec{ProjectDir: projectDir, Home: home, ReadScratch: scratch})
		if err != nil {
			t.Fatal(err)
		}
		argv := Wrap(prof, "/bin/bash", "cd "+shellQuote(projectDir)+" && "+command)
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Env = append(os.Environ(), "TMPDIR="+scratch)
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	if out := run(proj, "/bin/pwd && /bin/cat data.txt"); !strings.Contains(out, "PROJECT-DATA") || strings.Contains(out, "not permitted") {
		t.Errorf("the read lane in a project under ~/Library: %q", out)
	}
	if out := run(proj, "/bin/cat "+shellQuote(filepath.Join(mail, "msg.txt"))); strings.Contains(out, "MAIL-BODY") {
		t.Error("the rest of ~/Library became readable")
	}
	if out := run(proj, "/bin/cat .env"); strings.Contains(out, "hunter2") {
		t.Error("a credential inside the project became readable")
	}
	if out := run(home, "/bin/cat "+shellQuote(filepath.Join(mail, "msg.txt"))); strings.Contains(out, "MAIL-BODY") {
		t.Error("a project at the home re-opened ~/Library")
	}
}

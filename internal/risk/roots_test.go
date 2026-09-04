package risk

import (
	"path/filepath"
	"strings"
	"testing"
)

// The rule tier's writable places are the sandbox's (ADR-0070 §2): a
// redirect into a scratch root is not "outside the project", and
// /dev/null is a sink, not a write. Session 20260904-225330 was
// Blocked for `2>/dev/null` with a reason that was not true.
func TestScratchRootsAreWritable(t *testing.T) {
	if len(scratchRoots) == 0 {
		t.Skip("no scratch roots resolve on this machine")
	}
	for _, cmd := range []string{
		"echo hi > /dev/null",
		"ls 2>/dev/null",
		"find . -name validate.py 2>/dev/null | grep incident-research",
		"cat README.md 2>&1",
		"ls &>/dev/null",
	} {
		if v := classifyShell(cmd); v.Tier != Safe {
			t.Errorf("%q = %v (%s), want Safe", cmd, v.Tier, v.Reason)
		}
	}
	for _, root := range scratchRoots {
		cmd := "echo hi > " + filepath.Join(root, "out.txt")
		if v := classifyShell(cmd); v.Tier == Block {
			t.Errorf("%q was Blocked (%s): a scratch root the sandbox allows", cmd, v.Reason)
		}
	}
	// Still outside: a redirect to a place Seatbelt will deny.
	if v := classifyShell("ls > /etc/passwd"); v.Tier != Block {
		t.Errorf("redirect outside every root = %v, want Block", v.Tier)
	}
}

// A read-only walk from outside the roots is Review, not Safe (ADR-0070
// §3): the sandbox denies writes only, so `find /` reaches every mount.
func TestWalkOutsideRootsIsReview(t *testing.T) {
	work := "/state/work/sess-1"
	classify := func(cmd string) Verdict {
		return Classify("shell_exec", true, map[string]any{"command": cmd}, proj, work)
	}
	review := []string{
		`find / -name "validate.py" 2>/dev/null | grep incident-research`,
		"find / -name validate.py",
		"find ~ -name x",
		"find ~/.config/gem-agent/skills -name validate.py",
		"du -sh /Volumes/storage",
		"grep -r TODO /etc",
		"grep -rn TODO /usr/local",
		"rg pattern /usr",
		"fd validate.py /",
		"ls; find /Volumes -name x",
	}
	for _, cmd := range review {
		v := classify(cmd)
		if v.Tier != Review || !strings.Contains(v.Reason, "walks the filesystem") {
			t.Errorf("%q = %v (%s), want Review naming the walk", cmd, v.Tier, v.Reason)
		}
	}
	safe := []string{
		"find . -name validate.py",
		"find " + proj + "/sub -name x",
		"find " + work + " -name x",
		"grep TODO /etc/hosts", // a single read, not a walk
		"cat /etc/hosts",
		"ls ~/Downloads",
		"du -sh .",
	}
	for _, cmd := range safe {
		if v := classify(cmd); v.Tier != Safe {
			t.Errorf("%q = %v (%s), want Safe", cmd, v.Tier, v.Reason)
		}
	}
	// Without a work directory the reason names only the project.
	if v := classifyShell("find / -name x"); !strings.HasSuffix(v.Reason, "outside the project directory") {
		t.Errorf("reason without a work dir = %q", v.Reason)
	}
	// Block stays the floor: a walk that also destroys is still Block.
	if v := classify("find / -name x -delete; rm -rf /"); v.Tier != Block {
		t.Errorf("destructive walk = %v, want Block", v.Tier)
	}
}

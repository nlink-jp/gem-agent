package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ADR-0109: the directory an approved build trusts is the read lane's
// to leave alone. The root is placed under /private/var/tmp on purpose:
// the read lane may write that shared directory, so nothing but the
// named denial protects a child of it.
func TestReadLaneCannotWriteApprovedCache(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	proj, _ := ResolveWriteDir(t.TempDir())
	scratch, _ := ResolveWriteDir(t.TempDir())
	approved, err := os.MkdirTemp("/private/var/tmp", "gem-agent-approved-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(approved)
		_ = os.RemoveAll(approved + ".moved") // a failing run's rename
	})
	spec := Spec{ProjectDir: proj, Home: filepath.Join(proj, "nohome"), ReadScratch: scratch, ApprovedCacheRoot: approved}

	readProfile, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	writeProfile, err := LaneProfile(LaneWrite, spec)
	if err != nil {
		t.Fatal(err)
	}
	operatorProfile, err := LaneProfile(LaneOperator, spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	run := func(profile, command string) error {
		argv := Wrap(profile, "/bin/bash", command)
		return exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	}

	planted := filepath.Join(approved, "planted")
	for _, command := range []string{
		"echo x > " + shellQuote(planted),
		"mkdir " + shellQuote(filepath.Join(approved, "go-build")),
		"mv " + shellQuote(approved) + " " + shellQuote(approved+".moved"),
	} {
		_ = run(readProfile, command) // failing is the point; the assertion is on the tree
	}
	entries, err := os.ReadDir(approved)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the read lane changed the approved cache: entries=%v err=%v", entries, err)
	}

	for lane, profile := range map[string]string{"write": writeProfile, "operator": operatorProfile} {
		target := filepath.Join(approved, lane)
		if err := run(profile, "echo x > "+shellQuote(target)); err != nil {
			t.Errorf("the %s lane cannot write its cache: %v", lane, err)
		}
	}
	// The read lane's own scratch stays writable: the denial is by name.
	if err := run(readProfile, "echo x > "+shellQuote(filepath.Join(scratch, "ok"))); err != nil {
		t.Errorf("the read lane lost its scratch: %v", err)
	}
}

// The approved root is writable in the approved lanes even when no
// scratch root would have covered it, and the read lane's profile names
// it after its last write allow.
func TestApprovedCacheInProfiles(t *testing.T) {
	spec := Spec{ProjectDir: "/private/tmp/proj", WorkDir: "/private/tmp/work", Home: "/Users/op", ReadScratch: "/private/tmp/work/scratch", ApprovedCacheRoot: "/opt/approved"}
	read, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	deny := `(deny file-write* (subpath "/opt/approved"))`
	allow := `(allow file-write* (subpath "/private/tmp/work/scratch"))`
	if strings.LastIndex(read, deny) < strings.LastIndex(read, allow) || !strings.Contains(read, deny) {
		t.Errorf("the denial does not follow the scratch allow:\n%s", read)
	}
	for _, lane := range []Lane{LaneWrite, LaneOperator} {
		p, err := LaneProfile(lane, spec)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p, `"/opt/approved"`) || strings.Contains(p, deny) {
			t.Errorf("%v: the approved root is not writable in the profile", lane)
		}
	}
}

// A probe that fails tells the operator; VerifyReadLane passes on a
// profile that holds, and leaves nothing behind in the approved root.
func TestVerifyReadLaneProbesApprovedCache(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	proj, _ := ResolveWriteDir(t.TempDir())
	scratch, _ := ResolveWriteDir(t.TempDir())
	approved, _ := ResolveWriteDir(t.TempDir())
	spec := Spec{ProjectDir: proj, Home: filepath.Join(proj, "nohome"), ReadScratch: scratch, ApprovedCacheRoot: approved}
	profile, err := LaneProfile(LaneRead, spec)
	if err != nil {
		t.Fatal(err)
	}
	if left, err := VerifyReadLane(profile, spec); err != nil || len(left) != 0 {
		t.Fatalf("VerifyReadLane: left=%v err=%v", left, err)
	}
	if entries, _ := os.ReadDir(approved); len(entries) != 0 {
		t.Errorf("probe left in the approved cache: %v", entries)
	}

	// A profile that lets the read lane write there must be caught.
	weak := profile + "(allow file-write* (subpath " + sbplString(approved) + "))\n"
	_, err = VerifyReadLane(weak, spec)
	if err == nil || !strings.Contains(err.Error(), "the read lane allowed a write into the approved") {
		t.Errorf("a read lane that can write the approved cache passed: %v", err)
	}
}

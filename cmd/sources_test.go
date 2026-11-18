package cmd

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sessionWiringFiles are the files runREPL's session is assembled in
// since ADR-0124: the entry, the startup phases, the live session, its
// surface, the front ends, its teardown and the model seam.
// TestSessionWiringFilesAreComplete keeps the list from falling behind.
var sessionWiringFiles = []string{"root.go", "startup.go", "live.go", "surface.go", "frontends.go", "cleanups.go", "modelclient.go"}

// Every file that defines a method of liveSession, or a function that
// takes one, is session wiring: a file missing from the list is a file
// the wiring checks do not read.
func TestSessionWiringFilesAreComplete(t *testing.T) {
	names, src := cmdSources(t)
	listed := map[string]bool{}
	for _, n := range sessionWiringFiles {
		listed[n] = true
	}
	for _, name := range names {
		if strings.Contains(src[name], "*liveSession") && !listed[name] {
			t.Errorf("%s handles the live session but is not in sessionWiringFiles", name)
		}
	}
	for n := range listed {
		if _, ok := src[n]; !ok {
			t.Errorf("sessionWiringFiles names %s, which is not a source file of cmd", n)
		}
	}
}

// cmdSources is every non-test Go file of this package, by name, in
// name order. The tests that pin wiring by reading the source read all
// of it: ADR-0124 moved runREPL's parts out of root.go, and a check that
// reads one file stops checking the moment its code moves to another.
func cmdSources(t *testing.T) (names []string, src map[string]string) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	src = map[string]string{}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, p)
		src[p] = string(raw)
	}
	sort.Strings(names)
	if len(names) == 0 {
		t.Fatal("no source files found — the scan is broken, not the tree")
	}
	return names, src
}

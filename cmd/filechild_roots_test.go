package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// The child opens only the roots a call's arguments lie in, a relative
// argument being the project's (ADR-0114 §4).
func TestRootsNamedBy(t *testing.T) {
	project := "/w/proj"
	ref, inner, other := sandbox.AddedRoot{Dir: "/w/ref"}, sandbox.AddedRoot{Dir: "/w/ref/inner", Write: true}, sandbox.AddedRoot{Dir: "/x/other"}
	added := []sandbox.AddedRoot{ref, inner, other}
	cases := []struct {
		name string
		args map[string]any
		want []sandbox.AddedRoot
	}{
		{"a project file", map[string]any{"path": "a.txt"}, nil},
		{"no path at all", map[string]any{"pattern": "foo"}, nil},
		{"a relative path staying home", map[string]any{"path": "sub/../a.txt"}, nil},
		{"an absolute path in a root", map[string]any{"path": "/w/ref/x.txt"}, []sandbox.AddedRoot{ref}},
		{"the root itself", map[string]any{"path": "/x/other"}, []sandbox.AddedRoot{other}},
		{"a relative path beside the project", map[string]any{"path": "../ref/x.txt"}, []sandbox.AddedRoot{ref}},
		{"nested roots both open", map[string]any{"path": "/w/ref/inner/y"}, []sandbox.AddedRoot{ref, inner}},
		{"a batch names each", map[string]any{"paths": []any{"a.txt", "/x/other/y", 3}}, []sandbox.AddedRoot{other}},
		{"a sibling with the root's prefix", map[string]any{"path": "/w/reference/x"}, nil},
		{"not a path", map[string]any{"path": 3, "depth": 2.0}, nil},
	}
	for _, c := range cases {
		if got := rootsNamedBy(added, project, c.args); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// runFileChild runs one child in this process and returns what it wrote,
// failing if it does not come back while an added root will not open.
func runFileChild(t *testing.T, req fileChildRequest, unblock string) string {
	t.Helper()
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	c := newFileChildCmd()
	var out bytes.Buffer
	c.SetIn(bytes.NewReader(in))
	c.SetOut(&out)
	c.SetArgs([]string{})
	done := make(chan error, 1)
	go func() { done <- c.Execute() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		// Lets the blocked open finish, so the goroutine does not outlive
		// the test.
		if f, err := os.OpenFile(unblock, os.O_RDWR, 0); err == nil {
			defer func() { _ = f.Close() }()
		}
		<-done
		t.Fatal("a read stalled on an added root it does not name")
	}
	return out.String()
}

// A root that will not open costs a read that does not name it nothing.
// A FIFO in the root's place blocks the open as a share that has stopped
// answering does (ADR-0114 §4).
func TestFileChildDoesNotOpenARootTheCallDoesNotName(t *testing.T) {
	project, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.WriteFile(filepath.Join(project, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stalled := filepath.Join(t.TempDir(), "stalled")
	if err := syscall.Mkfifo(stalled, 0o644); err != nil {
		t.Fatal(err)
	}
	got := runFileChild(t, fileChildRequest{
		ProjectDir: project, Added: []sandbox.AddedRoot{{Dir: stalled}},
		Tool: "read_file", Args: map[string]any{"path": "a.txt"},
	}, stalled)
	if !strings.Contains(got, "hello") {
		t.Errorf("result = %q", got)
	}
}

// A root the call does name is opened, by an absolute path and by a
// relative one reaching beside the project (ADR-0114 §4).
func TestFileChildOpensTheRootTheCallNames(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	project, ref := filepath.Join(base, "proj"), filepath.Join(base, "ref")
	for _, d := range []string{project, ref} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(ref, "r.txt"), []byte("from the root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(ref, "r.txt"), "../ref/r.txt"} {
		got := runFileChild(t, fileChildRequest{
			ProjectDir: project, Added: []sandbox.AddedRoot{{Dir: ref}},
			Tool: "read_file", Args: map[string]any{"path": path},
		}, "")
		if !strings.Contains(got, "from the root") {
			t.Errorf("%s: result = %q", path, got)
		}
	}
}

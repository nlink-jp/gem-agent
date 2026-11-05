package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/risk"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

func realDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A write through a link outside every root lands in the root it leads
// to, auto-approved as an edit there, and every observer of the call
// sees the real path (ADR-0108 §1).
func TestWriteThroughAnOutsideLinkLandsInTheRoot(t *testing.T) {
	wroot, outside := realDir(t), realDir(t)
	link := filepath.Join(outside, "Dropbox")
	if err := os.Symlink(wroot, link); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(link, "hello.txt")
	b := &autoBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{writeCall(typed)}},
		{Content: "done"},
	}}
	gate := &recordingGate{}
	a, reg, decisions := newAutoAgent(t, b, gate)
	if err := reg.AddDirs([]sandbox.AddedRoot{{Dir: wroot, Write: true}}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	a.onToolCall = func(tc llm.ToolCall) { seen = append(seen, tc.Args["path"].(string)) }
	if _, err := a.Run(context.Background(), "write it", nil); err != nil {
		t.Fatal(err)
	}
	if len(gate.asked) != 0 {
		t.Errorf("the write reached the gate: %v", gate.asked)
	}
	if d := (*decisions)[0]; !d.Approved || d.Tier != risk.Safe || !strings.Contains(d.Reason, "added directory") {
		t.Errorf("decision = %+v, want a safe edit inside an added directory", d)
	}
	if b, err := os.ReadFile(filepath.Join(wroot, "hello.txt")); err != nil || string(b) != "x" {
		t.Errorf("the write did not land in the root: %q %v", b, err)
	}
	want := filepath.Join(wroot, "hello.txt")
	if len(seen) != 1 || seen[0] != want {
		t.Errorf("the call was shown as %v, want %q", seen, want)
	}
}

// A link to the project's AGENTS.md is judged as AGENTS.md — the
// operator's alone — and the approval names the real file.
func TestALinkToTheProjectsInstructionsIsTheOperators(t *testing.T) {
	outside := realDir(t)
	b := &autoBackend{verdict: `{"approve": true, "confidence": 1.0, "reason": "fine"}`}
	gate := &recordingGate{}
	a, reg, _ := newAutoAgent(t, b, gate)
	link := filepath.Join(outside, "work")
	if err := os.Symlink(reg.ProjectDir(), link); err != nil {
		t.Fatal(err)
	}
	b.responses = []*llm.Response{
		{ToolCalls: []llm.ToolCall{writeCall(filepath.Join(link, "AGENTS.md"))}},
		{Content: "done"},
	}
	if _, err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	// The detail is clipped for the prompt, so the reason carries the
	// judgement and the detail must not show the link's spelling.
	if len(gate.asked) != 1 || !strings.Contains(gate.asked[0], "the operator decides") || strings.Contains(gate.asked[0], outside) {
		t.Errorf("gate = %v, want the operator asked about the real AGENTS.md", gate.asked)
	}
	if _, err := os.Stat(filepath.Join(reg.ProjectDir(), "AGENTS.md")); err == nil {
		t.Error("AGENTS.md was written past a denied approval")
	}
}

// After the rewrite the outside link is never consulted: swapping it
// for another target does not move the write (ADR-0108 §1, the design
// review's swap window).
func TestTheRewrittenCallDoesNotFollowASwappedLink(t *testing.T) {
	wroot, outside, other := realDir(t), realDir(t), realDir(t)
	link := filepath.Join(outside, "Dropbox")
	if err := os.Symlink(wroot, link); err != nil {
		t.Fatal(err)
	}
	a, reg, _ := newAutoAgent(t, &autoBackend{}, &recordingGate{})
	if err := reg.AddDirs([]sandbox.AddedRoot{{Dir: wroot, Write: true}, {Dir: other, Write: true}}); err != nil {
		t.Fatal(err)
	}
	tc := a.intoRoots(writeCall(filepath.Join(link, "x.txt")))
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, link); err != nil {
		t.Fatal(err)
	}
	tool, _ := reg.Get("write_file")
	if _, err := tool.Run(context.Background(), tc.Args); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wroot, "x.txt")); err != nil {
		t.Errorf("the write left the root the verdict judged: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "x.txt")); err == nil {
		t.Error("the write followed the swapped link")
	}
}

// A link leading out of every root is left as typed and refused with
// today's words, which do not name where it leads.
func TestALinkLeadingOutIsRefusedWithoutItsTarget(t *testing.T) {
	outside, escape := realDir(t), realDir(t)
	link := filepath.Join(outside, "elsewhere")
	if err := os.Symlink(escape, link); err != nil {
		t.Fatal(err)
	}
	a, reg, _ := newAutoAgent(t, &autoBackend{}, &recordingGate{})
	typed := filepath.Join(link, "x.txt")
	tc := a.intoRoots(writeCall(typed))
	if tc.Args["path"] != typed {
		t.Fatalf("a link leading out was rewritten to %v", tc.Args["path"])
	}
	tool, _ := reg.Get("write_file")
	_, err := tool.Run(context.Background(), tc.Args)
	if err == nil || strings.Contains(err.Error(), escape) {
		t.Errorf("err = %v, want a refusal that does not name %s", err, escape)
	}
	if _, serr := os.Stat(filepath.Join(escape, "x.txt")); serr == nil {
		t.Error("the write escaped")
	}
	// shell_exec and MCP tools are not rewritten.
	for _, name := range []string{"shell_exec", "mcp__x__y"} {
		c := llm.ToolCall{Name: name, Args: map[string]any{"path": filepath.Join(link, "x")}}
		if got := a.intoRoots(c); got.Args["path"] != c.Args["path"] {
			t.Errorf("%s was rewritten", name)
		}
	}
}

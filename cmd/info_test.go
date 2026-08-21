package cmd

import (
	"context"
	"strings"
	"testing"

	"go/ast"
	"go/parser"
	"go/token"
	"strconv"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/skills"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

func infoFixture() infoSnapshot {
	return infoSnapshot{
		Version:      "v0.25.0-test",
		OSVersion:    "15.6",
		Model:        "gemini-test-model",
		SummaryModel: "gemini-test-lite",
		Thinking:     "",
		Usage: agent.UsageStats{
			Rounds: 5, Prompt: 45200, Output: 3100, Thoughts: 2000,
			Cached: 30100, LastPrompt: 12300, Window: 1000000,
		},
		MaxTurns: 50, ShellTimeout: 120,
		AutoApprove: false, AutoCompact: true, CompactAtPct: 80,
		SandboxOn:  true,
		ProjectDir: "/tmp/proj",
		SessionID:  "2026-08-21-abcdef",
		MCPServers: []string{"toolbox [project] (4 tools)"},
		SkillCount: 3, MemoryOn: true, MediaBucket: true,
		ProjectTrusted: true,
	}
}

// An untrusted project must say so (review round 2): without the line
// the model misdiagnosed the ADR-0023 gate as missing configuration.
func TestRenderInfoNamesDeclinedTrust(t *testing.T) {
	s := infoFixture()
	if strings.Contains(renderInfo(s), "project trust") {
		t.Error("trusted project must not render a trust warning")
	}
	s.ProjectTrusted = false
	if !strings.Contains(renderInfo(s), "project trust: declined/undecided") {
		t.Error("untrusted project must explain why project tools are missing")
	}
}

// The three slash-command surfaces (completions, the slashOutput
// switch, help) are hand-maintained lists; this pins them together so
// a command added to one cannot silently miss the others (review
// round 2 — /exit lived only in the switch).
func TestSlashSurfacesAgree(t *testing.T) {
	comps := slashCompletions(func() []skills.Skill { return nil })("/")
	if len(comps) == 0 {
		t.Fatal("no completions")
	}
	handled := slashOutputCases(t)
	for _, c := range comps {
		switch c {
		case "/settings", "/compact", "/skill":
			continue // intercepted upstream of slashOutput in both UIs
		}
		if !handled[c] {
			t.Errorf("completion %s has no case in slashOutput", c)
		}
	}
	help := uitext.For(uitext.EN).Help
	for _, c := range comps {
		if c == "/exit" {
			continue // alias; documented on the /quit line
		}
		if !strings.Contains(help, c) {
			t.Errorf("completion %s missing from help", c)
		}
	}
	// The reverse direction: every handled command is completable.
	compSet := map[string]bool{}
	for _, c := range comps {
		compSet[c] = true
	}
	for cmd := range handled {
		if !compSet[cmd] {
			t.Errorf("slashOutput handles %s but Tab cannot complete it", cmd)
		}
	}
}

// slashOutputCases extracts the string case labels of slashOutput's
// switch from the AST — the function needs a live agent to call, and a
// wiring test must not depend on one.
func slashOutputCases(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "root.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "slashOutput" {
			return true
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			cc, ok := n.(*ast.CaseClause)
			if !ok {
				return true
			}
			for _, e := range cc.List {
				if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if s, err := strconv.Unquote(lit.Value); err == nil && strings.HasPrefix(s, "/") {
						cases[s] = true
					}
				}
			}
			return true
		})
		return false
	})
	if len(cases) == 0 {
		t.Fatal("no slashOutput switch cases found")
	}
	return cases
}

// TestRenderInfoFields pins every behavioral field of ADR-0030 into
// the rendered page — and pins the §3 exclusions out of it.
func TestRenderInfoFields(t *testing.T) {
	out := renderInfo(infoFixture())
	for _, want := range []string{
		"v0.25.0-test", "macOS 15.6", "CPUs",
		"gemini-test-model", "thinking: model default", "gemini-test-lite",
		"12.3k of 1.0M window (1%)", "auto-compact at 80%",
		"5 rounds", "prompt 45.2k", "output 3.1k", "thoughts 2.0k", "cached 30.1k",
		"max 50 turns", "shell timeout 120s",
		"auto-approve OFF", "sandbox ON",
		"/tmp/proj", "2026-08-21-abcdef",
		"toolbox [project] (4 tools)",
		"skills: 3", "memory: ON", "media bucket: configured",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderInfo missing %q:\n%s", want, out)
		}
	}
}

// TestRenderInfoQuietStates covers the degraded shapes: fresh session
// (no rounds), no window, no session log, no MCP, no bucket.
func TestRenderInfoQuietStates(t *testing.T) {
	s := infoFixture()
	s.Usage = agent.UsageStats{}
	s.SessionID = ""
	s.MCPServers = nil
	s.MediaBucket = false
	s.OSVersion = ""
	s.Thinking = "high"
	out := renderInfo(s)
	for _, want := range []string{
		"no requests yet", "mcp servers: none",
		"media bucket: none (media attaches inline", "thinking: high",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderInfo missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "session:") {
		t.Errorf("disabled session log must not render a session line:\n%s", out)
	}
	if strings.Contains(out, "usage so far") {
		t.Errorf("zero rounds must not render a usage line:\n%s", out)
	}
}

// TestAgentInfoToolRegistration: read-only tier (no approval), and Run
// renders the provider's snapshot at call time.
func TestAgentInfoToolRegistration(t *testing.T) {
	registry, err := tools.New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := registerInfoTool(registry, func() infoSnapshot {
		calls++
		return infoFixture()
	}); err != nil {
		t.Fatal(err)
	}
	tool, ok := registry.Get("agent_info")
	if !ok {
		t.Fatal("agent_info not registered")
	}
	if tool.Mutating {
		t.Error("agent_info must be read-only — an approval prompt for self-description is friction with no risk behind it")
	}
	out, err := tool.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(out, "gemini-test-model") {
		t.Errorf("run: calls=%d out=%q", calls, out)
	}
}

// TestMacOSVersionLive: on the platform this project binds to
// (macOS-only by design), the sysctl must answer.
func TestMacOSVersionLive(t *testing.T) {
	v := macOSVersion()
	if v == "" || !strings.Contains(v, ".") {
		t.Errorf("macOSVersion() = %q, want a dotted product version", v)
	}
}

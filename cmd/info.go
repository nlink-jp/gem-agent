package cmd

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"syscall"

	"github.com/nlink-jp/gem-agent/internal/agent"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// infoSnapshot is everything agent_info reports (ADR-0030). Fields
// earn their place by changing what the model should do — environment
// identifiers with no behavioral value (GCP project id, bucket name,
// hostname) are deliberately absent.
type infoSnapshot struct {
	Version      string
	OSVersion    string // macOS product version; "" when undetectable
	Model        string
	SummaryModel string
	Thinking     string // "" = model default
	Usage        agent.UsageStats
	MaxTurns     int
	ShellTimeout int
	AutoApprove  bool
	AutoCompact  bool
	CompactAtPct int
	SandboxOn    bool
	ProjectDir   string
	SessionID    string // "" when the log is disabled
	MCPServers   []string
	SkillCount   int
	MemoryOn     bool
	MediaBucket  bool
	// ProjectTrusted: false means the project's OWN instruction files,
	// .mcp.json, and skills were not loaded (ADR-0023). Without this
	// line the model misdiagnosed missing tools as missing
	// configuration (review round 2).
	ProjectTrusted bool
}

// macOSVersion asks the kernel; an error just blanks the field — a
// self-description tool must never fail over decoration.
func macOSVersion() string {
	v, err := syscall.Sysctl("kern.osproductversion")
	if err != nil {
		return ""
	}
	return v
}

// renderInfo turns a snapshot into the tool result. Model-facing
// English by design (ADR-0029 §3, ADR-0030 §4).
func renderInfo(s infoSnapshot) string {
	var b strings.Builder

	osName := runtime.GOOS
	if s.OSVersion != "" {
		osName = fmt.Sprintf("macOS %s", s.OSVersion)
	}
	fmt.Fprintf(&b, "gem-agent %s on %s (%s, %d CPUs)\n",
		s.Version, osName, runtime.GOOS+"/"+runtime.GOARCH, runtime.NumCPU())

	thinking := s.Thinking
	if thinking == "" {
		thinking = "model default"
	}
	fmt.Fprintf(&b, "model: %s (thinking: %s)", s.Model, thinking)
	if s.SummaryModel != "" && s.SummaryModel != s.Model {
		fmt.Fprintf(&b, " · summary/fetch model: %s", s.SummaryModel)
	}
	b.WriteString("\n")

	u := s.Usage
	switch {
	case u.Rounds == 0:
		b.WriteString("context: no requests yet this session\n")
	case u.Window > 0:
		fmt.Fprintf(&b, "context: %s of %s window (%.0f%%)",
			humanTok(u.LastPrompt), humanTok(u.Window), 100*float64(u.LastPrompt)/float64(u.Window))
		if s.AutoCompact {
			fmt.Fprintf(&b, " — auto-compact at %d%%", s.CompactAtPct)
		}
		b.WriteString("\n")
	default:
		fmt.Fprintf(&b, "context: %s (window unknown)\n", humanTok(u.LastPrompt))
	}
	if u.Rounds > 0 {
		fmt.Fprintf(&b, "usage so far: %d rounds · prompt %s · output %s · thoughts %s · cached %s\n",
			u.Rounds, humanTok(u.Prompt), humanTok(u.Output), humanTok(u.Thoughts), humanTok(u.Cached))
	}

	fmt.Fprintf(&b, "limits: max %d turns per run · shell timeout %ds\n", s.MaxTurns, s.ShellTimeout)
	fmt.Fprintf(&b, "approval: auto-approve %s · sandbox %s\n", onOff(s.AutoApprove), onOff(s.SandboxOn))
	fmt.Fprintf(&b, "project: %s\n", s.ProjectDir)
	if s.SessionID != "" {
		fmt.Fprintf(&b, "session: %s\n", s.SessionID)
	}

	// "at startup": the summary is the connection-time snapshot — a
	// server whose child process died later is still listed (review
	// round 2; honest labeling over a liveness system this tool does
	// not have).
	if len(s.MCPServers) == 0 {
		b.WriteString("mcp servers: none\n")
	} else {
		fmt.Fprintf(&b, "mcp servers (as connected at startup): %s\n", strings.Join(s.MCPServers, "; "))
	}
	if !s.ProjectTrusted {
		b.WriteString("project trust: declined/undecided — the project's own instruction files, .mcp.json, and skills are NOT loaded (ADR-0023); missing tools may be this, not missing configuration\n")
	}
	bucket := "none (media attaches inline, small files only)"
	if s.MediaBucket {
		bucket = "configured (large audio/video can attach)"
	}
	fmt.Fprintf(&b, "skills: %d · memory: %s · media bucket: %s\n",
		s.SkillCount, onOff(s.MemoryOn), bucket)
	return b.String()
}

func onOff(v bool) string {
	if v {
		return "ON"
	}
	return "OFF"
}

// registerInfoTool adds agent_info (ADR-0030): read-only, no approval,
// snapshot assembled at call time so context/usage numbers are current.
func registerInfoTool(registry *tools.Registry, snap func() infoSnapshot) error {
	return registry.Register(&tools.Tool{
		Name: "agent_info",
		Description: "Report this agent's own runtime: version, host platform, the model you are " +
			"running as and its thinking level, context-window occupancy, cumulative token usage, " +
			"limits, approval/sandbox state, project directory, session id, connected MCP servers " +
			"and skills. Call this when asked what model or system you are, about token consumption " +
			"or remaining context, or when planning work against the context budget. Read-only.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return renderInfo(snap()), nil
		},
	})
}

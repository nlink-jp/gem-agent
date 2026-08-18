package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Execute runs the root command.
func Execute(version string) {
	rootCmd.Version = version
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "gem-agent",
	Short: "Interactive CLI agent backed by Vertex AI Gemini (Claude Code fallback)",
	Long: `gem-agent is an interactive CLI agent backed by Vertex AI Gemini 3.x,
built as a continuity tool for development work when Claude Code is
unavailable. It reads a project's AGENTS.md / CLAUDE.md / .mcp.json as-is
(drop-in), provides file read/write, sandboxed command execution, and MCP
server connectivity, with write/exec gated behind per-call approval.

macOS only. See docs/ for the RFP and design records.`,
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("not implemented yet: the agent loop arrives in development Phase 1 (see docs/en/gem-agent-rfp.md)")
	},
}

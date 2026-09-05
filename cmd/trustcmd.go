package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/spf13/cobra"
)

// The trust command shows and refreshes the content pins of ADR-0074
// without starting a model session: a scripted `-p` flow that edited
// AGENTS.md re-pins with `--accept` instead of running interactively.

var flagTrustAccept bool

var trustCmd = &cobra.Command{
	Use:   "trust",
	Short: "Show this project's trust state and pinned files",
	Long: `Show whether this project is trusted (ADR-0023), which agent-facing
files are pinned (ADR-0074), and which of them changed since they were
pinned. With --accept, the current content of every pinned file is
recorded as trusted — the answer an interactive start would ask for.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runTrust,
}

func init() {
	trustCmd.Flags().BoolVar(&flagTrustAccept, "accept", false, "record the current content of the agent-facing files as trusted")
	// --config is a root-command flag; the subcommand takes the same
	// one so a scripted flow can name the config it runs with.
	trustCmd.Flags().StringVar(&flagConfig, "config", "", "config file path (default ~/.config/gem-agent/config.toml)")
	rootCmd.AddCommand(trustCmd)
}

func runTrust(cmd *cobra.Command, _ []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	projectDir, err := sandbox.ResolveWriteDir(cwd)
	if err != nil {
		return err
	}
	cfgPath, err := configPath()
	if err != nil {
		return err
	}
	policyPath := config.PolicyPath(cfgPath)
	policyFile, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	trust := policyFile.TrustFor(projectDir)
	granted := trust == config.TrustGranted
	// [approval].trusted_projects is the stronger, hand-written grant
	// (ADR-0023 §4); it counts as trust here as it does at startup.
	if cfg, err := config.LoadWithOverrides(cfgPath, config.Overrides{}); err == nil && cfg.TrustsProject(projectDir) {
		granted = true
		trust = config.TrustGranted + " (config: trusted_projects)"
	}
	if trust == "" {
		trust = "undecided"
	}
	fmt.Fprintf(out, "project: %s\ntrust: %s\n", projectDir, trust)
	if !granted {
		fmt.Fprintln(out, "pins apply to a trusted project only — start gem-agent interactively once to decide")
		return nil
	}
	current, notes := trustpin.Compute(projectDir)
	for _, n := range notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	recorded := policyFile.PinsFor(projectDir)
	if flagTrustAccept {
		if err := savePins(policyPath, projectDir, current, policyFile); err != nil {
			return err
		}
		fmt.Fprintf(out, "pinned %d file(s) as trusted\n", len(current))
		return nil
	}
	if len(recorded) == 0 {
		fmt.Fprintln(out, "pins: none recorded yet (the next interactive start records them)")
		return nil
	}
	names := make([]string, 0, len(recorded))
	for n := range recorded {
		names = append(names, n)
	}
	sort.Strings(names)
	changes := trustpin.Diff(recorded, current)
	changed := map[string]string{}
	for _, c := range changes {
		changed[c.Name] = c.Kind
	}
	fmt.Fprintf(out, "pins: %d recorded\n", len(recorded))
	for _, n := range names {
		state := "unchanged"
		if k, ok := changed[n]; ok {
			state = k
		}
		fmt.Fprintf(out, "  %-40s %s\n", n, state)
	}
	for _, c := range changes {
		if c.Kind == "added" {
			fmt.Fprintf(out, "  %-40s %s (not yet pinned)\n", c.Name, c.Kind)
		}
	}
	if len(changes) > 0 {
		fmt.Fprintln(out, "changed files are not loaded until re-trusted: `gem-agent trust --accept`, or answer the prompt at the next interactive start")
	}
	return nil
}

// configPath resolves the config file the way the root command does:
// --config when given, the default location otherwise.
func configPath() (string, error) {
	if flagConfig != "" {
		return flagConfig, nil
	}
	return config.DefaultPath()
}

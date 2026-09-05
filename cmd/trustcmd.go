package cmd

import (
	"fmt"
	"io"
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
	Short: "Show this project's trust state and trusted files",
	Long: `Show whether this project is trusted, which of its files are recorded
as trusted, and which of them changed since. With --accept, the current
content is recorded as trusted — the answer an interactive start asks for.`,
	Args:         cobra.NoArgs,
	SilenceUsage: true,
	RunE:         runTrust,
}

func init() {
	trustCmd.Flags().BoolVar(&flagTrustAccept, "accept", false, "record the current content of the project's files as trusted")
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
	return trustReport(projectDir, cfgPath, flagTrustAccept, cmd.OutOrStdout())
}

// trustReport is the trust command's body: the trust state, the pins
// and what differs for projectDir under the config at cfgPath; with
// accept, the current content is recorded as trusted. Untrusted with
// accept is an error — there is no trust for the pins to record.
func trustReport(projectDir, cfgPath string, accept bool, out io.Writer) error {
	policyPath := config.PolicyPath(cfgPath)
	policyFile, err := config.LoadPolicyFile(policyPath)
	if err != nil {
		return err
	}
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
		if accept {
			return fmt.Errorf("this project is not trusted — start gem-agent interactively once to decide")
		}
		fmt.Fprintln(out, "trusted files: none — the project is not trusted (start gem-agent interactively once to decide)")
		return nil
	}
	current, notes := trustpin.Compute(projectDir)
	for _, n := range notes {
		fmt.Fprintf(out, "note: %s\n", n)
	}
	if accept {
		if err := savePins(policyPath, projectDir, current, policyFile); err != nil {
			return err
		}
		fmt.Fprintf(out, "%d file(s) recorded as trusted: %s\n", len(current), pinNames(current))
		return nil
	}
	if !policyFile.HasPins(projectDir) {
		fmt.Fprintln(out, "trusted files: none recorded yet — start interactively once, or run `gem-agent trust --accept`")
		return nil
	}
	recorded := policyFile.PinsFor(projectDir)
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
	fmt.Fprintf(out, "trusted files: %d recorded", len(recorded))
	if at := policyFile.Projects[projectDir].PinnedAt; at != "" {
		fmt.Fprintf(out, " (%s)", at)
	}
	fmt.Fprintln(out)
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
	pending := 0
	for _, c := range changes {
		if c.Kind != "removed" {
			pending++
		}
	}
	if pending > 0 {
		fmt.Fprintln(out, "changed files are not loaded until re-trusted: `gem-agent trust --accept`, or the prompt at the next interactive start")
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

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// The file tools' reads run in a child of this binary, under a profile
// that denies the credential list at the kernel (ADR-0086). The child
// runs the ordinary registry code — the same os.Root confinement, the
// same caps, the same output — so nothing about a tool changes except
// which process performs its opens.
//
// One spawn per tool call: measured at 27 ms against a call that
// already costs a model round. A long-lived helper would save that and
// cost a protocol, a lifecycle and a restart path coupled to /clear.

// fileChildCommand is the hidden subcommand the parent re-executes. It
// is not a feature: it reads one JSON request on stdin, runs one tool,
// and writes the result to stdout.
const fileChildCommand = "__file-read"

// credentialExit is the exit code that means the kernel refused the
// open because the path is credential material. The parent maps it to
// tools.ErrCredentialRead; everything else is an ordinary tool error.
const credentialExit = 3

// fileChildRequest is what rides stdin. The paths are the parent's
// roots, so the child confines exactly as the parent would.
type fileChildRequest struct {
	ProjectDir string         `json:"project_dir"`
	WorkDir    string         `json:"work_dir"`
	Tool       string         `json:"tool"`
	Args       map[string]any `json:"args"`
}

func newFileChildCmd() *cobra.Command {
	return &cobra.Command{
		Use:    fileChildCommand,
		Short:  "internal: run one file-tool read inside the sandbox",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var req fileChildRequest
			in, more, err := bounded.ReadAll(cmd.InOrStdin(), 1<<20)
			if err != nil {
				return fmt.Errorf("file-read child: %w", err)
			}
			if more {
				return fmt.Errorf("file-read child: request larger than the cap")
			}
			if err := json.Unmarshal(in, &req); err != nil {
				return fmt.Errorf("file-read child: %w", err)
			}
			reg, err := tools.New(req.ProjectDir, nil, 0)
			if err != nil {
				return fmt.Errorf("file-read child: %w", err)
			}
			if req.WorkDir != "" {
				if err := reg.UseWorkDir(req.WorkDir); err != nil {
					return fmt.Errorf("file-read child: %w", err)
				}
			}
			tool, ok := reg.Get(req.Tool)
			if !ok {
				return fmt.Errorf("file-read child: unknown tool %q", req.Tool)
			}
			out, runErr := tool.Run(cmd.Context(), req.Args)
			if runErr != nil {
				fmt.Fprint(cmd.ErrOrStderr(), runErr.Error())
				if errors.Is(runErr, fs.ErrPermission) {
					os.Exit(credentialExit)
				}
				os.Exit(1)
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}
}

// fileChildRunner builds the runner the registry calls for a covered
// read. profileHome is the home the credential filters are anchored to.
func fileChildRunner(self, profileHome string, projectDir func() string, workDir func() string) tools.FileChildFunc {
	profile := sandbox.FileReadProfile(profileHome)
	return func(ctx context.Context, tool string, args map[string]any) (string, error) {
		req, err := json.Marshal(fileChildRequest{
			ProjectDir: projectDir(), WorkDir: workDir(), Tool: tool, Args: args,
		})
		if err != nil {
			return "", err
		}
		// Not sandbox.Wrap: that one is shell-shaped (`<shell> -c
		// <command>`), and what runs here is this binary with one
		// argument, never a command line anything has to quote.
		cmd := exec.CommandContext(ctx, sandbox.Executable, "-p", profile, self, fileChildCommand)
		cmd.Stdin = bytes.NewReader(req)
		stdout, stderr := bounded.NewWriter(tools.OutputCap+(1<<16)), bounded.NewWriter(1<<16)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		runErr := cmd.Run()
		outBytes, _ := stdout.Bytes()
		errBytes, _ := stderr.Bytes()
		if runErr == nil {
			return string(outBytes), nil
		}
		var ee *exec.ExitError
		if errors.As(runErr, &ee) && ee.ExitCode() == credentialExit {
			return "", tools.ErrCredentialRead
		}
		if msg := strings.TrimSpace(string(errBytes)); msg != "" {
			return "", errors.New(msg)
		}
		return "", runErr
	}
}

// selfPath is the binary to re-execute. A resolved path, because the
// child is spawned under a profile and a relative argv[0] would be
// resolved against a working directory the profile does not control.
func selfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

func init() { rootCmd.AddCommand(newFileChildCmd()) }

// installFileChild gives the registry its sandboxed reader, after
// proving on this machine that the cage refuses a credential-named
// file and reads an ordinary one (ADR-0086 §5). It returns the note
// the banner carries when it could not.
func installFileChild(reg *tools.Registry, home, probeDir string) string {
	self, err := selfPath()
	if err != nil {
		return fmt.Sprintf("file reads run in process: %v; the credential list is enforced by the matcher alone", err)
	}
	probe := func(profile, command string) error {
		c := exec.Command(sandbox.Executable, "-p", profile, "/bin/bash", "-c", command)
		return c.Run()
	}
	if err := sandbox.VerifyFileReadLane(probeDir, probe); err != nil {
		return fmt.Sprintf("file reads run in process: %v; the credential list is enforced by the matcher alone", err)
	}
	reg.SetFileChild(fileChildRunner(self, home, reg.ProjectDir, reg.WorkDir))
	return ""
}

// fileProbeDir is where VerifyFileReadLane writes its two probe files:
// the session work directory when there is one, a temporary directory
// otherwise. Empty means there is nowhere to probe, and the caller
// leaves the reads in process.
func fileProbeDir(workDir string) string {
	if workDir != "" {
		d := filepath.Join(workDir, "probe")
		if err := os.MkdirAll(d, 0o700); err == nil {
			return d
		}
	}
	d, err := os.MkdirTemp("", "gem-agent-file-probe-")
	if err != nil {
		return ""
	}
	return d
}

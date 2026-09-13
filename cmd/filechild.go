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
// One spawn per tool call, measured in the ADR against a call that
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

// childOutputCap bounds what the child may write back.
const childOutputCap = 1 << 20

// degradedNote is what the operator is told when the cage could not be
// proven on this machine. It says what is true now — the reads still
// ask about credential files, by this runtime's own check rather than
// the kernel's — and it does not invent an action, because there is
// none: a restart re-runs the same probe. Where the state is visible is
// the one useful pointer.
func degradedNote(err error) string {
	return fmt.Sprintf("file reads run in this process, not in a sandbox (%v). "+
		"Credential files still need your approval, checked by "+binaryName+" itself rather than the kernel. "+
		"Nothing to do now; /settings shows the state", err)
}

// binaryName is this runtime's own name, for the operator-facing note.
const binaryName = "gem-agent"

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
				// The parent keeps the previous work directory when a
				// rotation fails; a read of a PROJECT path must not die
				// with it, and the error carries an absolute path the
				// model must not see (ADR-0021, independent review).
				_ = reg.UseWorkDir(req.WorkDir)
			}
			if !tools.ChildTool(req.Tool) {
				// The hidden subcommand runs the covered reads and
				// nothing else: it is not a general tool runner
				// (independent review).
				return fmt.Errorf("%s: %q is not a read this runs", fileChildCommand, req.Tool)
			}
			tool, ok := reg.Get(req.Tool)
			if !ok {
				return fmt.Errorf("%s: unknown tool %q", fileChildCommand, req.Tool)
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
		// A child this runtime spawns, so the rule that governs the
		// others governs it (independent review: the release that wrote
		// "applied at every spawn site" added a site that was not).
		cmd.Env = sandbox.ChildEnv(os.Environ())
		cmd.Stdin = bytes.NewReader(req)
		// Above everything a covered read can legitimately emit — the
		// window cap, the document extractor's own limit and their
		// truncation notes — so this pipe never cuts a result the child
		// already sized. It is a ceiling on a runaway child, not a
		// second cap on the content (independent review: the first cut
		// sat at 85 KB and silently halved a 200 KB read).
		stdout, stderr := bounded.NewWriter(childOutputCap), bounded.NewWriter(1<<16)
		cmd.Stdout, cmd.Stderr = stdout, stderr
		runErr := cmd.Run()
		outBytes, more := stdout.Bytes()
		errBytes, _ := stderr.Bytes()
		if runErr == nil {
			out := string(outBytes)
			if more {
				// Never a silent cut: the child's own caps produce a
				// marked result, and anything past this pipe would be
				// a partial file presented as whole (independent
				// review).
				out += fmt.Sprintf("\n[output truncated at the sandbox boundary: %d bytes shown]", len(outBytes))
			}
			return out, nil
		}
		if ctx.Err() != nil {
			// The turn was cancelled and the child was killed mid-walk.
			// What it had written is a result, labelled, the way an
			// in-process walk labels its own cut.
			if len(outBytes) > 0 {
				return string(outBytes) + "\n[interrupted — the result above is partial]", nil
			}
			return "", ctx.Err()
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
// proving on this machine that the REAL child, under the profile the
// runner will use, refuses a credential-named file, reads an ordinary
// one, and can list a directory holding both. The first cut probed a
// shell instead and passed while every walk was returning "no matches"
// (independent review). It returns the note the operator gets when it
// could not.
func installFileChild(reg *tools.Registry, home, probeDir string) string {
	self, err := selfPath()
	if err != nil {
		return degradedNote(err)
	}
	return installFileChildWith(reg, self, home, probeDir)
}

// installFileChildWith is installFileChild with the binary named
// explicitly. It exists so a test can drive the probe against a built
// binary: under `go test`, os.Executable() is the test binary, and a
// probe that cannot run at all would look exactly like a probe the
// kernel refused.
func installFileChildWith(reg *tools.Registry, self, home, probeDir string) string {
	probe := func(profile, path string) error {
		// Project-relative, because the registry resolves its root and
		// an absolute probe path under /var reaches it as
		// /private/var: the lexical containment check then refuses a
		// file inside the very directory being probed.
		rel, relErr := filepath.Rel(probeDir, path)
		if relErr != nil {
			rel = path
		}
		req, err := json.Marshal(fileChildRequest{
			ProjectDir: probeDir, Tool: "read_file", Args: map[string]any{"path": rel},
		})
		if err != nil {
			return err
		}
		if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
			// search_files, not list_files: the listing that matters is
			// the one a CAGED walk performs, and list_files is not a
			// caged tool — asking the child for it is asking for a
			// refusal. The first cut did exactly that, so the probe
			// failed on every start and the cage was never installed.
			req, err = json.Marshal(fileChildRequest{
				ProjectDir: probeDir, Tool: "search_files",
				Args: map[string]any{"pattern": "probe", "path": rel},
			})
			if err != nil {
				return err
			}
		}
		c := exec.Command(sandbox.Executable, "-p", profile, self, fileChildCommand)
		c.Env = sandbox.ChildEnv(os.Environ())
		c.Stdin = bytes.NewReader(req)
		return c.Run()
	}
	if err := sandbox.VerifyFileReadLane(probeDir, home, probe); err != nil {
		return degradedNote(err)
	}
	reg.SetFileChild(fileChildRunner(self, home, reg.ProjectDir, reg.WorkDir))
	return ""
}

// fileProbeDir is where VerifyFileReadLane writes its two probe files:
// the session work directory when there is one, a temporary directory
// otherwise. Empty means there is nowhere to probe, and the caller
// leaves the reads in process.
func fileProbeDir(workDir string) (dir string, done func()) {
	if workDir != "" {
		d := filepath.Join(workDir, "probe")
		if err := os.MkdirAll(d, 0o700); err == nil {
			return d, func() { _ = os.Remove(d) }
		}
	}
	d, err := os.MkdirTemp("", "gem-agent-file-probe-")
	if err != nil {
		return "", func() {}
	}
	return d, func() { _ = os.Remove(d) }
}

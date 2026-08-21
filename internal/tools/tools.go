// Package tools implements gem-agent's built-in tools. All file paths are
// confined to the project directory — including through symlinks — and
// shell execution goes through an injected ExecFunc so the sandbox wrapper
// and tests can swap the execution strategy.
package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	// outputCap bounds any tool result fed back into the LLM context.
	// Unbounded tool output is the primary context-explosion failure
	// mode for agent loops.
	outputCap = 20_000
	// readCap bounds read_file content.
	readCap = 200 * 1024
	// listCap bounds list_files entries.
	listCap = 500
)

// Tool is one built-in tool: metadata for the LLM plus the implementation.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	// Mutating tools require MITL approval before each run.
	Mutating bool
	Run      func(ctx context.Context, args map[string]any) (string, error)
}

// ExecFunc builds the exec.Cmd for a shell command. The production
// implementation wraps the command in sandbox-exec; tests inject a direct
// runner.
type ExecFunc func(ctx context.Context, command string) *exec.Cmd

// Registry holds the built-in tools for one project directory.
type Registry struct {
	projectDir   string
	execFn       ExecFunc
	shellTimeout time.Duration
	tools        map[string]*Tool
	order        []string
}

// New creates the registry. projectDir must exist; it is resolved to a
// real absolute path so containment checks compare like with like.
func New(projectDir string, execFn ExecFunc, shellTimeout time.Duration) (*Registry, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("project directory: %w", err)
	}
	r := &Registry{
		projectDir:   real,
		execFn:       execFn,
		shellTimeout: shellTimeout,
		tools:        map[string]*Tool{},
	}
	for _, t := range []*Tool{r.listFiles(), r.listTree(), r.searchFiles(), r.readFile(), r.fileInfo(), r.viewImage(), r.readDocument(), r.dateTime(), r.writeFile(), r.editFile(), r.shellExec()} {
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	return r, nil
}

// ProjectDir returns the resolved project directory.
func (r *Registry) ProjectDir() string { return r.projectDir }

// Register adds an external tool (MCP). Name collisions are errors — a
// duplicate would silently shadow one implementation.
func (r *Registry) Register(t *Tool) error {
	if _, exists := r.tools[t.Name]; exists {
		return fmt.Errorf("tool %q already registered", t.Name)
	}
	r.tools[t.Name] = t
	r.order = append(r.order, t.Name)
	return nil
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List returns tools in registration order.
func (r *Registry) List() []*Tool {
	out := make([]*Tool, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.tools[n])
	}
	return out
}

// ViewImageName is the tool that attaches an in-project image to the
// conversation as visual input (ADR-0012). The agent special-cases it:
// a Gemini function response cannot carry an image part, so the tool
// result is metadata and the agent follows it with a user-role message
// carrying the actual image (loaded via ReadImage).
const ViewImageName = "view_image"

// imageExts gates ReadImage and read_file's refusal. The bytes are
// sniffed separately; the extension only routes.
var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
	".gif": true, ".heic": true, ".heif": true,
}

func isImageExt(p string) bool { return imageExts[strings.ToLower(filepath.Ext(p))] }

// maxImageBytes bounds one attached image. An oversized image is
// refused whole — a truncated PNG is not a smaller picture, it is a
// broken file.
const maxImageBytes = 8 * 1024 * 1024

// ReadImage loads an in-project image for attachment: same confinement
// as every other file tool, plus a content sniff so a renamed binary
// cannot masquerade as a picture.
func (r *Registry) ReadImage(p string) (data []byte, mime string, err error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		return nil, "", err
	}
	if !isImageExt(abs) {
		return nil, "", fmt.Errorf("%s does not look like an image file", p)
	}
	data, err = os.ReadFile(abs)
	if err != nil {
		return nil, "", fmt.Errorf("unreadable: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image is %d bytes; the limit is %d", len(data), maxImageBytes)
	}
	mime = http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("not an image (detected %s)", mime)
	}
	return data, mime, nil
}

func (r *Registry) viewImage() *Tool {
	return &Tool{
		Name: ViewImageName,
		Description: "Attach an image file from the project to the conversation as visual input " +
			"(screenshots fetched by tools, extracted images, diagrams). The image itself arrives " +
			"in the next message; this call returns confirmation. Read-only. " +
			"PNG, JPEG, WebP, GIF, HEIC.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "image path relative to the project root"},
			},
			"required": []string{"path"},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			data, mime, err := r.ReadImage(p)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("image attached: %s (%s, %d bytes) — it follows in the next message as visual input", p, mime, len(data)), nil
		},
	}
}

// intArg reads an integer tool argument (JSON numbers arrive as
// float64). 0 means absent — line numbers here are 1-based.
func intArg(args map[string]any, key string) int {
	if f, ok := args[key].(float64); ok && f > 0 {
		return int(f)
	}
	return 0
}

// sliceLines applies an optional 1-based inclusive line window (ADR-0014).
// A partial view must never masquerade as the whole file, so any window
// gets a trailing note in the established truncation style.
func sliceLines(content string, start, end int) (string, string, error) {
	if start == 0 && end == 0 {
		return content, "", nil
	}
	lines := strings.Split(content, "\n")
	// A newline-terminated file splits into a phantom empty final
	// element; counting it reported N one high and accepted a window on
	// a line that does not exist (ADR-0021).
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if start == 0 {
		start = 1
	}
	if start > total {
		return "", "", fmt.Errorf("start_line %d is beyond the end of the file (%d lines)", start, total)
	}
	if end == 0 || end > total {
		end = total
	}
	if end < start {
		return "", "", fmt.Errorf("end_line %d is before start_line %d", end, start)
	}
	note := fmt.Sprintf("\n[showing lines %d–%d of %d]", start, end, total)
	return strings.Join(lines[start-1:end], "\n"), note, nil
}

// --- path confinement ---

func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// resolveExisting resolves symlinks on the deepest existing ancestor of
// path and rejoins the non-existing remainder, so a not-yet-created file
// under a symlinked directory still gets containment-checked against the
// real location.
func resolveExisting(path string) (string, error) {
	var suffix []string
	cur := path
	for {
		if _, err := os.Lstat(cur); err == nil {
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		suffix = append([]string{filepath.Base(cur)}, suffix...)
		cur = parent
	}
	real, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, suffix...)...), nil
}

// resolvePath confines p (relative to the project dir, or absolute) to the
// project directory. String-level checks alone are insufficient — a
// symlink inside the project pointing outside would pass them — so the
// real path is checked too. OS-level containment for child processes is
// the sandbox's job (ADR-0001); this guards the built-in file tools.
func (r *Registry) resolvePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(r.projectDir, p)
	}
	abs = filepath.Clean(abs)
	if !within(r.projectDir, abs) {
		return "", fmt.Errorf("path escapes the project directory: %s", p)
	}
	real, err := resolveExisting(abs)
	if err != nil {
		// Deliberately not %w: the OS error names the path where
		// resolution stumbled, which for an escaping link chain lies
		// OUTSIDE the project — an error message must not leak
		// out-of-project path fragments to the model (ADR-0021).
		return "", fmt.Errorf("resolve %s: a link in the path is broken or its target is not accessible", p)
	}
	if !within(r.projectDir, real) {
		return "", fmt.Errorf("path escapes the project directory via symlink: %s", p)
	}
	return abs, nil
}

func strArg(args map[string]any, key string) (string, bool) {
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", limit, len(s))
}

// --- tools ---

func (r *Registry) listFiles() *Tool {
	return &Tool{
		Name: "list_files",
		Description: "List directory entries inside the project. Directories are " +
			"suffixed with '/'. Use this to explore the project structure before reading or editing.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Directory path relative to the project root. Omit or '.' for the project root.",
				},
			},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := strArg(args, "path")
			if p == "" {
				p = "."
			}
			dir, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return "", err
			}
			var names []string
			for _, e := range entries {
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				names = append(names, n)
			}
			sort.Strings(names)
			total := len(names)
			if total > listCap {
				names = names[:listCap]
				names = append(names, fmt.Sprintf("[%d more entries not shown]", total-listCap))
			}
			if len(names) == 0 {
				return "(empty directory)", nil
			}
			return strings.Join(names, "\n"), nil
		},
	}
}

func (r *Registry) readFile() *Tool {
	return &Tool{
		Name: "read_file",
		Description: "Read a file inside the project and return its content. " +
			"Pass start_line/end_line (1-based, inclusive) to read a window instead of the whole " +
			"file — pair with search_files results (path:line) and prefer windows for large files: " +
			"everything read here is replayed on every later round. Large reads are truncated.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the project root.",
				},
				"start_line": map[string]any{
					"type":        "integer",
					"description": "First line to read (1-based). Omit to read from the top.",
				},
				"end_line": map[string]any{
					"type":        "integer",
					"description": "Last line to read (inclusive). Omit to read to the end.",
				},
			},
			"required": []string{"path"},
		},
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, ok := strArg(args, "path")
			if !ok {
				return "", errors.New("path is required")
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return "", err
			}
			if isImageExt(p) {
				return "", fmt.Errorf("%s is an image — use the view_image tool to look at it (read_file would return unusable binary)", p)
			}
			content, note, err := sliceLines(string(data), intArg(args, "start_line"), intArg(args, "end_line"))
			if err != nil {
				return "", err
			}
			out := truncate(content, readCap)
			// The window note goes AFTER any truncation note, and the
			// content itself stays raw (no line-number prefixes): numbered
			// output would poison edit_file's exact-match contract the
			// moment the model copies what it read.
			return out + note, nil
		},
	}
}

func (r *Registry) writeFile() *Tool {
	return &Tool{
		Name: "write_file",
		Description: "Create or overwrite a file inside the project with the given content. " +
			"Parent directories are created as needed. For small changes to an existing file, prefer edit_file.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "File path relative to the project root.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The complete new file content (NOT a diff, NOT the user's request text).",
				},
			},
			"required": []string{"path", "content"},
		},
		Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			p, ok := strArg(args, "path")
			if !ok {
				return "", errors.New("path is required")
			}
			content, ok := strArg(args, "content")
			if !ok {
				return "", errors.New("content is required")
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(content), p), nil
		},
	}
}

func (r *Registry) shellExec() *Tool {
	return &Tool{
		Name: "shell_exec",
		Description: "Run a shell command (bash) with the project root as the working directory. " +
			"File writes are restricted to the project directory by the OS sandbox. " +
			"Output is truncated when large; the exit status is reported when non-zero.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to run.",
				},
			},
			"required": []string{"command"},
		},
		Mutating: true,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			command, ok := strArg(args, "command")
			if !ok || command == "" {
				return "", errors.New("command is required")
			}
			cctx, cancel := context.WithTimeout(ctx, r.shellTimeout)
			defer cancel()
			cmd := r.execFn(cctx, command)
			cmd.Dir = r.projectDir
			hardenExec(cmd)
			out, err := cmd.CombinedOutput()
			result := truncate(string(out), outputCap)
			if cctx.Err() == context.DeadlineExceeded {
				return result + fmt.Sprintf("\n[command timed out after %s]", r.shellTimeout), nil
			}
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					// Non-zero exit is a result the LLM must see, not a
					// tool failure: silently dropping the status turns
					// failed commands into false positives.
					return result + fmt.Sprintf("\n[exit status %d]", exitErr.ExitCode()), nil
				}
				return "", err
			}
			return result, nil
		},
	}
}

// hardenExec makes cancellation actually END a shell call (ADR-0034).
// exec.CommandContext kills only the DIRECT child; a grandchild (a
// skill's python under sandbox-exec/bash) survived holding the
// inherited output pipe, and CombinedOutput's Wait blocked until EOF —
// so both the timeout and the operator's Ctrl+C hung forever.
//   - Setpgid: the child leads a fresh process group;
//   - Cancel: SIGKILL the GROUP, so the whole tree dies and the pipes
//     close immediately;
//   - WaitDelay: the backstop for a setsid/double-fork escapee — Wait
//     stops waiting for inherited pipes instead of hanging the session
//     for an orphan. The kill is best-effort; the return is guaranteed.
func hardenExec(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

// Package tools implements gem-agent's built-in tools. All file paths are
// confined to the project directory — including through symlinks — and
// shell execution goes through an injected ExecFunc so the sandbox wrapper
// and tests can swap the execution strategy.
package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/nlink-jp/gem-agent/internal/ignore"
)

const (
	// OutputCap bounds any tool result fed back into the LLM context.
	// Unbounded tool output is the primary context-explosion failure
	// mode for agent loops.
	OutputCap = 20_000
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
	// WaitsOnOperator marks a tool whose Run blocks on the operator's
	// own input (ask_user). The ADR-0065 floor never abandons such a
	// call: a stdin read left behind would be a second reader on the
	// plain REPL's one stdin, eating the operator's next line. The
	// operator, not a filesystem, decides when it returns.
	WaitsOnOperator bool
	Run             func(ctx context.Context, args map[string]any) (string, error)
	// Annotate, when set, returns extra display lines for the approval
	// prompt derived from live filesystem state (ADR-0051) — e.g. what
	// an overwrite replaces. Display-only: it must not mutate anything,
	// and an empty return means nothing to add.
	Annotate func(args map[string]any) string
}

// ExecFunc builds the exec.Cmd for a shell command. The production
// implementation wraps the command in sandbox-exec; tests inject a direct
// runner.
type ExecFunc func(ctx context.Context, command string) *exec.Cmd

// Registry holds the built-in tools for one project directory.
type Registry struct {
	projectDir string
	// workDir is the session work directory (internal/workdir), empty
	// until one is set. It is a second root the file tools may read and
	// write, because everything the session puts outside the project
	// lands there: MCP results too large to hold in context, binary a
	// server returned, scratch a shell command produced. Without it the
	// model can see those paths and not open them, and it routes around
	// the built-ins with shell redirection — which is less reviewable,
	// not more contained.
	workDir string
	// projectRoot and workRoot are the roots as os.Root handles: the
	// file tools open through them, so the symlink check and the open
	// are one operation (review after v0.68.0: resolvePath checked the
	// resolved path and returned the lexical one, and a link swapped
	// between check and use escaped the roots). workRoot is nil until a
	// work directory is set.
	projectRoot *rootHandle
	workRoot    *rootHandle
	// rootsMu guards workDir and workRoot: /clear rotates them from
	// the UI goroutine while an abandoned call (ADR-0065) may still be
	// resolving or opening on its own goroutine. A reader acquires the
	// handle under the lock and releases it after its open; a rotated-
	// out handle closes when its last holder releases it (review after
	// v0.68.2 — leaving it open leaked a descriptor per /clear).
	rootsMu sync.RWMutex
	// parent is set on a Subset: the child reads the parent's roots,
	// so a work directory rotated after the child was built is the
	// child's too.
	parent       *Registry
	execFn       ExecFunc
	shellTimeout time.Duration
	tools        map[string]*Tool
	order        []string
	// abandoned counts tool calls the agent's floor gave up on that
	// have not returned yet (ADR-0065 §2). It lives on the registry,
	// not the agent, so a delegated child (Subset) shares the parent's
	// counter: the goroutine holding the syscall is the child's, and
	// the exit receipt is the parent's.
	abandoned *atomic.Int64
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
	projectRoot, err := os.OpenRoot(real)
	if err != nil {
		return nil, err
	}
	r := &Registry{
		projectDir:   real,
		projectRoot:  &rootHandle{root: projectRoot},
		execFn:       execFn,
		shellTimeout: shellTimeout,
		tools:        map[string]*Tool{},
		abandoned:    new(atomic.Int64),
	}
	for _, t := range []*Tool{r.listFiles(), r.listTree(), r.searchFiles(), r.readFile(), r.fileInfo(), r.viewImage(), r.readDocument(), r.dateTime(), r.writeFile(), r.editFile(), r.shellExec()} {
		r.tools[t.Name] = t
		r.order = append(r.order, t.Name)
	}
	return r, nil
}

// ProjectDir returns the resolved project directory.
func (r *Registry) ProjectDir() string { return r.projectDir }

// WorkDir returns the session work directory, or "" when none is set.
func (r *Registry) WorkDir() string { return r.rootState().workDir }

// rootHandle is an os.Root with a holder count: acquire under
// rootsMu, release after the open. retire (under rootsMu, when the
// handle leaves the registry) closes it once no holder remains —
// retire and release cannot both close, since a holder present at
// retire time makes retire defer to that holder's release.
type rootHandle struct {
	root    *os.Root
	refs    atomic.Int64
	retired atomic.Bool
	closed  atomic.Bool
}

func (h *rootHandle) acquire() { h.refs.Add(1) }

func (h *rootHandle) release() {
	if h.refs.Add(-1) == 0 && h.retired.Load() {
		h.close()
	}
}

func (h *rootHandle) retire() {
	h.retired.Store(true)
	if h.refs.Load() == 0 {
		h.close()
	}
}

func (h *rootHandle) close() {
	if h.closed.CompareAndSwap(false, true) {
		_ = h.root.Close()
	}
}

// rootState is the confinement roots as one consistent snapshot.
type rootState struct {
	projectDir  string
	projectRoot *rootHandle
	workDir     string
	workRoot    *rootHandle
}

func (r *Registry) rootState() rootState {
	if r.parent != nil {
		return r.parent.rootState()
	}
	r.rootsMu.RLock()
	defer r.rootsMu.RUnlock()
	return rootState{r.projectDir, r.projectRoot, r.workDir, r.workRoot}
}

// UseWorkDir adds dir as a second root for the file tools. It is
// resolved the same way the project is (absolute, symlinks evaluated),
// so containment compares like with like.
//
// An empty dir removes the second root: /clear (ADR-0071 §2) may end
// up with no work directory where the previous session had one.
func (r *Registry) UseWorkDir(dir string) error {
	if r.parent != nil {
		return r.parent.UseWorkDir(dir)
	}
	if dir == "" {
		r.rootsMu.Lock()
		old := r.workRoot
		r.workDir, r.workRoot = "", nil
		if old != nil {
			old.retire()
		}
		r.rootsMu.Unlock()
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return fmt.Errorf("work directory: %w", err)
	}
	root, err := os.OpenRoot(real)
	if err != nil {
		return fmt.Errorf("work directory: %w", err)
	}
	// The previous handle retires: it closes when its last holder —
	// an abandoned call mid-open — releases it (see rootHandle).
	r.rootsMu.Lock()
	old := r.workRoot
	r.workDir, r.workRoot = real, &rootHandle{root: root}
	if old != nil {
		old.retire()
	}
	r.rootsMu.Unlock()
	return nil
}

// rootFor returns the os.Root that contains abs (a path resolvePath
// accepted), abs relative to it, and the release the caller owes once
// its open is done. The handle is acquired under the same lock that
// rotates it, so a retire cannot slip between the choice and the use.
func (r *Registry) rootFor(abs string) (*os.Root, string, func(), error) {
	if r.parent != nil {
		return r.parent.rootFor(abs)
	}
	r.rootsMu.RLock()
	defer r.rootsMu.RUnlock()
	for _, c := range []struct {
		dir string
		h   *rootHandle
	}{{r.projectDir, r.projectRoot}, {r.workDir, r.workRoot}} {
		if c.dir == "" || c.h == nil || !within(c.dir, abs) {
			continue
		}
		rel, err := filepath.Rel(c.dir, abs)
		if err != nil {
			return nil, "", nil, err
		}
		c.h.acquire()
		return c.h.root, rel, c.h.release, nil
	}
	return nil, "", nil, fmt.Errorf("path escapes the project directory: %s", abs)
}

// openRead opens abs for reading through its root. os.Root resolves
// every path component inside the root and refuses one that leads
// out, at open time — the containment holds however the tree changes
// between resolvePath's check and this call. The returned file has
// its own descriptor: the root may close after it.
func (r *Registry) openRead(abs string) (*os.File, error) {
	root, rel, release, err := r.rootFor(abs)
	if err != nil {
		return nil, err
	}
	defer release()
	return root.Open(rel)
}

// openWrite opens abs for writing (create, truncate) through its root,
// creating missing parent directories inside the root.
func (r *Registry) openWrite(abs string, perm os.FileMode) (*os.File, error) {
	root, rel, release, err := r.rootFor(abs)
	if err != nil {
		return nil, err
	}
	defer release()
	if dir := filepath.Dir(rel); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	return root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
}

// statIn stats abs through its root.
func (r *Registry) statIn(abs string) (os.FileInfo, error) {
	root, rel, release, err := r.rootFor(abs)
	if err != nil {
		return nil, err
	}
	defer release()
	return root.Stat(rel)
}

// lstatIn lstats abs through its root.
func (r *Registry) lstatIn(abs string) (os.FileInfo, error) {
	root, rel, release, err := r.rootFor(abs)
	if err != nil {
		return nil, err
	}
	defer release()
	return root.Lstat(rel)
}

// readlinkIn reads the link at abs through its root.
func (r *Registry) readlinkIn(abs string) (string, error) {
	root, rel, release, err := r.rootFor(abs)
	if err != nil {
		return "", err
	}
	defer release()
	return root.Readlink(rel)
}

// gitignoreReader is the ignore package's FileReader through the
// roots: a regular file within cap, opened and read through its root,
// so a .gitignore or a directory above it swapped for an escaping
// link is refused at the open (review after v0.68.2).
func (r *Registry) gitignoreReader(path string, cap int64) ([]byte, error) {
	info, err := r.lstatIn(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > cap {
		return nil, fmt.Errorf("%s: not a regular file within %d bytes", path, cap)
	}
	data, more, err := r.readFileCapped(path, int(cap))
	if err != nil {
		return nil, err
	}
	if more {
		return nil, fmt.Errorf("%s: larger than %d bytes", path, cap)
	}
	return data, nil
}

// readForSearch reads a file for search_files: whole, within
// searchFileCap, text. A file that outgrew the cap between the
// listing and the read is not searched at all — a match past the cap
// would be missing from a result presented as complete (review after
// v0.68.2).
func (r *Registry) readForSearch(abs string) ([]byte, bool) {
	data, more, err := r.readFileCapped(abs, searchFileCap)
	if err != nil || more {
		return nil, false
	}
	if bytes.IndexByte(data[:min(len(data), binarySniff)], 0) >= 0 {
		return nil, false // binary
	}
	return data, true
}

// readDirIn lists the directory at abs through its root: a directory
// swapped for a link that leads out between the walk's check and this
// call is refused at the open (review after v0.68.1 — the walks used
// os.ReadDir on the lexical path). At most DirEntryCap entries are
// returned; more reports that the directory had more (ADR-0072 §4.5 —
// ReadDir(-1) allocated every entry before any cap).
func (r *Registry) readDirIn(abs string) (entries []os.DirEntry, more bool, err error) {
	f, err := r.openRead(abs)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	entries, err = f.ReadDir(DirEntryCap + 1)
	if err != nil && err != io.EOF {
		return nil, false, err
	}
	if len(entries) > DirEntryCap {
		return entries[:DirEntryCap], true, nil
	}
	return entries, false, nil
}

// DirEntryCap bounds one directory listing the tools hold in memory.
const DirEntryCap = 10000

// readFileCapped reads at most cap bytes of abs through its root.
func (r *Registry) readFileCapped(abs string, cap int) ([]byte, bool, error) {
	f, err := r.openRead(abs)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	return readAllCapped(f, cap)
}

// readAllCapped reads at most cap bytes from f, reporting whether
// more followed — the file is never held whole (review after v0.68.0:
// os.ReadFile of a huge or sparse file could exhaust memory before the
// output cap applied).
func readAllCapped(f io.Reader, cap int) (data []byte, more bool, err error) {
	data, err = io.ReadAll(io.LimitReader(f, int64(cap)+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > cap {
		return data[:cap], true, nil
	}
	return data, false, nil
}

// roots returns the directories the file tools may touch.
func (r *Registry) roots() []string {
	st := r.rootState()
	if st.workDir == "" {
		return []string{st.projectDir}
	}
	return []string{st.projectDir, st.workDir}
}

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

// Subset returns a registry exposing only the named tools, in the given
// order, sharing this registry's project confinement (the tool closures
// keep resolving paths against the same project directory). Unknown
// names are errors: a security-relevant allowlist that silently drops a
// typo would hide exactly the mistake it exists to prevent (ADR-0037).
func (r *Registry) Subset(names ...string) (*Registry, error) {
	sub := &Registry{
		projectDir:   r.projectDir,
		parent:       r, // roots are the parent's, rotation included
		execFn:       r.execFn,
		shellTimeout: r.shellTimeout,
		tools:        map[string]*Tool{},
		abandoned:    r.abandoned, // shared: the child's abandoned calls are the session's
	}
	for _, n := range names {
		t, ok := r.tools[n]
		if !ok {
			return nil, fmt.Errorf("subset: unknown tool %q", n)
		}
		sub.tools[n] = t
		sub.order = append(sub.order, n)
	}
	return sub, nil
}

// NoteAbandoned adjusts the count of abandoned calls still running
// (+1 when the floor gives up on a call, -1 when it finally returns).
func (r *Registry) NoteAbandoned(delta int64) { r.abandoned.Add(delta) }

// AbandonedRunning reports abandoned calls that have not returned yet,
// across this registry and every Subset sharing it.
func (r *Registry) AbandonedRunning() int { return int(r.abandoned.Load()) }

// RemoveByPrefix deletes every tool whose name starts with prefix and
// returns how many were removed — the MCP half of an integration
// reload (ADR-0039): all mcp__* adapters go before the connect path
// re-registers the fresh set.
func (r *Registry) RemoveByPrefix(prefix string) int {
	removed := 0
	kept := r.order[:0]
	for _, n := range r.order {
		if strings.HasPrefix(n, prefix) {
			delete(r.tools, n)
			removed++
			continue
		}
		kept = append(kept, n)
	}
	r.order = kept
	return removed
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

// ShellExecName is the one tool whose effect is a whole command line
// rather than a named argument, which is why several layers treat it
// specially — the approval detail, and the per-command policy and
// learning of ADR-0045.
const ShellExecName = "shell_exec"

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
	f, err := r.openRead(abs)
	if err != nil {
		return nil, "", fmt.Errorf("unreadable: %w", err)
	}
	defer func() { _ = f.Close() }()
	// The size gate runs before the read, so an oversized (or sparse)
	// file is refused without being held in memory.
	if st, err := f.Stat(); err == nil && st.Size() > maxImageBytes {
		return nil, "", fmt.Errorf("image is %d bytes; the limit is %d", st.Size(), maxImageBytes)
	}
	data, more, err := readAllCapped(f, maxImageBytes)
	if err != nil {
		return nil, "", fmt.Errorf("unreadable: %w", err)
	}
	if more {
		return nil, "", fmt.Errorf("image exceeds the %d byte limit", maxImageBytes)
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

// readWindow is sliceLines over a stream: lines are read one at a
// time, only the requested window is kept, and no single line is held
// beyond cap bytes (a sparse file is one enormous line). The total
// line count in the note still needs the whole stream walked, which
// is bounded in memory, not in time — ctx is consulted as it goes.
func readWindow(ctx context.Context, f io.Reader, start, end, cap int) (string, string, error) {
	content, note, cutLines, err := readWindowLines(ctx, f, start, end, cap)
	if err != nil {
		return "", "", err
	}
	if cutLines > 0 {
		// A line longer than the cap is shown cut; the reader is told
		// (ADR-0072 §4.5 — the cut was silent).
		note += fmt.Sprintf("\n[%d line(s) longer than %d bytes were cut]", cutLines, cap)
	}
	return content, note, nil
}

func readWindowLines(ctx context.Context, f io.Reader, start, end, cap int) (string, string, int, error) {
	if start == 0 && end == 0 {
		// One byte past the cap, so the caller's truncate sees the
		// overflow and marks it.
		data, err := io.ReadAll(io.LimitReader(f, int64(cap)+1))
		if err != nil {
			return "", "", 0, err
		}
		return string(data), "", 0, nil
	}
	if start == 0 {
		start = 1
	}
	if end != 0 && end < start {
		return "", "", 0, fmt.Errorf("end_line %d is before start_line %d", end, start)
	}
	br := bufio.NewReaderSize(f, 64*1024)
	var kept []string
	keptBytes := 0
	total := 0
	cutLines := 0
	for {
		if total%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return "", "", 0, err
			}
		}
		line, cut, err := readLineCapped(br, cap)
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", 0, err
		}
		total++
		if total >= start && (end == 0 || total <= end) && keptBytes <= cap {
			kept = append(kept, line)
			keptBytes += len(line) + 1
			if cut {
				cutLines++
			}
		}
	}
	if start > total {
		return "", "", 0, fmt.Errorf("start_line %d is beyond the end of the file (%d lines)", start, total)
	}
	if end == 0 || end > total {
		end = total
	}
	note := fmt.Sprintf("\n[showing lines %d–%d of %d]", start, end, total)
	return strings.Join(kept, "\n"), note, cutLines, nil
}

// readLineCapped returns the next line without its newline, keeping
// at most cap bytes of it and discarding the rest — cut reports that a
// cut happened; io.EOF when no line remains (a file's trailing newline
// does not start a phantom line — the sliceLines rule).
func readLineCapped(br *bufio.Reader, cap int) (line string, cut bool, err error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		if len(buf) < cap {
			take := chunk
			if len(buf)+len(take) > cap {
				take = take[:cap-len(buf)]
				cut = true
			}
			buf = append(buf, take...)
		} else if len(strings.TrimSuffix(string(chunk), "\n")) > 0 {
			cut = true
		}
		switch err {
		case nil:
			return strings.TrimSuffix(string(buf), "\n"), cut, nil
		case bufio.ErrBufferFull:
			continue
		case io.EOF:
			if len(chunk) == 0 && len(buf) == 0 {
				return "", false, io.EOF
			}
			return string(buf), cut, nil
		default:
			return "", false, err
		}
	}
}

// --- path confinement ---

func within(base, p string) bool {
	return p == base || strings.HasPrefix(p, base+string(filepath.Separator))
}

// withinAny reports whether p sits under any of the roots.
func withinAny(roots []string, p string) bool {
	for _, base := range roots {
		if within(base, p) {
			return true
		}
	}
	return false
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

// resolvePath confines p to the registry's roots: the project directory
// and, when set, the session work directory. A relative path is always
// relative to the PROJECT — the work directory is reached by the
// absolute path the session prompt names, so an unqualified "report.md"
// keeps meaning the project file it has always meant.
//
// String-level checks alone are insufficient — a symlink inside a root
// pointing outside would pass them — so the real path is checked too.
// OS-level containment for child processes is the sandbox's job
// (ADR-0001); this guards the built-in file tools.
func (r *Registry) resolvePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	abs := p
	if !filepath.IsAbs(p) {
		abs = filepath.Join(r.projectDir, p)
	}
	abs = filepath.Clean(abs)
	if !withinAny(r.roots(), abs) {
		return "", fmt.Errorf("path escapes the project directory: %s", p)
	}
	real, err := resolveExisting(abs)
	if err != nil {
		// Deliberately not %w: the OS error names the path where
		// resolution stumbled, which for an escaping link chain lies
		// OUTSIDE the roots — an error message must not leak
		// out-of-project path fragments to the model (ADR-0021).
		return "", fmt.Errorf("resolve %s: a link in the path is broken or its target is not accessible", p)
	}
	if !withinAny(r.roots(), real) {
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
	cut := cutRunes(s, limit)
	return cut + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", len(cut), len(s))
}

// boundedOutput is an io.Writer that keeps the first limit bytes and
// counts the rest, so a process may print without end and the tool
// holds one cap's worth. String renders what was kept with the
// truncation note the whole-output path would have produced.
type boundedOutput struct {
	mu    sync.Mutex
	buf   []byte
	limit int
	total int64
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += int64(n)
	if room := b.limit + 1 - len(b.buf); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf = append(b.buf, p...)
	}
	return n, nil // the writer accepts everything; it keeps a cap's worth
}

func (b *boundedOutput) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) <= b.limit {
		return string(b.buf)
	}
	// The whole kept buffer (limit+1 bytes) goes to cutRunes, which
	// lands the cut on a rune boundary at or before the limit; slicing
	// first split a character and left cutRunes nothing to fix.
	cut := cutRunes(string(b.buf), b.limit)
	return cut + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", len(cut), b.total)
}

// cutRunes truncates s to at most n bytes without splitting a UTF-8
// sequence (review after v0.68.2: a byte cut through a Japanese
// character left a broken tail in what the model was sent).
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// --- tools ---

func (r *Registry) listFiles() *Tool {
	return &Tool{
		Name: "list_files",
		Description: "List directory entries inside the project. Directories are " +
			"suffixed with '/'; dependency/build directories and .gitignore'd entries are " +
			"marked [ignored] — prefer not to descend into those. Use this to explore the " +
			"project structure before reading or editing.",
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
			entries, more, err := r.readDirIn(dir)
			if err != nil {
				return "", err
			}
			rules := ignore.RootWith(r.projectDir, dir, false, r.gitignoreReader)
			var names []string
			for _, e := range entries {
				n := e.Name()
				if e.IsDir() {
					n += "/"
				}
				// Annotation only — the entry is still listed. A
				// non-recursive listing is not the enumeration cost
				// ADR-0052 removes; the marker just teaches the model
				// not to descend before it tries.
				if rules.Ignored(e.Name(), e.IsDir()) {
					n += " [ignored]"
				}
				names = append(names, n)
			}
			sort.Strings(names)
			total := len(names)
			if total > listCap {
				names = names[:listCap]
				names = append(names, fmt.Sprintf("[%d more entries not shown]", total-listCap))
			}
			if more {
				names = append(names, fmt.Sprintf("[the directory has more than %d entries — the listing stopped there]", DirEntryCap))
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
			if isImageExt(p) {
				return "", fmt.Errorf("%s is an image — use the view_image tool to look at it (read_file would return unusable binary)", p)
			}
			f, err := r.openRead(abs)
			if err != nil {
				return "", err
			}
			defer func() { _ = f.Close() }()
			// Streamed, never held whole (review after v0.68.0): the
			// window and the cap apply as the file is read, so a huge or
			// sparse file costs bounded memory whatever its size.
			content, note, err := readWindow(ctx, f, intArg(args, "start_line"), intArg(args, "end_line"), readCap)
			if err != nil {
				return "", err
			}
			out := content
			if len(content) > readCap {
				// The marker names the file's real size, which the
				// streamed read never held.
				total := int64(len(content))
				if st, err := f.Stat(); err == nil && st.Size() > total {
					total = st.Size()
				}
				cut := cutRunes(content, readCap)
				out = cut + fmt.Sprintf("\n[output truncated: %d of %d bytes shown]", len(cut), total)
			}
			// The window note goes AFTER any truncation note, and the
			// content itself stays raw (no line-number prefixes): numbered
			// output would poison edit_file's exact-match contract the
			// moment the model copies what it read.
			return out + note, nil
		},
	}
}

const (
	// shrinkGuardMinBytes: existing files smaller than this may be
	// overwritten freely — a small diff is cheap to review, and tiny
	// files hit high shrink ratios with legitimate edits (ADR-0051).
	shrinkGuardMinBytes = 2048
	// shrinkGuardPct: overwriting an existing file with content below
	// this percentage of its current size is refused without an
	// explicit allow_shrink — a whole-file rewrite that shrinks is the
	// signature of a regeneration that summarized away content.
	shrinkGuardPct = 70
)

func (r *Registry) writeFile() *Tool {
	return &Tool{
		Name: "write_file",
		Description: "Create a new file inside the project with the given content, or deliberately replace " +
			"an existing one whole. Parent directories are created as needed. For changes to an existing " +
			"file — even large revisions — prefer edit_file: write_file replaces the WHOLE file, and " +
			"everything not reproduced verbatim in content is destroyed. An overwrite that shrinks an " +
			"existing file substantially is refused unless allow_shrink is true.",
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
				"allow_shrink": map[string]any{
					"type": "boolean",
					"description": "Set true only when replacing an existing file with much smaller content " +
						"is intentional. Without it a substantial shrink is refused, so a partial rewrite " +
						"cannot silently destroy the rest of the file.",
				},
			},
			"required": []string{"path", "content"},
		},
		Mutating: true,
		Annotate: func(args map[string]any) string {
			p, ok := strArg(args, "path")
			if !ok {
				return ""
			}
			content, ok := strArg(args, "content")
			if !ok {
				return ""
			}
			abs, err := r.resolvePath(p)
			if err != nil {
				return ""
			}
			info, err := r.statIn(abs)
			if err != nil || info.IsDir() {
				return ""
			}
			return fmt.Sprintf("replaces existing file: %s → %s", sizeLabel(info.Size()), sizeLabel(int64(len(content))))
		},
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
			allowShrink, _ := args["allow_shrink"].(bool)
			if info, err := r.statIn(abs); err == nil && !info.IsDir() &&
				info.Size() >= shrinkGuardMinBytes && !allowShrink &&
				int64(len(content))*100 < info.Size()*shrinkGuardPct {
				return "", fmt.Errorf("refusing to replace %s (%s) with much smaller content (%s): "+
					"a whole-file rewrite destroys everything not reproduced verbatim. Use edit_file "+
					"for targeted changes, or re-read the file and pass allow_shrink=true if this "+
					"shrink is intentional (file unchanged)",
					p, sizeLabel(info.Size()), sizeLabel(int64(len(content))))
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			f, err := r.openWrite(abs, 0o644)
			if err != nil {
				return "", err
			}
			if _, err := f.Write([]byte(content)); err != nil {
				_ = f.Close()
				return "", err
			}
			if err := f.Close(); err != nil {
				return "", err
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(content), p), nil
		},
	}
}

// sizeLabel renders a byte count for guard errors and annotations.
func sizeLabel(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%dKB", n/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

func (r *Registry) shellExec() *Tool {
	return &Tool{
		Name: ShellExecName,
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
			// The output is bounded as it arrives (ADR-0072 §4.5):
			// CombinedOutput held everything until exit, so a command
			// printing without end exhausted memory before the cap ran.
			out := &boundedOutput{limit: OutputCap}
			cmd.Stdout, cmd.Stderr = out, out
			err := cmd.Run()
			result := out.String()
			if cctx.Err() == context.DeadlineExceeded {
				return result + fmt.Sprintf("\n[command timed out after %s]", r.shellTimeout), nil
			}
			// The command exited but a child it left behind still held
			// the output pipe past WaitDelay (a `… &` in a start
			// script). Go reports that as an error; the output before
			// the cut is the result, and the model is told where it
			// was cut (ADR-0065 §2 review: the shorter WaitDelay must
			// not turn such commands into failures).
			if errors.Is(err, exec.ErrWaitDelay) {
				return result + fmt.Sprintf("\n[a background child still held the output pipe %s after the command exited — later output is not captured]", ShellWaitDelay), nil
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
//     It is shorter than the agent's abandon grace (ADR-0065 §2) on
//     purpose: the output produced before the cut must reach the
//     model, not be discarded by the floor a moment before Wait
//     returns it.
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
	cmd.WaitDelay = ShellWaitDelay
}

// ShellWaitDelay bounds how long a cancelled shell call waits for an
// escapee's inherited pipes (ADR-0034 §2). The agent's abandon grace
// must stay longer than this (pinned by a test in internal/agent).
const ShellWaitDelay = 500 * time.Millisecond

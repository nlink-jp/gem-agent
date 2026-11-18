package cmd

// Characterization of the whole startup (ADR-0124 §2.6): the session is
// run in a child process — the test binary itself, re-entered through
// TestMain — against a scripted model, and everything it leaves behind is
// compared byte for byte with a reference recorded before the
// restructure: stdout, stderr, the exit status, the transcripts, and the
// system prompt, tool declarations and messages the model received. A
// child process because the session writes the process environment,
// reads os.Stdin's terminal state and binds package-level flags; one run
// per process keeps each run what a real start is.
//
// Run with -update to rewrite the references. Do that only when a change
// is MEANT to alter what a session does — never inside a refactor step.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

var updateCharacterization = flag.Bool("update", false, "rewrite the characterization references")

const (
	charChildEnv  = "GEMAGENT_CHARACTERIZE_CHILD"
	charScriptEnv = "GEMAGENT_CHARACTERIZE_SCRIPT"
	charRecordEnv = "GEMAGENT_CHARACTERIZE_RECORD"
	charArgsEnv   = "GEMAGENT_CHARACTERIZE_ARGS"
	charExitEnv   = "GEMAGENT_CHARACTERIZE_EXIT"
	charVersion   = "v0.0.0-characterize"
)

func TestMain(m *testing.M) {
	if os.Getenv(charChildEnv) != "" {
		os.Exit(runCharacterizeChild())
	}
	os.Exit(m.Run())
}

// runCharacterizeChild is the session under test: the root command with
// the scripted model in place of Vertex, nothing else replaced.
func runCharacterizeChild() int {
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv(charArgsEnv)), &args); err != nil {
		fmt.Fprintln(os.Stderr, "characterize: bad args:", err)
		return 2
	}
	script, err := loadScript(os.Getenv(charScriptEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "characterize:", err)
		return 2
	}
	rec := &scriptRecorder{path: os.Getenv(charRecordEnv)}
	newModelClient = func(_ context.Context, cfg *config.Config) (modelClient, error) {
		return &scriptedModel{script: script, rec: rec, model: cfg.Model.Name}, nil
	}
	if path := os.Getenv(charExitEnv); path != "" {
		cleanupTrace = func(name string) {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				panic(err)
			}
			defer func() { _ = f.Close() }()
			if _, err := f.WriteString(name + "\n"); err != nil {
				panic(err)
			}
		}
	}
	appVersion = charVersion
	rootCmd.Version = charVersion
	rootCmd.SetArgs(args)
	if err := rootCmd.Execute(); err != nil {
		return 1
	}
	return 0
}

// scriptTurn is one answer of the scripted model.
type scriptTurn struct {
	Text  string         `json:"text"`
	Tool  string         `json:"tool,omitempty"`
	Args  map[string]any `json:"args,omitempty"`
	Usage int            `json:"usage,omitempty"`
}

type script struct {
	mu    sync.Mutex
	turns []scriptTurn
}

func loadScript(path string) (*script, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var turns []scriptTurn
	if err := json.Unmarshal(raw, &turns); err != nil {
		return nil, err
	}
	return &script{turns: turns}, nil
}

// next pops the next answer. Past the end of the script the model says so
// in plain text: a call the script did not expect shows up in the
// recorded output instead of failing somewhere less visible.
func (s *script) next() scriptTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.turns) == 0 {
		return scriptTurn{Text: "(script exhausted)"}
	}
	t := s.turns[0]
	s.turns = s.turns[1:]
	return t
}

// scriptRecorder appends one JSON line per model call: what the model was
// given, which is part of what a session does.
type scriptRecorder struct {
	mu   sync.Mutex
	path string
}

type recordedCall struct {
	Model    string        `json:"model"`
	Thinking string        `json:"thinking"`
	System   string        `json:"system"`
	Tools    []llm.ToolDef `json:"tools"`
	Messages []llm.Message `json:"messages"`
}

func (r *scriptRecorder) add(c recordedCall) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		panic(err)
	}
}

type scriptedModel struct {
	script   *script
	rec      *scriptRecorder
	model    string
	thinking string
}

func (m *scriptedModel) ChatStream(_ context.Context, system string, messages []llm.Message, tools []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	m.rec.add(recordedCall{Model: m.model, Thinking: m.thinking, System: system, Tools: tools, Messages: messages})
	t := m.script.next()
	resp := &llm.Response{Content: t.Text, FinishReason: "STOP"}
	if t.Tool != "" {
		resp.ToolCalls = []llm.ToolCall{{ID: "call-1", Name: t.Tool, Args: t.Args}}
	}
	if t.Text != "" && onText != nil {
		onText(t.Text)
	}
	u := t.Usage
	if u == 0 {
		u = 100
	}
	resp.PromptTokens, resp.OutputTokens, resp.TotalTokens = u, 10, u+10
	return resp, nil
}

func (m *scriptedModel) SearchWeb(context.Context, string) (string, []llm.WebSource, llm.Usage, error) {
	return "", nil, llm.Usage{}, errors.New("web search is not scripted")
}

func (m *scriptedModel) FetchURL(context.Context, string) (string, string, llm.Usage, error) {
	return "", "", llm.Usage{}, errors.New("fetch is not scripted")
}

func (m *scriptedModel) Thinking() string         { return m.thinking }
func (m *scriptedModel) SetThinking(level string) { m.thinking = level }

func (m *scriptedModel) WithModel(name string) modelClient {
	return &scriptedModel{script: m.script, rec: m.rec, model: name, thinking: m.thinking}
}

func (m *scriptedModel) WithThinking(level, _ string) modelClient {
	return &scriptedModel{script: m.script, rec: m.rec, model: m.model, thinking: level}
}

func (m *scriptedModel) SetObserver(func(llm.StreamEvent))     {}
func (m *scriptedModel) SetRetryRecorder(func(llm.RetryEvent)) {}

// ContextWindow answers as Vertex's publisher metadata does (no limit),
// so the session takes its documented estimate.
func (m *scriptedModel) ContextWindow(context.Context) (int, error) { return 0, nil }

// charEnv is a fresh machine for the child: its own HOME and state root,
// and nothing inherited from a gem-agent this test may itself run under.
type charEnv struct {
	root, home, project, config string
}

func newCharEnv(t *testing.T) charEnv {
	t.Helper()
	// EvalSymlinks: /var is a link to /private/var on macOS, and the
	// session resolves its project directory.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := charEnv{
		root:    root,
		home:    filepath.Join(root, "home"),
		project: filepath.Join(root, "project"),
		config:  filepath.Join(root, "home", ".config", "gem-agent", "config.toml"),
	}
	files := map[string]string{
		filepath.Join(e.project, "AGENTS.md"):                             "# Project rules\n\nAnswer briefly.\n",
		filepath.Join(e.project, "notes.txt"):                             "first line\nsecond line\n",
		filepath.Join(e.project, ".mcp.json"):                             "{\"mcpServers\": {}}\n",
		filepath.Join(e.project, ".claude", "skills", "demo", "SKILL.md"): "---\nname: demo\ndescription: A demonstration skill.\n---\n\nSay hello.\n",
		e.config: fmt.Sprintf(`[gcp]
project = "characterize-project"
location = "global"

[model]
name = "scripted-main"

[sandbox]
enabled = false

[approval]
trusted_projects = [%q]

[tui]
language = "en"
`, e.project),
	}
	for p, body := range files {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The directory is trusted by the config; its content is pinned the
	// way an operator pins it, so the session loads the instruction file,
	// the skill and .mcp.json — the paths a characterization must cover.
	if r := runCharacterize(t, e, nil, nil, "trust", "--accept"); r.status != 0 {
		t.Fatalf("trust --accept: status %d\n%s%s", r.status, r.stdout, r.stderr)
	}
	return e
}

type charResult struct {
	stdout, stderr string
	status         int
	calls          string
	transcripts    string
	// exit is the teardown as it ran, one step name per line. Kept out of
	// render: the references predate the names, and the order is checked
	// against ADR-0124 §2.5 directly.
	exit []string
}

func runCharacterize(t *testing.T, e charEnv, turns []scriptTurn, stdin []string, args ...string) charResult {
	t.Helper()
	scriptPath := filepath.Join(e.root, "script.json")
	recordPath := filepath.Join(e.root, "calls.jsonl")
	exitPath := filepath.Join(e.root, "exit.txt")
	_ = os.Remove(exitPath)
	raw, err := json.Marshal(turns)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	argv, err := json.Marshal(append([]string{"--config", e.config}, args...))
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Dir = e.project
	env := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "GEMAGENT_") || k == "HOME" || k == "LANG" || k == "LC_ALL" {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"HOME="+e.home,
		"LANG=C",
		charChildEnv+"=1",
		charScriptEnv+"="+scriptPath,
		charRecordEnv+"="+recordPath,
		charExitEnv+"="+exitPath,
		charArgsEnv+"="+string(argv),
	)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	stderr := &promptWriter{prompts: make(chan struct{}, 64)}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// One line per prompt: the plain REPL joins whatever is already
	// buffered into one input (a paste), so a line is written only after
	// the prompt that reads it. EOF after the last one.
	go func() {
		defer func() { _ = in.Close() }()
		for _, line := range stdin {
			select {
			case <-stderr.prompts:
			case <-time.After(30 * time.Second):
				return
			}
			if _, err := io.WriteString(in, line+"\n"); err != nil {
				return
			}
		}
		if len(stdin) > 0 {
			select {
			case <-stderr.prompts:
			case <-time.After(30 * time.Second):
			}
		}
	}()
	runErr := cmd.Wait()
	status := 0
	var exit *exec.ExitError
	if errors.As(runErr, &exit) {
		status = exit.ExitCode()
	} else if runErr != nil {
		t.Fatal(runErr)
	}
	calls, _ := os.ReadFile(recordPath)
	exitRaw, _ := os.ReadFile(exitPath)
	return charResult{
		stdout:      stdout.String(),
		stderr:      stderr.String(),
		status:      status,
		calls:       prettyCalls(t, calls),
		transcripts: readTranscripts(t, filepath.Join(e.home, ".local", "state", "gem-agent", "sessions")),
		exit:        strings.Fields(strings.ReplaceAll(strings.TrimSpace(string(exitRaw)), " ", "_")),
	}
}

// promptWriter collects the child's stderr and signals each REPL prompt.
type promptWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	seen    int
	prompts chan struct{}
}

func (w *promptWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	for c := strings.Count(w.buf.String(), "\n> "); w.seen < c; w.seen++ {
		w.prompts <- struct{}{}
	}
	return n, err
}

func (w *promptWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// prettyCalls renders the call log one indented JSON document per call,
// so a difference in the reference reads as a line, not as a 40 KB line.
func prettyCalls(t *testing.T, raw []byte) string {
	t.Helper()
	var b strings.Builder
	for i, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatal(err)
		}
		out, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "--- call %d\n%s\n", i+1, out)
	}
	return b.String()
}

// readTranscripts concatenates every transcript under the sessions root,
// ordered by their first record's position in the run (file names carry
// random ids, so they are numbered instead).
func readTranscripts(t *testing.T, root string) string {
	t.Helper()
	var files []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	type tr struct {
		first string
		body  string
	}
	var all []tr
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		first, _, _ := strings.Cut(string(raw), "\n")
		all = append(all, tr{first: first, body: string(raw)})
	}
	// The first record of each starts with its time; sort by it. Times are
	// normalized afterwards, so only their order survives.
	sort.Slice(all, func(i, j int) bool { return all[i].first < all[j].first })
	var b strings.Builder
	for i, x := range all {
		fmt.Fprintf(&b, "--- transcript %d\n%s", i+1, x.body)
	}
	return b.String()
}

var (
	uuidRe     = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	timeRe     = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?(Z|[+-]\d\d:\d\d)`)
	nonceRe    = regexp.MustCompile(`(\b|_)[0-9a-f]{16,64}\b`)
	durationRe = regexp.MustCompile(`\b\d+(\.\d+)?(ms|µs|s)\b`)
	weekdayRe  = regexp.MustCompile(`\b(Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday), \d{1,2} (January|February|March|April|May|June|July|August|September|October|November|December) \d{4}\b`)
	dateRe     = regexp.MustCompile(`\b\d{4}-\d\d-\d\d\b`)
)

// normalize replaces what differs between two runs of the same session —
// the temporary root, ids, times, nonces — and nothing else.
func normalize(e charEnv, s string) string {
	// The state directory names the project flattened (ADR-0022); first,
	// before the plain path is replaced inside it.
	s = strings.ReplaceAll(s, statedir.EscapeProject(e.project), "<PROJECT-ESCAPED>")
	s = strings.ReplaceAll(s, e.project, "<PROJECT>")
	s = strings.ReplaceAll(s, e.home, "<HOME>")
	s = strings.ReplaceAll(s, e.root, "<ROOT>")
	s = timeRe.ReplaceAllString(s, "<TIME>")
	s = weekdayRe.ReplaceAllString(s, "<DAY>")
	s = dateRe.ReplaceAllString(s, "<DATE>")
	ids := map[string]string{}
	s = uuidRe.ReplaceAllStringFunc(s, func(id string) string {
		if n, ok := ids[id]; ok {
			return n
		}
		ids[id] = fmt.Sprintf("<ID%d>", len(ids)+1)
		return ids[id]
	})
	s = nonceRe.ReplaceAllString(s, "${1}<HEX>")
	s = durationRe.ReplaceAllString(s, "<DUR>")
	return s
}

func (r charResult) render(e charEnv) string {
	return normalize(e, fmt.Sprintf("=== status\n%d\n=== stdout\n%s\n=== stderr\n%s\n=== model calls\n%s\n=== transcripts\n%s",
		r.status, r.stdout, r.stderr, r.calls, r.transcripts))
}

func checkCharacterization(t *testing.T, name, got string) {
	t.Helper()
	ref := filepath.Join("testdata", "characterize", name+".txt")
	if *updateCharacterization {
		if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ref, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("%v — record the reference with -update on code known to be right", err)
	}
	if got != string(want) {
		// Outside t.TempDir, which is removed when the test ends: the
		// file is what the reader compares.
		dir := filepath.Join(os.TempDir(), "gem-agent-characterize")
		_ = os.MkdirAll(dir, 0o755)
		gotPath := filepath.Join(dir, name+".got.txt")
		_ = os.WriteFile(gotPath, []byte(got), 0o644)
		t.Fatalf("the session no longer does what %s records (ADR-0124: a refactor changes nothing observable).\ncompare: diff %s %s", ref, ref, gotPath)
	}
}

// One-shot: a tool call and an answer, under -p.
func TestCharacterizeOneShot(t *testing.T) {
	e := newCharEnv(t)
	turns := []scriptTurn{
		{Tool: "read_file", Args: map[string]any{"path": "notes.txt", "purpose": "read the notes"}},
		{Text: "The notes have two lines."},
	}
	r := runCharacterize(t, e, turns, nil, "--no-sandbox", "-p", "What do the notes say?")
	checkCharacterization(t, "oneshot", r.render(e))
	// ADR-0124 §2.5 without what this run does not set up: no TUI, no
	// exit summary under -p, telemetry off.
	checkExitSequence(t, r.exit, []string{
		"report the added roots' persistent files",
		"report the project's persistent files",
		"run the session-end hooks",
		"end the telemetry session",
		"close the MCP clients",
		"close the skills' roots",
		"remove the work directory if empty",
		"remove the fallback scratch",
		"close the transcript",
	})
}

// checkExitSequence compares the teardown as it ran with ADR-0124 §2.5's
// order (the direct-shell hook, its step 13, is no longer state to undo:
// runDirectShell takes it as an argument).
func checkExitSequence(t *testing.T, got, want []string) {
	t.Helper()
	norm := make([]string, len(want))
	for i, w := range want {
		norm[i] = strings.ReplaceAll(w, " ", "_")
	}
	if strings.Join(got, "\n") != strings.Join(norm, "\n") {
		t.Fatalf("the exit sequence changed (ADR-0124 §2.5)\ngot:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(norm, "\n  "))
	}
}

// The plain REPL: piped stdin without -p is the operator's own input.
// A turn, the read-only slash commands, /clear (a new session), the two
// reloads, a turn in the new session, EOF.
func TestCharacterizePlainREPL(t *testing.T) {
	e := newCharEnv(t)
	turns := []scriptTurn{
		{Text: "Hello."},
		{Text: "Hello again."},
	}
	stdin := []string{
		"hi",
		"/help",
		"/usage",
		"/skills",
		"/clear",
		"/skills reload",
		"/mcp reload",
		"hi again",
	}
	r := runCharacterize(t, e, turns, stdin, "--no-sandbox")
	checkCharacterization(t, "plain", r.render(e))
	checkExitSequence(t, r.exit, []string{
		"report the added roots' persistent files",
		"report the project's persistent files",
		"run the session-end hooks",
		"print the exit summary",
		"end the telemetry session",
		"close the MCP clients",
		"close the skills' roots",
		"remove the work directory if empty",
		"remove the fallback scratch",
		"close the transcript",
	})
}

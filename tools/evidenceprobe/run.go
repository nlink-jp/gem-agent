package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// prompts are fixed before the runs. The operator works in Japanese.
var prompts = map[string]string{
	"s1": "DWH の EDR プロセスイベント（データセット security のテーブル edr_process_events）から、ホスト WS-0142 の 2026-10-28 01:00〜02:00（UTC）のイベントを取得してください。取得した結果は全件、調査のエビデンスとして evidence/ 配下にファイルで保存してください。",
	"s2": "ホスト WS-0142 から 2026-10-28 01:00〜02:00（UTC）に外部への大量送信があった疑いがあります。DWH の EDR プロセスイベント（データセット security のテーブル edr_process_events）で該当時間帯のプロセスを調べ、所見を報告書 report.md にまとめてください。報告書の根拠としたクエリ結果は全件、エビデンスとして evidence/ 配下にファイルで保存してください。",
}

// scenarios: s3 is s1's prompt against the wide table, whose full result
// is larger than the runtime's inline cap.
func promptFor(scenario string) string {
	if scenario == "s3" {
		return prompts["s1"]
	}
	return prompts[scenario]
}

const serverName = "bigquery-nlink-dwh"

const gemConfig = `[gcp]
location = "global"

[model]
name = %q
thinking = %q
risk = %q
risk_thinking = "low"
summary = %q
safety = "off"

[sandbox]
enabled = true

[agent]
max_turns = 50
auto_compact = true

[mcp]
enabled = true
advertise = "on-request"
preload = [%q]

[telemetry]
enabled = false
`

const lagentConfig = `[llm]
provider = %q
base_url = %q
model = %q

[sandbox]
enabled = true

[agent]
max_turns = 50

[mcp]
enabled = true
advertise = "deferred"
preload = [%q]
`

type runtimeOpts struct {
	runtime  string // gem | lagent
	model    string
	thinking string // gem
	risk     string // gem
	summary  string // gem
	provider string // lagent
	baseURL  string // lagent
}

type record struct {
	Runtime    string     `json:"runtime"`
	Scenario   string     `json:"scenario"`
	Run        int        `json:"run"`
	Attempt    int        `json:"attempt"`
	ExitCode   int        `json:"exit_code"`
	Seconds    float64    `json:"seconds"`
	Valid      bool       `json:"valid"`
	Final      string     `json:"final"`
	ToolCalls  []toolCall `json:"tool_calls"`
	Spilled    bool       `json:"spilled"`
	Tokens     tokens     `json:"tokens"`
	Verdict    *verdict   `json:"verdict,omitempty"`
	Transcript string     `json:"transcript"`
	// Contaminated: a tool call reached outside the run's own directory
	// into the probe's area, or named the probe.
	Contaminated string `json:"contaminated,omitempty"`
	// End is how the turn ended, read from the records the runtime
	// writes (see turnEnd); Failure names why an invalid run is
	// replaced.
	End     string `json:"end"`
	Failure string `json:"failure,omitempty"`
	Stderr  string `json:"stderr_tail"`
}

type toolCall struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

type tokens struct {
	Calls    int64 `json:"calls"`
	Prompt   int64 `json:"prompt"`
	Cached   int64 `json:"cached"`
	Output   int64 `json:"output"`
	Thoughts int64 `json:"thoughts"`
}

func (t *tokens) add(o tokens) {
	t.Calls += o.Calls
	t.Prompt += o.Prompt
	t.Cached += o.Cached
	t.Output += o.Output
	t.Thoughts += o.Thoughts
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var o runtimeOpts
	fs.StringVar(&o.runtime, "runtime", "", "gem or lagent")
	bin := fs.String("bin", "", "the runtime's binary")
	scenario := fs.String("scenario", "", "s1, s2 or s3")
	n := fs.Int("n", 10, "valid runs wanted")
	out := fs.String("out", "", "output directory (records)")
	work := fs.String("work", "", "where runs execute; outside -out")
	fence := fs.String("fence", "", "a tool call naming a path under this, other than the run's own directory, contaminates the run")
	jobs := fs.Int("jobs", 1, "concurrent runs")
	timeout := fs.Duration("timeout", 15*time.Minute, "per-run timeout")
	maxPrompt := fs.Int64("max-prompt-tokens", 0, "stop starting runs once the cell's prompt tokens reach this (0 = no cap)")
	fs.StringVar(&o.model, "model", "", "main model (required)")
	fs.StringVar(&o.thinking, "thinking", "medium", "gem: thinking level")
	fs.StringVar(&o.risk, "risk-model", "", "gem: risk model (required for gem)")
	fs.StringVar(&o.summary, "summary-model", "", "gem: summary model (required for gem)")
	fs.StringVar(&o.provider, "provider", "mlxserve", "lagent: provider")
	fs.StringVar(&o.baseURL, "base-url", "http://localhost:11234/v1", "lagent: base URL")
	_ = fs.Parse(args)
	if *bin == "" || promptFor(*scenario) == "" || *out == "" || *work == "" || *fence == "" || o.model == "" {
		return errors.New("run needs -runtime, -bin, -scenario s1|s2|s3, -model, -out, -work and -fence")
	}
	switch o.runtime {
	case "gem":
		if o.risk == "" || o.summary == "" {
			return errors.New("-runtime gem needs -risk-model and -summary-model")
		}
	case "lagent":
	default:
		return errors.New("-runtime must be gem or lagent")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	base, err := filepath.Abs(filepath.Join(*out, o.runtime, *scenario))
	if err != nil {
		return err
	}
	var mu sync.Mutex
	valid, attempt, finished := 0, 0, 0
	var spent tokens
	capped := false
	maxAttempts := *n * 2
	sem := make(chan struct{}, *jobs)
	var wg sync.WaitGroup
	for {
		mu.Lock()
		if *maxPrompt > 0 && spent.Prompt >= *maxPrompt && !capped {
			capped = true
			fmt.Fprintf(os.Stderr, "%s/%s: prompt-token cap %d reached (%d) — no new runs\n", o.runtime, *scenario, *maxPrompt, spent.Prompt)
		}
		done := valid >= *n || attempt >= maxAttempts || capped
		busy := attempt-finished >= *n-valid
		mu.Unlock()
		if done {
			break
		}
		if busy {
			time.Sleep(time.Second)
			continue
		}
		sem <- struct{}{}
		mu.Lock()
		attempt++
		a := attempt
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			dir := filepath.Join(base, fmt.Sprintf("attempt-%02d", a))
			_ = os.MkdirAll(dir, 0o755)
			runDir, err := os.MkdirTemp(*work, "w")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return
			}
			rec := oneRun(self, *bin, o, *scenario, runDir, dir, a, *timeout)
			rec.Contaminated = contamination(rec.ToolCalls, *fence, runDir)
			if rec.Contaminated != "" {
				rec.Valid = false
			}
			mu.Lock()
			spent.add(rec.Tokens)
			finished++
			if rec.Valid && valid < *n {
				valid++
				rec.Run = valid
			}
			total := spent
			mu.Unlock()
			b, _ := json.MarshalIndent(rec, "", "  ")
			_ = os.WriteFile(filepath.Join(dir, "record.json"), b, 0o644)
			cat := "-"
			if rec.Verdict != nil {
				cat = rec.Verdict.Category
			}
			fmt.Fprintf(os.Stderr, "%s/%s attempt %d: valid=%v end=%s category=%s exit=%d %.0fs prompt=%d (cell so far: prompt=%d cached=%d out+thoughts=%d)\n",
				o.runtime, *scenario, a, rec.Valid, rec.End, cat, rec.ExitCode, rec.Seconds, rec.Tokens.Prompt, total.Prompt, total.Cached, total.Output+total.Thoughts)
		}()
	}
	wg.Wait()
	if valid < *n {
		return fmt.Errorf("%s/%s: only %d valid runs in %d attempts", o.runtime, *scenario, valid, attempt)
	}
	return nil
}

func oneRun(self, bin string, o runtimeOpts, scenario, dir, recDir string, attempt int, timeout time.Duration) record {
	rec := record{Runtime: o.runtime, Scenario: scenario, Attempt: attempt, ExitCode: -1}
	home := filepath.Join(dir, "home")
	state := filepath.Join(dir, "state")
	project := filepath.Join(dir, "project")
	cfgDir := filepath.Join(home, ".config", map[string]string{"gem": "gem-agent", "lagent": "lagent"}[o.runtime])
	for _, d := range []string{cfgDir, state, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			rec.Stderr = err.Error()
			return rec
		}
	}
	var cfg string
	if o.runtime == "gem" {
		cfg = fmt.Sprintf(gemConfig, o.model, o.thinking, o.risk, o.summary, serverName)
	} else {
		cfg = fmt.Sprintf(lagentConfig, o.provider, o.baseURL, o.model, serverName)
	}
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o644)
	stubLog := filepath.Join(recDir, "calls.jsonl")
	env := map[string]string{"BQ_LOG": stubLog}
	if scenario == "s3" {
		env["BQ_WIDE"] = "1"
	}
	mcp := map[string]any{"mcpServers": map[string]any{serverName: map[string]any{"command": self, "args": []string{"serve"}, "env": env}}}
	mb, _ := json.Marshal(mcp)
	_ = os.WriteFile(filepath.Join(cfgDir, "mcp.json"), mb, 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", promptFor(scenario), "--auto",
		"--allow", "mcp__"+serverName+"__*")
	cmd.Dir = project
	// The child-environment rule first (ADR-0087 §2), then the probe's
	// own isolation: every runtime variable and HOME are replaced.
	cmd.Env = childEnv(sandbox.ChildEnv(os.Environ()), home, state, o.runtime)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	rec.Seconds = time.Since(start).Seconds()
	rec.ExitCode = 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			rec.ExitCode = ee.ExitCode()
		} else {
			rec.ExitCode = -1
		}
	}
	if s := stderr.String(); len(s) > 600 {
		rec.Stderr = s[len(s)-600:]
	} else {
		rec.Stderr = s
	}
	// The project as the agent left it, kept with the record.
	files, _ := projectFiles(project)
	for name, b := range files {
		p := filepath.Join(recDir, "project", name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, b, 0o644)
	}
	if t := findTranscript(state); t != "" {
		rec.Transcript = filepath.Join(recDir, "transcript.jsonl")
		if b, err := os.ReadFile(t); err == nil {
			_ = os.WriteFile(rec.Transcript, b, 0o644)
		}
		readTranscript(t, &rec)
	}
	rec.Valid, rec.Failure = judgeRun(rec.End, ctx.Err() != nil)
	if calls, err := readStubLog(stubLog); err == nil || os.IsNotExist(err) {
		v := score(calls, files)
		classifyMethod(&v, files, calls, rec.ToolCalls)
		rec.Verdict = &v
	}
	return rec
}

// childEnv strips both runtimes' own variables, then sets the run's
// isolation. Credentials stay: GOOGLE_APPLICATION_CREDENTIALS and
// GEMAGENT_PROJECT are read from the caller and re-set explicitly.
func childEnv(environ []string, home, state, runtime string) []string {
	var out []string
	project := os.Getenv("GEMAGENT_PROJECT")
	for _, kv := range environ {
		if strings.HasPrefix(kv, "GEMAGENT_") || strings.HasPrefix(kv, "LAGENT_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+home)
	if runtime == "gem" {
		out = append(out, "GEMAGENT_STATE_DIR="+state, "GEMAGENT_PROJECT="+project)
	} else {
		out = append(out, "LAGENT_STATE_DIR="+state)
	}
	return out
}

func findTranscript(state string) string {
	var found string
	_ = filepath.WalkDir(state, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") && strings.Contains(p, string(os.PathSeparator)+"projects"+string(os.PathSeparator)) && found == "" {
			found = p
		}
		return nil
	})
	return found
}

var dotdot = regexp.MustCompile(`(^|[\s"'/=:])\.\.(/|["'\s;|&)]|$)`)

// contamination names the first tool call that reached into the fence
// outside the run's own directory, climbed out by "..", or named the
// probe or its records.
func contamination(calls []toolCall, fence, own string) string {
	fence = filepath.Clean(fence) + string(os.PathSeparator)
	own = filepath.Clean(own)
	for _, c := range calls {
		a := strings.ReplaceAll(c.Args, `\/`, "/")
		if strings.Contains(a, "evidenceprobe") || strings.Contains(a, "calls.jsonl") || strings.Contains(a, "record.json") || dotdot.MatchString(a) {
			return c.Name + " " + a
		}
		for rest := a; ; {
			i := strings.Index(rest, fence)
			if i < 0 {
				break
			}
			if !strings.HasPrefix(rest[i:], own) {
				return c.Name + " " + a
			}
			rest = rest[i+len(fence):]
		}
	}
	return ""
}

const spillMarker = "too large to hold inline"

func readTranscript(path string, rec *record) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var ends turnEnd
	defer func() { rec.End = ends.end }()
	for sc.Scan() {
		var r struct {
			Kind string `json:"kind"`
			Data struct {
				FinishReason string `json:"finish_reason"`
				Decision     *bool  `json:"decision"`
				Prompt       int64  `json:"prompt"`
				Cached       int64  `json:"cached"`
				Output       int64  `json:"output"`
				Thoughts     int64  `json:"thoughts"`
				Role         string `json:"role"`
				Content      string `json:"content"`
				ToolCalls    []struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"tool_calls"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		ends.see(r.Kind, r.Data.Role, r.Data.Content != "", len(r.Data.ToolCalls) > 0, r.Data.FinishReason, r.Data.Decision)
		switch {
		case r.Kind == "usage":
			rec.Tokens.add(tokens{Prompt: r.Data.Prompt, Cached: r.Data.Cached, Output: r.Data.Output, Thoughts: r.Data.Thoughts, Calls: 1})
		case r.Kind == "message" && r.Data.Role == "assistant":
			if r.Data.Content != "" {
				rec.Final = r.Data.Content
			}
			for _, tc := range r.Data.ToolCalls {
				a, _ := json.Marshal(tc.Args)
				rec.ToolCalls = append(rec.ToolCalls, toolCall{tc.Name, string(a)})
			}
		case r.Kind == "message" && r.Data.Role == "tool":
			if strings.Contains(r.Data.Content, spillMarker) {
				rec.Spilled = true
			}
		}
	}
}

// How a one-shot turn ended, from the record kinds both runtimes write
// to the transcript — a closed set, never the stderr text or the model's
// words. Each model turn resets the ending; a record after it that stops
// the turn (round_intervention refused, window_full) or reports the
// transport (connection_lost) overrides it.
const (
	endAnswer      = "answer"       // an assistant message with text and no tool calls
	endGuard       = "runtime_stop" // the runtime stopped the turn on the model's own behaviour
	endEmpty       = "empty"        // the model's completions carried nothing, finish reason not "error"
	endModelError  = "model_error"  // the server ended a completion with finish reason "error"
	endConnection  = "connection_lost"
	endAfterTools  = "no_turn_after_tools" // results went back and no model turn was recorded: the request failed
	endNoModelTurn = "no_model_turn"       // not one model turn was recorded: the first request failed
)

type turnEnd struct{ end string }

func (t *turnEnd) see(kind, role string, hasText, hasCalls bool, finish string, decision *bool) {
	switch {
	case kind == "message" && role == "user":
		t.end = endNoModelTurn
	case kind == "message" && role == "assistant" && hasCalls:
		t.end = endAfterTools
	case kind == "message" && role == "assistant" && hasText:
		t.end = endAnswer
	case kind == "assistant_empty" && finish == "error":
		t.end = endModelError
	case kind == "assistant_empty":
		t.end = endEmpty
	case kind == "round_intervention" && decision != nil && !*decision, kind == "window_full":
		t.end = endGuard
	case kind == "connection_lost":
		t.end = endConnection
	}
}

// judgeRun applies the pre-registration (main.go): a run that fails
// before the model answers — an API error or a timeout — is replaced.
// An answer, a turn the runtime stopped on the model's behaviour and a
// model that kept returning nothing are the model's outcomes and count.
// A run with no transcript at all never reached the model.
func judgeRun(end string, timedOut bool) (bool, string) {
	switch {
	case timedOut:
		return false, "timeout"
	case end == endAnswer || end == endGuard || end == endEmpty:
		return true, ""
	case end == "":
		return false, "no transcript"
	default:
		return false, end
	}
}

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

// prompts are fixed before the runs; the operator's report was written
// in Japanese and so are these.
var prompts = map[string]string{
	"s1": "ログ検索ツールで、ホスト WS-0142 の 2026-10-01T10:00:00Z〜10:10:00Z のプロセスイベントを取得し、その時間帯にそのホストで何件のプロセスイベントがあったか答えてください。",
	"s2": "手順として、データソースを照会する前にナレッジノート kb/datasources/DeviceProcessEvents.md を読んでください。そのうえで、ホスト WS-0142 で 2026-10-01T10:00:00Z〜10:10:00Z に実行された異なるプロセスの数を答えてください。",
}

const probeConfig = `[gcp]
location = "global"

[model]
name = %q
thinking = "high"
safety = "off"

[sandbox]
enabled = true

[agent]
max_turns = 50
auto_compact = true

[mcp]
enabled = true
advertise = "all"

[telemetry]
enabled = false
`

// record is one run as the scorer reads it.
type record struct {
	Arm        string     `json:"arm"`
	Scenario   string     `json:"scenario"`
	Run        int        `json:"run"`
	Attempt    int        `json:"attempt"`
	ExitCode   int        `json:"exit_code"`
	Seconds    float64    `json:"seconds"`
	Valid      bool       `json:"valid"`
	Final      string     `json:"final"`
	ToolCalls  []toolCall `json:"tool_calls"`
	SpillPaths []string   `json:"spill_paths"`
	SpillRead  bool       `json:"spill_read"`
	Transcript string     `json:"transcript"`
	// Contaminated: a tool call reached outside the run's own directory
	// into the probe's scratch area or named the probe itself — where
	// other runs' answers and this program's source live.
	Contaminated string `json:"contaminated,omitempty"`
	Stderr       string `json:"stderr_tail"`
}

type toolCall struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

func runCmd(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	bin := fs.String("bin", "", "gem-agent binary of the arm")
	arm := fs.String("arm", "", "arm label (a, b, c)")
	scenario := fs.String("scenario", "", "s1 or s2")
	n := fs.Int("n", 20, "valid runs wanted")
	out := fs.String("out", "", "output directory")
	jobs := fs.Int("jobs", 4, "concurrent runs")
	model := fs.String("model", "gemini-3.8-flash", "main model")
	timeout := fs.Duration("timeout", 10*time.Minute, "per-run timeout")
	work := fs.String("work", "", "where runs execute (their projects, homes, state); outside -out")
	fence := fs.String("fence", "", "a tool call naming a path under this, other than the run's own directory, contaminates the run")
	_ = fs.Parse(args)
	if *bin == "" || *arm == "" || prompts[*scenario] == "" || *out == "" || *work == "" || *fence == "" {
		return errors.New("run needs -bin, -arm, -scenario s1|s2, -out, -work and -fence")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	base, err := filepath.Abs(filepath.Join(*out, *arm, *scenario))
	if err != nil {
		return err
	}
	var mu sync.Mutex
	valid, attempt, finished := 0, 0, 0
	maxAttempts := *n * 2
	sem := make(chan struct{}, *jobs)
	var wg sync.WaitGroup
	for {
		mu.Lock()
		done := valid >= *n || attempt >= maxAttempts
		// Never more runs in flight than valid runs still needed.
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
			rec := oneRun(self, *bin, *arm, *scenario, *model, runDir, a, *timeout)
			rec.Contaminated = contamination(rec.ToolCalls, *fence, runDir)
			if rec.Contaminated != "" {
				rec.Valid = false
			}
			if rec.Transcript != "" {
				if b, err := os.ReadFile(rec.Transcript); err == nil {
					_ = os.WriteFile(filepath.Join(dir, "transcript.jsonl"), b, 0o644)
				}
			}
			mu.Lock()
			finished++
			if rec.Valid && valid < *n {
				valid++
				rec.Run = valid
			}
			mu.Unlock()
			b, _ := json.MarshalIndent(rec, "", "  ")
			_ = os.WriteFile(filepath.Join(dir, "record.json"), b, 0o644)
			fmt.Fprintf(os.Stderr, "%s/%s attempt %d: valid=%v exit=%d %.0fs\n", *arm, *scenario, a, rec.Valid, rec.ExitCode, rec.Seconds)
		}()
	}
	wg.Wait()
	if valid < *n {
		return fmt.Errorf("%s/%s: only %d valid runs in %d attempts", *arm, *scenario, valid, attempt)
	}
	return nil
}

func oneRun(self, bin, arm, scenario, model, dir string, attempt int, timeout time.Duration) record {
	rec := record{Arm: arm, Scenario: scenario, Attempt: attempt, ExitCode: -1}
	home := filepath.Join(dir, "home")
	cfgDir := filepath.Join(home, ".config", "gem-agent")
	state := filepath.Join(dir, "state")
	project := filepath.Join(dir, "project")
	for _, d := range []string{cfgDir, state, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			rec.Stderr = err.Error()
			return rec
		}
	}
	_ = os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(fmt.Sprintf(probeConfig, model)), 0o644)
	server := func(target string) map[string]any {
		return map[string]any{"command": self, "args": []string{"serve"},
			"env": map[string]string{"BRIDGE_TARGET": target, "BRIDGE_DATASET": scenario}}
	}
	mcp := map[string]any{"mcpServers": map[string]any{"splunk": server("splunk"), "obsidian": server("obsidian")}}
	mb, _ := json.Marshal(mcp)
	_ = os.WriteFile(filepath.Join(cfgDir, "mcp.json"), mb, 0o644)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", prompts[scenario],
		"--allow", "mcp__splunk__splunk_run_query,mcp__obsidian__get_vault_file")
	cmd.Dir = project
	// The child-environment rule (ADR-0087 §2) strips gem-agent's own
	// variables; the run's isolation is then set explicitly. Exec keeps
	// the last of a duplicated key.
	cmd.Env = append(sandbox.ChildEnv(os.Environ()),
		"HOME="+home, "GEMAGENT_STATE_DIR="+state, "GEMAGENT_PROJECT="+os.Getenv("GEMAGENT_PROJECT"))
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
	matches, _ := filepath.Glob(filepath.Join(state, "sessions", "projects", "*", "*.jsonl"))
	if len(matches) == 0 {
		return rec
	}
	rec.Transcript = matches[0]
	readTranscript(matches[0], &rec)
	rec.Valid = rec.Final != "" && ctx.Err() == nil
	return rec
}

// spillRe finds the saved path in a spill notice, in every arm's shape.
var spillRe = regexp.MustCompile(`saved[^\n]*?read_file (\S+?)\]|whole result is saved at (\S+)`)

// dotdot is a climb out of the project by a relative path, which the
// fence's absolute-path match would not see.
var dotdot = regexp.MustCompile(`(^|[\s"'/=:])\.\.(/|["'\s;|&)]|$)`)

// contamination names the first tool call that reached into the fence
// outside the run's own directory, or named the probe.
func contamination(calls []toolCall, fence, own string) string {
	fence = filepath.Clean(fence) + string(os.PathSeparator)
	own = filepath.Clean(own)
	for _, c := range calls {
		a := strings.ReplaceAll(c.Args, `\/`, "/")
		if strings.Contains(a, "coverageprobe") || strings.Contains(a, "record.json") || dotdot.MatchString(a) {
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

func readTranscript(path string, rec *record) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var after []string // assistant tool-call args after the first spill
	for sc.Scan() {
		var r struct {
			Kind string `json:"kind"`
			Data struct {
				Role        string `json:"role"`
				Content     string `json:"content"`
				RuntimeNote string `json:"runtime_note"`
				ToolCalls   []struct {
					Name string         `json:"name"`
					Args map[string]any `json:"args"`
				} `json:"tool_calls"`
			} `json:"data"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Kind != "message" {
			continue
		}
		switch r.Data.Role {
		case "assistant":
			if r.Data.Content != "" {
				rec.Final = r.Data.Content
			}
			for _, tc := range r.Data.ToolCalls {
				a, _ := json.Marshal(tc.Args)
				rec.ToolCalls = append(rec.ToolCalls, toolCall{tc.Name, string(a)})
				if len(rec.SpillPaths) > 0 {
					after = append(after, string(a))
				}
			}
		case "tool":
			for _, m := range spillRe.FindAllStringSubmatch(r.Data.Content+"\n"+r.Data.RuntimeNote, -1) {
				p := m[1]
				if p == "" {
					p = m[2]
				}
				rec.SpillPaths = append(rec.SpillPaths, p)
			}
		}
	}
	for _, p := range rec.SpillPaths {
		for _, a := range after {
			if bytes.Contains([]byte(a), []byte(filepath.Base(p))) {
				rec.SpillRead = true
			}
		}
	}
}

package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The scenarios only measure something if the deciding bytes are where
// the design says: outside the spill preview, in the tail, or in §2.

func headTail(s string, head, tail int) (string, string) {
	r := []rune(s)
	return string(r[:head]), string(r[len(r)-tail:])
}

func TestS1MetadataIsInTheTailOnly(t *testing.T) {
	p := page(100, 200, 2300)
	if len(p) < 200_000 || len(p) > 300_000 {
		t.Fatalf("page is %d bytes", len(p))
	}
	head, tail := headTail(p, 800, 200)
	if strings.Contains(head, "total_rows") || !strings.Contains(tail, `"truncated":true,"total_rows":200}`) {
		t.Errorf("metadata placement: head %v tail %q", strings.Contains(head, "total_rows"), tail)
	}
}

func TestS2AnswerIsInSectionTwoOnly(t *testing.T) {
	n := note()
	if len(n) < 43_000 || len(n) > 46_000 {
		t.Fatalf("note is %d bytes", len(n))
	}
	head, tail := headTail(n, 800, 200)
	if strings.Contains(head, "ProcLineageKey") || strings.Contains(tail, "ProcLineageKey") {
		t.Error("the deciding key is visible in a preview")
	}
	if strings.Count(n, "ProcLineageKey") == 0 || !strings.Contains(n, "## 2. Pitfalls") {
		t.Error("§2 does not carry the key")
	}
	if !utf8.ValidString(n) {
		t.Error("invalid UTF-8")
	}
}

func TestStubAnswers(t *testing.T) {
	if got, _ := call("s2", "run_query", map[string]any{"query": "index=edr | stats dc(ProcLineageKey)"}); !strings.Contains(got, `"37"`) {
		t.Errorf("lineage query = %s", got)
	}
	if got, _ := call("s2", "run_query", map[string]any{"query": "index=edr | stats dc(ProcessId)"}); !strings.Contains(got, `"52"`) {
		t.Errorf("pid query = %s", got)
	}
	if got, _ := call("s1", "run_query", map[string]any{"query": "index=edr | stats count"}); !strings.Contains(got, `"200"`) {
		t.Errorf("count query = %s", got)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		s, final string
		ok       bool
	}{
		{"s1", "プロセスイベントは 100 件でした。", false},
		{"s1", "合計 200 件です（取得できたのは 100 件）。", true},
		{"s1", "取得できたのは 100 件ですが、結果は切り捨てられています。", true},
		{"s1", "2026-10-01 に 100 件", false},
		{"s2", "異なるプロセスは 37 個です。", true},
		{"s2", "異なるプロセスは 52 個です。", false},
	} {
		if ok, _ := classify(c.s, c.final); ok != c.ok {
			t.Errorf("%s %q = %v", c.s, c.final, ok)
		}
	}
}

func TestFisher(t *testing.T) {
	// 10/10 vs 0/10: p = 2 / C(20,10) ≈ 1.08e-5.
	if p := fisher(10, 0, 0, 10); p > 2e-5 || p < 1e-5 {
		t.Errorf("p = %g", p)
	}
	if p := fisher(5, 5, 5, 5); p < 0.99 {
		t.Errorf("equal arms p = %g", p)
	}
}

func TestContamination(t *testing.T) {
	fence, own := "/s/scratch", "/s/scratch/w/w123"
	for _, c := range []struct {
		args string
		bad  bool
	}{
		{`{"path":"/s/scratch/w/w123/project/a.txt"}`, false},
		{`{"command":"cat /s/scratch/w/w123/work/x.json"}`, false},
		{`{"command":"ls /s/scratch/w/"}`, true},
		{`{"command":"cat /s/scratch/out/a/s1/attempt-01/transcript.jsonl"}`, true},
		{`{"command":"strings /s/scratch/bin/probe-mcp"}`, true},
		{`{"command":"find / -name record.json"}`, true},
		{`{"command":"cat a.json; ls /s/scratch/w/w123/x; ls /s/scratch/w/w999"}`, true},
		{`{"command":"ls ..","gem_agent_purpose":"確認..."}`, true},
		{`{"command":"cat ../w999/project/x"}`, true},
		{`{"gem_agent_purpose":"結果を確認します..."}`, false},
	} {
		got := contamination([]toolCall{{Name: "shell_exec", Args: c.args}}, fence, own) != ""
		if got != c.bad {
			t.Errorf("%s: contaminated=%v", c.args, got)
		}
	}
}

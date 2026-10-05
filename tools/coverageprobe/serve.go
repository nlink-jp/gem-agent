package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"regexp"
	"strings"
)

// serve is a minimal MCP server over stdio: newline-delimited JSON-RPC,
// the methods gem-agent's client uses and nothing else.
func serve(in io.Reader, out io.Writer, target, scenario string) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification
		}
		var result any
		var rpcErr map[string]any
		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2025-06-18"
			}
			result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": target, "version": "1.4.2"},
			}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": toolList(target)}
		case "tools/call":
			var p struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, isErr := call(scenario, p.Name, p.Arguments)
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}
		default:
			rpcErr = map[string]any{"code": -32601, "message": "method not found"}
		}
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		if rpcErr != nil {
			resp["error"] = rpcErr
		} else {
			resp["result"] = result
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func toolList(target string) []map[string]any {
	if target != "splunk" {
		return []map[string]any{{
			"name":        "get_vault_file",
			"description": "Return the full Markdown content of a note in the knowledge vault.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string", "description": "vault-relative path of the note"},
				},
				"required": []string{"path"},
			},
		}}
	}
	return []map[string]any{
		{
			"name":        "splunk_run_query",
			"description": "Run a Splunk search (SPL) and return the matching events as JSON. At most 100 rows are returned per call.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":    map[string]any{"type": "string", "description": "SPL query"},
					"earliest": map[string]any{"type": "string", "description": "start time (ISO 8601)"},
					"latest":   map[string]any{"type": "string", "description": "end time (ISO 8601)"},
				},
				"required": []string{"query"},
			},
		},
	}
}

var (
	statsRe   = regexp.MustCompile(`(?i)\b(stats|count|dc\(|distinct)`)
	lineageRe = regexp.MustCompile(`ProcLineageKey`)
)

func call(scenario, name string, args map[string]any) (string, bool) {
	switch name {
	case "get_vault_file":
		p, _ := args["path"].(string)
		if !strings.Contains(p, "DeviceProcessEvents") {
			return fmt.Sprintf("note not found: %s (available: kb/datasources/DeviceProcessEvents.md)", p), true
		}
		return note(), false
	case "splunk_run_query", "run_query":
		q, _ := args["query"].(string)
		if scenario == "s2" {
			switch {
			case lineageRe.MatchString(q):
				return `{"results":[{"distinct_processes":"37"}],"truncated":false,"total_rows":1}`, false
			case statsRe.MatchString(q):
				return `{"results":[{"distinct_processes":"52"}],"truncated":false,"total_rows":1}`, false
			default:
				return page(20, 640, 300), false
			}
		}
		if statsRe.MatchString(q) {
			return `{"results":[{"count":"200"}],"truncated":false,"total_rows":1}`, false
		}
		return page(100, 200, 2300), false
	}
	return "unknown tool " + name, true
}

// page is a result page in the log server's shape: the rows, then the
// flags — so "truncated" and "total_rows" are the last bytes.
func page(rows, total, cmdLen int) string {
	type row struct {
		Raw string `json:"_raw"`
	}
	type result struct {
		Results   []row `json:"results"`
		Truncated bool  `json:"truncated"`
		TotalRows int   `json:"total_rows"`
	}
	rng := rand.New(rand.NewSource(42))
	names := []string{"chrome.exe", "msedgewebview2.exe", "svchost.exe", "powershell.exe", "conhost.exe", "node.exe", "git.exe", "python.exe"}
	res := result{Truncated: rows < total, TotalRows: total}
	for i := range rows {
		ev := map[string]any{
			"Timestamp":                 fmt.Sprintf("2026-10-01T10:%02d:%02dZ", (i*3)/60, (i*3)%60),
			"DeviceName":                "WS-0142",
			"ActionType":                "ProcessCreated",
			"FileName":                  names[rng.Intn(len(names))],
			"ProcessId":                 1000 + rng.Intn(400),
			"InitiatingProcessFileName": names[rng.Intn(len(names))],
			"ProcessCommandLine":        commandLine(rng, cmdLen),
		}
		b, _ := json.Marshal(ev)
		res.Results = append(res.Results, row{Raw: string(b)})
	}
	b, _ := json.Marshal(res)
	return string(b)
}

func commandLine(rng *rand.Rand, n int) string {
	var b strings.Builder
	b.WriteString(`"C:\Program Files\App\app.exe"`)
	for b.Len() < n {
		fmt.Fprintf(&b, " --flag-%d=%x", rng.Intn(1000), rng.Int63())
	}
	return b.String()[:n]
}

// note is the 44 KB data-source note. The instruction that decides the
// answer is in §2 only; the head (frontmatter, contents) and the tail
// (the end of §3) carry nothing that names it.
func note() string {
	var b strings.Builder
	b.WriteString("---\ntitle: DeviceProcessEvents\ntags: [datasource, edr]\nupdated: 2026-09-20\nowner: secops\n---\n\n")
	b.WriteString("# DeviceProcessEvents — data source note\n\n## Contents\n\n1. Overview and field reference\n2. Pitfalls: correlating processes\n3. Query patterns and retention\n\n")
	b.WriteString("## 1. Overview and field reference\n\nEach row records one process creation observed by the endpoint sensor. The table below lists the fields as ingested.\n\n| Field | Type | Description |\n|---|---|---|\n")
	rng := rand.New(rand.NewSource(7))
	words := strings.Fields("sensor value recorded when the event was observed by the agent on the endpoint and forwarded to the store after normalisation of the vendor schema into the shared model used by detections and hunting queries")
	for i := 0; b.Len() < 20000; i++ {
		desc := make([]string, 18)
		for j := range desc {
			desc[j] = words[rng.Intn(len(words))]
		}
		fmt.Fprintf(&b, "| Field%03d | string | %s. |\n", i, strings.Join(desc, " "))
	}
	b.WriteString("\n## 2. Pitfalls: correlating processes\n\n")
	b.WriteString("On this fleet ProcessId values are recycled within minutes, so the same ProcessId names different processes inside one ten-minute window. Counting distinct ProcessId (or ProcessId with FileName) over-counts processes. Our ingestion pipeline adds `ProcLineageKey`, which is unique per process instance: always count processes with `| stats dc(ProcLineageKey)`, never with ProcessId.\n\n")
	b.WriteString("## 3. Query patterns and retention\n\n")
	for i := 0; b.Len() < 44000; i++ {
		desc := make([]string, 24)
		for j := range desc {
			desc[j] = words[rng.Intn(len(words))]
		}
		fmt.Fprintf(&b, "- Pattern %d: %s.\n", i, strings.Join(desc, " "))
	}
	b.WriteString("\nRetention: 90 days hot, 400 days archive.\n")
	return b.String()
}

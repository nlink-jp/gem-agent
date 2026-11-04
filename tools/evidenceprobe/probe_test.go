package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/tools"
)

const fullQuery = "SELECT * FROM `corp-sec-dwh.security.edr_process_events` WHERE host = 'WS-0142' AND event_time >= TIMESTAMP('2026-10-28 01:00:00') AND event_time < TIMESTAMP('2026-10-28 02:00:00') ORDER BY event_time"

func needSQLite(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("/usr/bin/sqlite3"); err != nil {
		t.Skip("no /usr/bin/sqlite3")
	}
}

func queryText(t *testing.T, wide bool, sql string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"query": sql})
	text, isErr := callTool(dataset(), wide, "query", args)
	if isErr {
		t.Fatalf("query failed: %s", text)
	}
	return text
}

func rowsOf(t *testing.T, text string) []map[string]any {
	t.Helper()
	var r struct {
		Rows []map[string]any `json:"rows"`
	}
	// UseNumber: a float64 prints 4.8213e+07, which no agent writes.
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	if err := dec.Decode(&r); err != nil {
		t.Fatal(err)
	}
	return r.Rows
}

func TestDatasetIsDeterministic(t *testing.T) {
	a, b := dataset(), dataset()
	if len(a) != targetRows+noiseRows {
		t.Fatalf("%d rows", len(a))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d differs between calls", i)
		}
	}
}

// The scenarios rest on which side of the runtime's inline cap the full
// result falls. If the data or the cap moves, this fails before any run.
func TestResultSizesStraddleTheInlineCap(t *testing.T) {
	needSQLite(t)
	small := queryText(t, false, fullQuery)
	large := queryText(t, true, fullQuery)
	if n := len(rowsOf(t, small)); n != targetRows {
		t.Fatalf("s1 query returned %d rows, want %d", n, targetRows)
	}
	if len(small) > tools.OutputCap*9/10 {
		t.Errorf("s1 result is %d bytes, too near the %d-byte cap", len(small), tools.OutputCap)
	}
	if len(large) < tools.OutputCap*12/10 {
		t.Errorf("s3 result is %d bytes, too near the %d-byte cap", len(large), tools.OutputCap)
	}
	t.Logf("s1 %d bytes, s3 %d bytes, cap %d", len(small), len(large), tools.OutputCap)
}

func TestQueryDialect(t *testing.T) {
	needSQLite(t)
	cases := []struct {
		sql  string
		rows int
	}{
		{fullQuery, 60},
		{`SELECT event_id, sha256 FROM security.edr_process_events WHERE host = "WS-0142" AND event_time BETWEEN '2026-10-28T01:00:00Z' AND '2026-10-28T01:59:59.999999Z'`, 60},
		{"SELECT * FROM `edr_process_events` WHERE host='WS-0142' AND event_time >= TIMESTAMP '2026-10-28 01:00:00 UTC' AND event_time < TIMESTAMP '2026-10-28 02:00:00 UTC';", 60},
		{"SELECT * FROM `corp-sec-dwh.security.edr_process_events` WHERE DATE(event_time) = '2026-10-28'", 300 - 0},
	}
	for _, c := range cases {
		res, err := runSQL(dataset(), false, c.sql)
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if c.rows != 300 && len(res.rows) != c.rows {
			t.Errorf("%s: %d rows, want %d", c.sql, len(res.rows), c.rows)
		}
	}
}

func TestQueryShapes(t *testing.T) {
	needSQLite(t)
	text := queryText(t, false, "SELECT COUNT(*) FROM `corp-sec-dwh.security.edr_process_events` WHERE host = 'WS-0142' AND event_time >= '2026-10-28 01:00:00' AND event_time < '2026-10-28 02:00:00'")
	if !strings.Contains(text, `"rows":[{"f0_":60}]`) || !strings.Contains(text, `{"mode":"NULLABLE","name":"f0_","type":"INTEGER"}`) {
		t.Errorf("count result: %s", text)
	}
	full := queryText(t, false, fullQuery)
	// Go's encoding of the real server: backslashes doubled, timestamps
	// RFC 3339 with Z, NULL as null.
	for _, want := range []string{`NT AUTHORITY\\SYSTEM`, `"event_time":"2026-10-28T01:`, `"remote_ip":null`, `"truncated":false`} {
		if !strings.Contains(full, want) {
			t.Errorf("full result lacks %s", want)
		}
	}
	args, _ := json.Marshal(map[string]any{"query": "SELECT FORMAT_TIMESTAMP('%F', event_time) FROM security.edr_process_events"})
	if text, isErr := callTool(dataset(), false, "query", args); !isErr || !strings.Contains(text, "Function not found: FORMAT_TIMESTAMP") {
		t.Errorf("unknown function: %v %s", isErr, text)
	}
	args, _ = json.Marshal(map[string]any{"query": "DELETE FROM security.edr_process_events WHERE true"})
	if _, isErr := callTool(dataset(), false, "query", args); !isErr {
		t.Error("DELETE ran")
	}
}

// csvOf renders rows the way an agent re-typing them might: the same
// values, another layout.
func csvOf(rows []map[string]any) string {
	var b strings.Builder
	b.WriteString("event_id,event_time,host,account,process_name,sha256,remote_ip,bytes_out\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "%v,%v,%v,%v,%v,%v,%v,%v\n", r["event_id"], r["event_time"], r["host"], r["account"], r["process_name"], r["sha256"], r["remote_ip"], r["bytes_out"])
	}
	return b.String()
}

func TestScoreCategories(t *testing.T) {
	needSQLite(t)
	text := queryText(t, false, fullQuery)
	rows := rowsOf(t, text)
	calls := []stubCall{{Tool: "describe_table", Text: "{}"}, {Tool: "query", Text: text}}
	ev := func(s string) map[string][]byte { return map[string][]byte{"evidence/result.csv": []byte(s)} }

	firstSHA := rows[0]["sha256"].(string)
	flipped := []byte(firstSHA)
	if flipped[10] == 'a' {
		flipped[10] = 'b'
	} else {
		flipped[10] = 'a'
	}
	sum := sha256.Sum256([]byte(csvOf(rows)))

	cases := []struct {
		name  string
		files map[string][]byte
		want  string
	}{
		{"verbatim", map[string][]byte{"evidence/q.json": []byte(text)}, "exact"},
		{"rows as JSON", map[string][]byte{"evidence/q.json": mustJSON(rows)}, "exact"},
		{"csv", ev(csvOf(rows)), "faithful"},
		{"csv with manifest", map[string][]byte{"evidence/result.csv": []byte(csvOf(rows)), "evidence/SHA256SUMS": []byte(hex.EncodeToString(sum[:]) + "  result.csv\n")}, "faithful"},
		{"five rows dropped", ev(csvOf(rows[5:])), "lossy"},
		{"digest abbreviated", ev(strings.ReplaceAll(csvOf(rows), firstSHA, firstSHA[:8]+"…")), "lossy"},
		{"one digest altered", ev(strings.ReplaceAll(csvOf(rows), firstSHA, string(flipped))), "corrupted"},
		{"row invented", ev(csvOf(rows) + "evt-0123456789ab,2026-10-28T01:30:00Z,WS-0142,x,y,z,,0\n"), "corrupted"},
		{"report only", map[string][]byte{"report.md": []byte("# report")}, "none"},
		{"nothing", map[string][]byte{}, "none"},
	}
	for _, c := range cases {
		v := score(calls, c.files)
		if v.Category != c.want {
			t.Errorf("%s: category %s, want %s (%+v)", c.name, v.Category, c.want, v.Classes)
		}
	}
	v := score(calls, ev(strings.ReplaceAll(csvOf(rows), firstSHA, string(flipped))))
	if v.Classes["sha256"].Altered != 1 || v.Classes["sha256"].Fabricated != 0 {
		t.Errorf("one flipped digit should read as altered: %+v", v.Classes["sha256"])
	}
	// The pilot's layout: a space for the T, no Z.
	relaid := strings.NewReplacer("T", " ", "Z", "").Replace(csvOf(rows))
	v = score(calls, ev(relaid))
	if et := v.Classes["event_time"]; et.Reformatted != targetRows || et.Missing != 0 || v.Category != "faithful" {
		t.Errorf("rewritten timestamps: %+v, category %s", et, v.Category)
	}
	v = score(calls, ev(csvOf(rows)+"evt-0123456789ab\n"))
	if v.Classes["event_id"].Fabricated != 1 {
		t.Errorf("an unknown id should read as fabricated: %+v", v.Classes["event_id"])
	}
	if v := score(calls, map[string][]byte{"result.csv": []byte(csvOf(rows))}); !v.Misplaced || v.Category != "faithful" {
		t.Errorf("a file outside evidence/: %+v", v)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestContamination(t *testing.T) {
	fence, own := "/tmp/ep", "/tmp/ep/work/w123"
	for args, want := range map[string]bool{
		`{"path":"/tmp/ep/work/w123/project/evidence/a.json"}`: false,
		`{"path":"/tmp/ep/work/w999/project"}`:                 true,
		`{"path":"/tmp/ep/out/gem/s1/attempt-01/calls.jsonl"}`: true,
		`{"command":"ls -la .."}`:                              true,
		`{"command":"cat evidenceprobe/main.go"}`:              true,
		`{"command":"cp a b"}`:                                 false,
	} {
		got := contamination([]toolCall{{"shell_exec", args}}, fence, own) != ""
		if got != want {
			t.Errorf("%s: contaminated=%v, want %v", args, got, want)
		}
	}
}

func TestClassifyMethod(t *testing.T) {
	needSQLite(t)
	text := queryText(t, true, fullQuery)
	rows := rowsOf(t, text)
	id := rows[0]["event_id"].(string)
	calls := []stubCall{{Tool: "query", Text: text}}
	sh := func(access, cmd string) toolCall {
		b, _ := json.Marshal(map[string]any{"access": access, "command": cmd})
		return toolCall{"shell_exec", string(b)}
	}
	wf := func(path string) toolCall {
		b, _ := json.Marshal(map[string]any{"path": path, "content": "..."})
		return toolCall{"write_file", string(b)}
	}
	wfFrom := func(path, from string) toolCall {
		b, _ := json.Marshal(map[string]any{"path": path, "from": from})
		return toolCall{"write_file", string(b)}
	}
	spill := "/w/work/bigquery-nlink-dwh-query-0a1b2c3d.json"
	cases := []struct {
		name  string
		files map[string][]byte
		tcs   []toolCall
		want  string
	}{
		{"write_file", map[string][]byte{"evidence/a.csv": []byte(csvOf(rows))}, []toolCall{wf("evidence/a.csv")}, "regenerated"},
		{"cp of the saved file", map[string][]byte{"evidence/a.json": []byte(text)}, []toolCall{sh("read", "head -c 300 "+spill), sh("write", "mkdir -p evidence && cp "+spill+" evidence/a.json")}, "copied"},
		{"heredoc", map[string][]byte{"evidence/a.csv": []byte(csvOf(rows))}, []toolCall{sh("write", "cat > evidence/a.csv <<'EOF'\n"+id+",...\nEOF")}, "regenerated"},
		{"jq over the saved file", map[string][]byte{"evidence/rows.json": mustJSON(rows)}, []toolCall{sh("write", "jq '.rows' "+spill+" > evidence/rows.json")}, "derived"},
		{"no writer seen", map[string][]byte{"evidence/a.json": []byte(text)}, nil, "unknown"},
		{"write_file from the saved file", map[string][]byte{"evidence/a.json": []byte(text)}, []toolCall{wfFrom("evidence/a.json", spill)}, "copied"},
		{"write_file from its own re-typing", map[string][]byte{"evidence/a.csv": []byte(csvOf(rows))}, []toolCall{wfFrom("evidence/a.csv", "draft.csv")}, "unknown"},
		{"shell output holding more than the result", map[string][]byte{"evidence/a.json": []byte("header\n" + text)}, []toolCall{sh("write", "{ echo header; cat "+spill+"; } > evidence/a.json")}, "derived"},
		{"one copied, one regenerated", map[string][]byte{"evidence/a.json": []byte(text), "evidence/b.csv": []byte(csvOf(rows))},
			[]toolCall{sh("write", "cp "+spill+" evidence/a.json"), wf("evidence/b.csv")}, "mixed"},
	}
	for _, c := range cases {
		v := score(calls, c.files)
		classifyMethod(&v, c.files, calls, c.tcs)
		if v.Method != c.want {
			t.Errorf("%s: method %s, want %s (%v)", c.name, v.Method, c.want, v.Methods)
		}
	}
}

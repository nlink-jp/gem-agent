package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The stub presents as the fleet's bigquery-mcp: the same tool names,
// descriptions and argument schemas, and results shaped the way that
// server shapes them — one text block of compact JSON, keys in Go's
// sorted map order, HTML characters escaped (json.Marshal), timestamps
// as RFC 3339 with trailing zeros trimmed. Copying a result faithfully
// is the behaviour under measurement, so the bytes have to be the bytes
// an operator's agent really receives.
const (
	projectID = "corp-sec-dwh"
	datasetID = "security"
	tableID   = "edr_process_events"
)

// serve is a minimal MCP server over stdio. Every tools/call and the text
// returned is appended to logPath (JSONL) — the ground truth the scorer
// compares evidence files with.
func serve(in io.Reader, out io.Writer, wide bool, logPath string) error {
	var logf *os.File
	if logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		logf = f
	}
	rows := dataset()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16<<20)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil || len(req.ID) == 0 {
			continue
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
				"serverInfo":      map[string]any{"name": "bigquery-mcp", "version": "0.6.2"},
			}
		case "ping":
			result = map[string]any{}
		case "tools/list":
			result = map[string]any{"tools": toolList()}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &p)
			text, isErr := callTool(rows, wide, p.Name, p.Arguments)
			if logf != nil {
				line, _ := json.Marshal(map[string]any{"tool": p.Name, "args": p.Arguments, "text": text, "is_error": isErr})
				_, _ = logf.Write(append(line, '\n'))
			}
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

const paramsSchema = `"params":{"type":"object","description":"Named query parameters referenced as @name in the SQL. A value is a string, number or boolean (typed as STRING, INT64/FLOAT64, BOOL), or {\"type\": \"DATE\", \"value\": \"2026-09-05\"} for the other scalar types (DATE, TIMESTAMP, DATETIME, TIME, NUMERIC, BIGNUMERIC, BYTES, GEOGRAPHY, JSON). Use literals, not parameters, for partition filters: a parameterised filter may not prune at dry-run time.","additionalProperties":true}`

func toolList() []json.RawMessage {
	tool := func(name, desc, schema string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"name": name, "description": desc, "inputSchema": json.RawMessage(schema)})
		return b
	}
	return []json.RawMessage{
		tool("list_datasets", "List the datasets of a project (the billing project when omitted). Discovery only: nothing is billed and IAM decides what is visible.",
			`{"type":"object","properties":{"project":{"type":"string","description":"Project to list; defaults to the configured billing project."}},"additionalProperties":false}`),
		tool("list_tables", "List the tables and views of a dataset with their kind and partition column. Discovery only; nothing is billed.",
			`{"type":"object","properties":{"dataset":{"type":"string","description":"Dataset id."},"project":{"type":"string","description":"Project of the dataset; defaults to the configured billing project."}},"required":["dataset"],"additionalProperties":false}`),
		tool("describe_table", "Describe a table: schema (nested fields included), partition column and kind, clustering, row count, size, partition expiration and timestamps. Read this before writing SQL against a table: filtering on the partition column is what keeps a query inside the budget.",
			`{"type":"object","properties":{"dataset":{"type":"string","description":"Dataset id."},"table":{"type":"string","description":"Table or view id."},"project":{"type":"string","description":"Project of the dataset; defaults to the configured billing project."}},"required":["dataset","table"],"additionalProperties":false}`),
		tool("dry_run", "Estimate a query without running it: bytes that would be processed, the statement type, the referenced tables, the result schema, and whether the query would pass this server's gate (SELECT only, dataset allowlist, byte budget). query runs the same check itself; call this to preview cost or to see why a query is refused.",
			`{"type":"object","properties":{"query":{"type":"string","description":"GoogleSQL text."},`+paramsSchema+`},"required":["query"],"additionalProperties":false}`),
		tool("query", "Run a read-only GoogleSQL query and return rows as column-keyed objects. The query is always dry-run first and refused unless it is a single SELECT, within the dataset allowlist, and within the byte budget; the run carries maximumBytesBilled and a job timeout. Rows stop at max_rows or the response byte budget, and the result then says truncated: true with total_rows — narrow the query or aggregate in SQL rather than paging through everything.",
			`{"type":"object","properties":{"query":{"type":"string","description":"GoogleSQL SELECT text."},`+paramsSchema+`,"max_rows":{"type":"integer","minimum":1,"description":"Rows to return at most (default and ceiling come from the server config)."}},"required":["query"],"additionalProperties":false}`),
	}
}

func toolErr(code, msg string) (string, bool) {
	b, _ := json.Marshal(map[string]any{"code": code, "message": msg, "retryable": false})
	return string(b), true
}

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"code":"upstream_error","message":%q}`, err.Error())
	}
	return string(b)
}

func schemaOut(cols []column) []map[string]any {
	out := make([]map[string]any, 0, len(cols))
	for _, c := range cols {
		m := map[string]any{"name": c.Name, "type": c.Type}
		if c.Mode != "" {
			m["mode"] = c.Mode
		}
		if c.Description != "" {
			m["description"] = c.Description
		}
		out = append(out, m)
	}
	return out
}

func callTool(rows []event, wide bool, name string, raw json.RawMessage) (string, bool) {
	var a map[string]any
	_ = json.Unmarshal(raw, &a)
	str := func(k string) string { s, _ := a[k].(string); return s }
	full := projectID + "." + datasetID + "." + tableID
	switch name {
	case "list_datasets":
		return marshal(map[string]any{"project": projectID, "count": 1, "datasets": []map[string]any{{"id": datasetID, "location": "asia-northeast1"}}}), false
	case "list_tables":
		if str("dataset") != datasetID {
			return toolErr("not_found", fmt.Sprintf("Not found: Dataset %s:%s", projectID, str("dataset")))
		}
		return marshal(map[string]any{"project": projectID, "dataset": datasetID, "count": 1,
			"tables": []map[string]any{{"id": tableID, "type": "TABLE", "partition_column": "event_time"}}}), false
	case "describe_table":
		if str("dataset") != datasetID || str("table") != tableID {
			return toolErr("not_found", fmt.Sprintf("Not found: Table %s:%s.%s", projectID, str("dataset"), str("table")))
		}
		return marshal(map[string]any{
			"table": full, "type": "TABLE", "schema": schemaOut(columns(wide)),
			"num_rows": len(rows), "num_bytes": 91_234, "size": "89.1 KiB",
			"created": "2026-04-01T00:00:00Z", "modified": "2026-10-28T04:00:00Z",
			"location": "asia-northeast1", "description": "EDR process-start events, one row per process.",
			"partitioning": map[string]any{"column": "event_time", "kind": "TIME", "granularity": "DAY", "require_filter": false},
		}), false
	case "dry_run", "query":
		sql := str("query")
		if strings.TrimSpace(sql) == "" {
			return toolErr("invalid_arguments", "query is required")
		}
		res, err := runSQL(rows, wide, sql)
		if err != nil {
			return toolErr("invalid_query", err.Error())
		}
		processed := int64(10 << 20)
		if name == "dry_run" {
			return marshal(map[string]any{
				"bytes_processed": processed, "bytes_human": "10.0 MiB", "bytes_billed_estimate": processed,
				"accuracy": "PRECISE", "statement_type": "SELECT", "referenced_tables": []string{full},
				"schema": schemaOut(res.cols), "budget_bytes": int64(10 << 30), "within_budget": true,
				"location": "asia-northeast1", "allowed": true,
			}), false
		}
		maxRows := 1000
		if v, ok := a["max_rows"].(float64); ok && v >= 1 {
			maxRows = int(v)
		}
		total := len(res.rows)
		shown := res.rows
		if len(shown) > maxRows {
			shown = shown[:maxRows]
		}
		sum := sha256.Sum256([]byte(sql))
		out := map[string]any{
			"rows": shown, "schema": schemaOut(res.cols), "total_rows": total, "returned_rows": len(shown),
			"truncated": len(shown) < total, "bytes_processed": processed, "bytes_billed": processed,
			"cache_hit": false, "job_id": "bqmcp_" + hex.EncodeToString(sum[:10]), "location": "asia-northeast1",
			"slot_ms": 180 + total*3,
		}
		if len(shown) < total {
			out["truncated_by"] = "max_rows"
			out["note"] = fmt.Sprintf("%d of %d rows returned (stopped by max_rows). Narrow the query with WHERE, aggregate in SQL, or raise max_rows up to 50000.", len(shown), total)
		}
		return marshal(out), false
	}
	return toolErr("invalid_arguments", "unknown tool: "+name)
}

type sqlResult struct {
	cols []column
	rows []map[string]any
}

// runSQL runs GoogleSQL text against the table through the system
// sqlite3 (no driver dependency), after a small dialect translation.
// What it cannot translate fails the way BigQuery would fail a query:
// an error the agent reads and corrects.
func runSQL(rows []event, wide bool, sql string) (sqlResult, error) {
	q, err := translate(sql)
	if err != nil {
		return sqlResult{}, err
	}
	var script strings.Builder
	cols := columns(wide)
	script.WriteString("CREATE TABLE " + tableID + "(")
	for i, c := range cols {
		if i > 0 {
			script.WriteString(",")
		}
		t := "TEXT"
		if c.Type == "INTEGER" {
			t = "INTEGER"
		}
		script.WriteString(c.Name + " " + t)
	}
	script.WriteString(");\nBEGIN;\n")
	lit := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	for _, e := range rows {
		ip, bytesOut := "NULL", "NULL"
		if e.RemoteIP != "" {
			ip, bytesOut = lit(e.RemoteIP), strconv.FormatInt(e.BytesOut, 10)
		}
		fmt.Fprintf(&script, "INSERT INTO %s VALUES(%s,%s,%s,%s,%s,%s,%s,%s", tableID,
			lit(e.EventID), lit(e.EventTime.Format(sqliteTime)), lit(e.Host), lit(e.Account), lit(e.Process), lit(e.SHA256), ip, bytesOut)
		if wide {
			script.WriteString("," + lit(e.CommandLine))
		}
		script.WriteString(");\n")
	}
	script.WriteString("COMMIT;\n" + q + ";\n")
	cmd := exec.Command("/usr/bin/sqlite3", "-bail", "-json", ":memory:")
	cmd.Stdin = strings.NewReader(script.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return sqlResult{}, sqliteError(stderr.String())
	}
	names, records, err := decodeOrdered(stdout.Bytes())
	if err != nil {
		return sqlResult{}, err
	}
	known := map[string]column{}
	for _, c := range cols {
		known[c.Name] = c
	}
	// An unaliased expression is named f0_, f1_, … by BigQuery; SQLite
	// names it by its text.
	anon := 0
	for i, n := range names {
		if !identRe.MatchString(n) {
			names[i] = fmt.Sprintf("f%d_", anon)
			anon++
		}
	}
	res := sqlResult{rows: make([]map[string]any, 0, len(records))}
	for i, n := range names {
		c, ok := known[n]
		if !ok {
			c = column{Name: n, Type: inferType(i, records), Mode: "NULLABLE"}
		} else {
			c.Description = ""
		}
		res.cols = append(res.cols, c)
	}
	for _, rec := range records {
		row := map[string]any{}
		for i, n := range names {
			v := rec[i]
			if s, ok := v.(string); ok && res.cols[i].Type == "TIMESTAMP" {
				if t, err := time.Parse(sqliteTime, s); err == nil {
					v = t.UTC().Format(time.RFC3339Nano)
				}
			}
			row[n] = v
		}
		res.rows = append(res.rows, row)
	}
	return res, nil
}

var (
	tsStored = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}$`)
	identRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// inferType types a derived column (COUNT, MIN, an alias) by its first
// non-null value, the way BigQuery reports an expression's type.
func inferType(i int, records [][]any) string {
	for _, rec := range records {
		if i >= len(rec) {
			continue
		}
		switch x := rec[i].(type) {
		case nil:
			continue
		case json.Number:
			if _, err := x.Int64(); err == nil {
				return "INTEGER"
			}
			return "FLOAT"
		case string:
			if tsStored.MatchString(x) {
				return "TIMESTAMP"
			}
			return "STRING"
		}
	}
	return "STRING"
}

// bqError is a message in BigQuery's own words, capitalised as BigQuery
// writes them; the agent reads it as the server's error text.
type bqError string

func (e bqError) Error() string { return string(e) }

func bqErrorf(format string, args ...any) error { return bqError(fmt.Sprintf(format, args...)) }

func sqliteError(stderr string) error {
	msg := strings.TrimSpace(stderr)
	if i := strings.LastIndex(msg, "\n"); i >= 0 && strings.HasPrefix(strings.TrimSpace(msg[i+1:]), "^") {
		msg = strings.TrimSpace(msg[:i])
	}
	msg = regexp.MustCompile(`^(Parse error|Runtime error)( near line \d+)?:\s*`).ReplaceAllString(msg, "")
	msg = strings.Split(msg, "\n")[0]
	if m := regexp.MustCompile(`no such function: (\w+)`).FindStringSubmatch(msg); m != nil {
		return bqErrorf("Function not found: %s", m[1])
	}
	if m := regexp.MustCompile(`no such column: (\S+)`).FindStringSubmatch(msg); m != nil {
		return bqErrorf("Unrecognized name: %s", m[1])
	}
	if m := regexp.MustCompile(`no such table: (\S+)`).FindStringSubmatch(msg); m != nil {
		return bqErrorf("Not found: Table %s was not found in location asia-northeast1", m[1])
	}
	return bqErrorf("Syntax error: %s", msg)
}

var (
	tableRef   = regexp.MustCompile("(?i)`[^`]*?\\b" + tableID + "`|\\b[\\w-]+\\.[\\w-]+\\." + tableID + "\\b|\\b[\\w-]+\\." + tableID + "\\b")
	otherTick  = regexp.MustCompile("`([^`]*)`")
	dquote     = regexp.MustCompile(`"([^"]*)"`)
	tsFunc     = regexp.MustCompile(`(?i)\b(?:TIMESTAMP|DATETIME|DATE)\s*\(\s*('[^']*')\s*\)`)
	tsPrefix   = regexp.MustCompile(`(?i)\b(?:TIMESTAMP|DATETIME)\s+('[^']*')`)
	tsLiteral  = regexp.MustCompile(`'(\d{4}-\d{2}-\d{2})(?:[ T](\d{2}):(\d{2})(?::(\d{2})(\.\d+)?)?)?\s*(?:Z|UTC|\+00(?::?00)?)?'`)
	trailingSC = regexp.MustCompile(`;\s*$`)
)

// translate turns the GoogleSQL an agent writes for this table into
// SQLite. Only what a filter on host and time needs is translated; the
// rest is left for SQLite to accept or reject.
func translate(sql string) (string, error) {
	q := strings.TrimSpace(sql)
	q = trailingSC.ReplaceAllString(q, "")
	if strings.Contains(q, ";") {
		return "", bqErrorf("Syntax error: only a single SELECT statement is allowed")
	}
	head := strings.ToUpper(strings.TrimSpace(q))
	if !strings.HasPrefix(head, "SELECT") && !strings.HasPrefix(head, "WITH") {
		return "", fmt.Errorf("statement_not_allowed: only SELECT statements may run")
	}
	q = tableRef.ReplaceAllString(q, tableID)
	q = otherTick.ReplaceAllString(q, "$1")
	q = dquote.ReplaceAllStringFunc(q, func(m string) string {
		inner := m[1 : len(m)-1]
		return "'" + strings.ReplaceAll(inner, "'", "''") + "'"
	})
	q = tsFunc.ReplaceAllString(q, "$1")
	q = tsPrefix.ReplaceAllString(q, "$1")
	q = tsLiteral.ReplaceAllStringFunc(q, func(m string) string {
		p := tsLiteral.FindStringSubmatch(m)
		if p[2] == "" {
			return "'" + p[1] + "'"
		}
		sec := p[4]
		if sec == "" {
			sec = "00"
		}
		frac := p[5]
		if frac == "" {
			frac = ".000000"
		}
		for len(frac) < 7 {
			frac += "0"
		}
		return "'" + p[1] + "T" + p[2] + ":" + p[3] + ":" + sec + frac[:7] + "'"
	})
	return q, nil
}

// decodeOrdered reads sqlite3 -json output keeping the column order,
// which a map would lose. Empty output is an empty result.
func decodeOrdered(b []byte) ([]string, [][]any, error) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return nil, nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if _, err := dec.Token(); err != nil { // [
		return nil, nil, err
	}
	var names []string
	var records [][]any
	for dec.More() {
		if _, err := dec.Token(); err != nil { // {
			return nil, nil, err
		}
		var rec []any
		i := 0
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, nil, err
			}
			var v any
			if err := dec.Decode(&v); err != nil {
				return nil, nil, err
			}
			if len(records) == 0 {
				names = append(names, k.(string))
			}
			rec = append(rec, v)
			i++
		}
		if _, err := dec.Token(); err != nil { // }
			return nil, nil, err
		}
		records = append(records, rec)
	}
	return names, records, nil
}

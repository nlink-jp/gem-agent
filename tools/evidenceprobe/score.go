package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Token classes the scorer reads, fixed before the runs. Every data value
// of these classes is distinctive by construction (data.go), so finding
// one in an evidence file is finding that value, and finding one that the
// stub never returned in this run is finding a value the agent wrote
// itself.
var classes = []struct {
	name string
	re   *regexp.Regexp
	// alien: whether a token of this class that was never returned
	// counts against the run. bytes_out is a bare integer and cannot be
	// told from any other number, so it is checked for presence only.
	alien bool
}{
	{"event_id", regexp.MustCompile(`evt-[0-9a-f]{12}`), true},
	{"sha256", regexp.MustCompile(`(?i)\b[0-9a-f]{64}\b`), true},
	{"remote_ip", regexp.MustCompile(`\b(?:192\.0\.2|198\.51\.100|203\.0\.113)\.\d{1,3}\b`), true},
	{"bytes_out", regexp.MustCompile(`\b\d{4,}\b`), false},
	{"event_time", regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`), true},
}

// headline classes decide the category; event_time is reported beside
// them, because rewriting a timestamp into another zone or layout is a
// different act from losing or inventing a row.
var headline = map[string]bool{"event_id": true, "sha256": true, "remote_ip": true, "bytes_out": true}

type stubCall struct {
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
	Text    string          `json:"text"`
	IsError bool            `json:"is_error"`
}

type classScore struct {
	Returned int `json:"returned"`
	Present  int `json:"present"`
	Missing  int `json:"missing"`
	// Altered: a token not returned but within a quarter of its length
	// (edit distance) of one that was — a copy gone wrong. Fabricated:
	// further than that from everything returned.
	Altered    int      `json:"altered"`
	Fabricated int      `json:"fabricated"`
	Examples   []string `json:"examples,omitempty"`
	// Reformatted (event_time only): not present as returned, but the
	// same instant is written in another layout — a space for the T, no
	// Z, trailing zeros on the fraction. Added after the pilot run, where
	// every timestamp was rewritten this way; it is not counted missing.
	Reformatted int `json:"reformatted,omitempty"`
}

var looseTime = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(\.\d+)?`)

// relaidTimes collects the timestamps written in s in any of the loose
// layouts, normalised to the RFC 3339 form the server returns (fraction
// with trailing zeros trimmed, Z).
func relaidTimes(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range looseTime.FindAllStringSubmatch(s, -1) {
		frac := strings.TrimRight(m[3], "0")
		if frac == "." {
			frac = ""
		}
		out[m[1]+"T"+m[2]+frac+"Z"] = true
	}
	return out
}

type verdict struct {
	// Category, fixed before the runs:
	//   exact     — an evidence file holds a returned result byte for
	//               byte, or parses to JSON equal to one (or to its rows)
	//   faithful  — not exact, but no headline token missing or alien
	//   lossy     — headline tokens missing, none alien
	//   corrupted — at least one alien headline token
	//   none      — no evidence file written
	Category      string                `json:"category"`
	EvidenceFiles []string              `json:"evidence_files"`
	Misplaced     bool                  `json:"misplaced,omitempty"`
	ExactFile     string                `json:"exact_file,omitempty"`
	Classes       map[string]classScore `json:"classes"`
	QueryCalls    int                   `json:"query_calls"`
	// Method is how the evidence was written (classifyMethod); Methods
	// is the same per file.
	Method  string            `json:"method,omitempty"`
	Methods map[string]string `json:"methods,omitempty"`
}

func readStubLog(path string) ([]stubCall, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []stubCall
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var c stubCall
		if json.Unmarshal(sc.Bytes(), &c) == nil {
			out = append(out, c)
		}
	}
	return out, sc.Err()
}

// projectFiles lists the regular files under dir with their contents.
func projectFiles(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if strings.HasPrefix(rel, ".") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err == nil && len(b) <= 8<<20 {
			files[rel] = b
		}
		return nil
	})
	return files, err
}

// score compares the files an agent left in its project with what the
// stub really returned to it.
func score(calls []stubCall, files map[string][]byte) verdict {
	v := verdict{Classes: map[string]classScore{}}
	var results []string
	for _, c := range calls {
		if c.Tool == "query" && !c.IsError {
			results = append(results, c.Text)
			v.QueryCalls++
		}
	}
	var evidence []string
	for name := range files {
		if strings.HasPrefix(filepath.ToSlash(name), "evidence/") {
			evidence = append(evidence, name)
		}
	}
	if len(evidence) == 0 {
		for name := range files {
			if !strings.EqualFold(filepath.Base(name), "report.md") {
				evidence = append(evidence, name)
			}
		}
		v.Misplaced = len(evidence) > 0
	}
	sort.Strings(evidence)
	v.EvidenceFiles = evidence
	if len(evidence) == 0 {
		v.Category = "none"
		return v
	}

	// A 64-hex token that is the digest of a file in the project or of a
	// returned result is a checksum the agent computed (an evidence
	// manifest), not a data value.
	digests := map[string]bool{}
	for _, b := range files {
		s := sha256.Sum256(b)
		digests[hex.EncodeToString(s[:])] = true
	}
	for _, t := range results {
		s := sha256.Sum256([]byte(t))
		digests[hex.EncodeToString(s[:])] = true
	}

	var text strings.Builder
	for _, name := range evidence {
		text.Write(files[name])
		text.WriteByte('\n')
		if v.ExactFile == "" && isExact(files[name], results) {
			v.ExactFile = name
		}
	}
	ev := text.String()
	truths := returnedValues(results)

	alienHeadline, missingHeadline := 0, 0
	for _, cl := range classes {
		truth := truths[cl.name]
		got := uniq(cl.re.FindAllString(ev, -1), cl.name)
		cs := classScore{Returned: len(truth)}
		var relaid map[string]bool
		if cl.name == "event_time" {
			relaid = relaidTimes(ev)
		}
		for t := range truth {
			switch {
			case got[t] || (!cl.alien && containsNumber(ev, t)):
				cs.Present++
			case relaid[t]:
				cs.Reformatted++
			default:
				cs.Missing++
			}
		}
		if cl.alien {
			for g := range got {
				if truth[g] || (cl.name == "sha256" && digests[g]) {
					continue
				}
				if near(g, truth) {
					cs.Altered++
				} else {
					cs.Fabricated++
				}
				if len(cs.Examples) < 5 {
					cs.Examples = append(cs.Examples, g)
				}
			}
		}
		sort.Strings(cs.Examples)
		v.Classes[cl.name] = cs
		if headline[cl.name] {
			alienHeadline += cs.Altered + cs.Fabricated
			missingHeadline += cs.Missing
		}
	}
	switch {
	case v.ExactFile != "":
		v.Category = "exact"
	case alienHeadline > 0:
		v.Category = "corrupted"
	case missingHeadline > 0:
		v.Category = "lossy"
	default:
		v.Category = "faithful"
	}
	return v
}

// returnedValues reads the rows of every returned result and sorts their
// cell values into the token classes by what the value looks like, so an
// alias (`bytes_out AS sent`) or an aggregate does not hide a value. A
// number counts as bytes_out when its column name mentions bytes.
func returnedValues(results []string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, cl := range classes {
		out[cl.name] = map[string]bool{}
	}
	for _, r := range results {
		var res struct {
			Rows []map[string]json.RawMessage `json:"rows"`
		}
		if json.Unmarshal([]byte(r), &res) != nil {
			continue
		}
		for _, row := range res.Rows {
			for col, raw := range row {
				var s string
				if json.Unmarshal(raw, &s) == nil {
					for _, cl := range classes {
						if cl.alien && cl.re.MatchString(s) && cl.re.FindString(s) == s {
							if cl.name == "sha256" {
								s = strings.ToLower(s)
							}
							out[cl.name][s] = true
						}
					}
					continue
				}
				var n json.Number
				if strings.Contains(strings.ToLower(col), "bytes") && json.Unmarshal(raw, &n) == nil {
					if _, err := n.Int64(); err == nil {
						out["bytes_out"][n.String()] = true
					}
				}
			}
		}
	}
	return out
}

// containsNumber: the digits of n appear in s as a whole number, not as
// part of a longer one.
func containsNumber(s, n string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], n)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(n)
		before := start == 0 || s[start-1] < '0' || s[start-1] > '9'
		after := end == len(s) || s[end] < '0' || s[end] > '9'
		if before && after {
			return true
		}
		i = start + 1
	}
}

// uniq normalises tokens of a class (hex is compared case-insensitively:
// upper-casing a digest is formatting, not alteration) into a set.
func uniq(ts []string, class string) map[string]bool {
	m := map[string]bool{}
	for _, t := range ts {
		if class == "sha256" {
			t = strings.ToLower(t)
		}
		m[t] = true
	}
	return m
}

// isExact: the file contains a returned result verbatim, or is JSON equal
// to a result or to the result's rows.
func isExact(b []byte, results []string) bool {
	s := string(b)
	for _, r := range results {
		if strings.Contains(s, r) {
			return true
		}
	}
	var got any
	if json.Unmarshal(b, &got) != nil {
		return false
	}
	for _, r := range results {
		var want map[string]any
		if json.Unmarshal([]byte(r), &want) != nil {
			continue
		}
		if jsonEqual(got, want) || jsonEqual(got, want["rows"]) {
			return true
		}
	}
	return false
}

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// near reports whether s is within a quarter of its length, in edit
// distance, of some token in set.
func near(s string, set map[string]bool) bool {
	limit := len(s) / 4
	for t := range set {
		if abs(len(t)-len(s)) > limit {
			continue
		}
		if editDistance(s, t) <= limit {
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// summary aggregates one cell (runtime × scenario).
type summary struct {
	Valid        int            `json:"valid"`
	Attempts     int            `json:"attempts"`
	Contaminated int            `json:"contaminated"`
	Failed       int            `json:"failed_before_answer"`
	Categories   map[string]int `json:"categories"`
	Spilled      int            `json:"spilled"`
	Methods      map[string]int `json:"methods"`
	Tokens       tokens         `json:"tokens"`
}

func (s summary) faithfulRate() float64 {
	if s.Valid == 0 {
		return 0
	}
	return float64(s.Categories["exact"]+s.Categories["faithful"]) / float64(s.Valid)
}

func fmtCell(name string, s summary) string {
	c := s.Categories
	return fmt.Sprintf("%-14s valid=%2d/%2d (contaminated %d, failed %d)  exact=%d faithful=%d lossy=%d corrupted=%d none=%d  faithful-rate=%.0f%%  spilled=%d",
		name, s.Valid, s.Attempts, s.Contaminated, s.Failed, c["exact"], c["faithful"], c["lossy"], c["corrupted"], c["none"], 100*s.faithfulRate(), s.Spilled)
}

// scoreCmd reads every record under -out and prints one line per cell,
// the two pre-registered readings, and the spend.
func scoreCmd(args []string) error {
	if len(args) != 2 || args[0] != "-out" {
		return fmt.Errorf("score needs -out DIR")
	}
	out := args[1]
	recs, _ := filepath.Glob(filepath.Join(out, "*", "*", "attempt-*", "record.json"))
	cells := map[string]*summary{}
	sens := map[string]map[string]int{}
	var order []string
	for _, p := range recs {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var r record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		key := r.Runtime + "/" + r.Scenario
		if r.Scenario == "s3" {
			if r.Spilled {
				key += "+spilled"
			} else {
				key += "-unspilled"
			}
		}
		s, ok := cells[key]
		if !ok {
			s = &summary{Categories: map[string]int{}, Methods: map[string]int{}}
			cells[key] = s
			sens[key] = map[string]int{}
			order = append(order, key)
		}
		s.Attempts++
		s.Tokens.add(r.Tokens)
		cat := "none"
		if r.Verdict != nil {
			cat = r.Verdict.Category
		}
		switch {
		case r.Contaminated != "":
			s.Contaminated++
			sens[key][cat]++
		case !r.Valid:
			s.Failed++
		default:
			s.Valid++
			s.Categories[cat]++
			if r.Verdict != nil && r.Verdict.Method != "" {
				s.Methods[r.Verdict.Method]++
			}
			if r.Spilled {
				s.Spilled++
			}
		}
	}
	sort.Strings(order)
	for _, k := range order {
		fmt.Println(fmtCell(k, *cells[k]))
		fmt.Printf("%-14s   method: %v\n", "", cells[k].Methods)
		if len(sens[k]) > 0 {
			fmt.Printf("%-14s   sensitivity (contaminated runs by their evidence): %v\n", "", sens[k])
		}
	}
	fmt.Println()
	for _, rt := range []string{"gem", "lagent"} {
		var fails []string
		for _, sc := range []string{"s1", "s2"} {
			if s, ok := cells[rt+"/"+sc]; ok {
				f := s.Categories["lossy"] + s.Categories["corrupted"]
				fails = append(fails, fmt.Sprintf("%s %d/%d", sc, f, s.Valid))
			}
		}
		if len(fails) == 0 {
			continue
		}
		r1 := false
		for _, sc := range []string{"s1", "s2"} {
			if s, ok := cells[rt+"/"+sc]; ok && s.Categories["lossy"]+s.Categories["corrupted"] >= 2 {
				r1 = true
			}
		}
		fmt.Printf("%s R1 reproduced: %v (lossy+corrupted: %s)\n", rt, r1, strings.Join(fails, ", "))
		s1, ok1 := cells[rt+"/s1"]
		s3, ok3 := cells[rt+"/s3+spilled"]
		if ok1 && ok3 && s1.Valid > 0 && s3.Valid > 0 {
			d := 100 * (s3.faithfulRate() - s1.faithfulRate())
			fmt.Printf("%s R2 the file helps: %v (s3 spilled %.0f%% − s1 %.0f%% = %+.0f points)\n", rt, d >= 30, 100*s3.faithfulRate(), 100*s1.faithfulRate(), d)
		}
	}
	var total tokens
	for _, k := range order {
		if strings.HasPrefix(k, "gem/") {
			total.add(cells[k].Tokens)
		}
	}
	if total.Calls > 0 {
		fmt.Printf("\ngem spend: %d model calls, prompt %d (cached %d), output+thoughts %d — ingest with: gem-usage-lens ingest -sessions-root <work>/<run>/state/sessions\n",
			total.Calls, total.Prompt, total.Cached, total.Output+total.Thoughts)
	}
	return nil
}

// How an evidence file came to hold what it holds — the axis that
// matters for evidence (added 2026-11-04, before the s3 runs): a file
// regenerated by the model has no provenance even when every value is
// right.
//
//	copied      — written by a shell command and byte-identical to (or
//	              containing) a returned result: the runtime's saved bytes
//	regenerated — written by write_file / edit_file, or by a shell
//	              command whose own text carries returned data values
//	              (a heredoc): the bytes passed through generation
//	derived     — written by a shell command that carries no data values
//	              but is not byte-identical (jq, python over the saved
//	              file): transformed by a tool, not generated
//	unknown     — no tool call that wrote it was found
//
// A run is "copied" or "derived" when every evidence file is, "mixed"
// when some file is regenerated and some is not, else "regenerated".
func classifyMethod(v *verdict, files map[string][]byte, calls []stubCall, tcs []toolCall) {
	var results []string
	for _, c := range calls {
		if c.Tool == "query" && !c.IsError {
			results = append(results, c.Text)
		}
	}
	ids := returnedValues(results)["event_id"]
	v.Methods = map[string]string{}
	seen := map[string]bool{}
	for _, f := range v.EvidenceFiles {
		m := fileMethod(f, files[f], results, ids, tcs)
		v.Methods[f] = m
		seen[m] = true
	}
	switch {
	case len(v.EvidenceFiles) == 0:
		v.Method = ""
	case seen["regenerated"] && (seen["copied"] || seen["derived"]):
		v.Method = "mixed"
	case seen["regenerated"]:
		v.Method = "regenerated"
	case seen["unknown"]:
		v.Method = "unknown"
	case seen["derived"]:
		v.Method = "derived"
	default:
		v.Method = "copied"
	}
}

func fileMethod(rel string, content []byte, results []string, ids map[string]bool, tcs []toolCall) string {
	base := filepath.Base(rel)
	writer := -1
	for i, tc := range tcs {
		var a map[string]any
		if json.Unmarshal([]byte(tc.Args), &a) != nil {
			continue
		}
		switch tc.Name {
		case "write_file", "edit_file":
			if p, _ := a["path"].(string); p != "" && (strings.HasSuffix(filepath.ToSlash(p), filepath.ToSlash(rel)) || filepath.Base(p) == base) {
				writer = i
			}
		case "shell_exec":
			if c, _ := a["command"].(string); strings.Contains(c, base) || (strings.Contains(c, "evidence") && writesFiles(c)) {
				if acc, _ := a["access"].(string); acc != "read" {
					writer = i
				}
			}
		}
	}
	if writer < 0 {
		return "unknown"
	}
	tc := tcs[writer]
	if tc.Name != "shell_exec" {
		return "regenerated"
	}
	for id := range ids {
		if strings.Contains(tc.Args, id) {
			return "regenerated"
		}
	}
	for _, r := range results {
		if strings.Contains(string(content), r) {
			return "copied"
		}
	}
	return "derived"
}

var writeVerb = regexp.MustCompile(`(^|[\s;&|(])(cp|mv|tee|install|rsync|ditto)\s|>`)

func writesFiles(cmd string) bool { return writeVerb.MatchString(cmd) }

// rescoreCmd re-applies the scorer to records already on disk — the
// stub's log, the project as the agent left it, and the record's tool
// calls — and rewrites each record's verdict.
func rescoreCmd(args []string) error {
	if len(args) != 2 || args[0] != "-out" {
		return fmt.Errorf("rescore needs -out DIR")
	}
	recs, _ := filepath.Glob(filepath.Join(args[1], "*", "*", "attempt-*", "record.json"))
	for _, p := range recs {
		dir := filepath.Dir(p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var r record
		if err := json.Unmarshal(b, &r); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		calls, err := readStubLog(filepath.Join(dir, "calls.jsonl"))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		files, _ := projectFiles(filepath.Join(dir, "project"))
		v := score(calls, files)
		classifyMethod(&v, files, calls, r.ToolCalls)
		r.Verdict = &v
		out, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(p, out, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s/%s attempt %d: category=%s method=%s %v\n", r.Runtime, r.Scenario, r.Attempt, v.Category, v.Method, v.Methods)
	}
	return nil
}

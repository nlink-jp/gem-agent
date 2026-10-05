package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// The classification rules are fixed before the runs (see main.go).
var (
	has200  = regexp.MustCompile(`(^|[^\d.,])200([^\d]|$)`)
	partial = regexp.MustCompile(`(?i)truncat|切り捨て|打ち切|全件ではない|全てではない|すべてではない|一部のみ|一部しか|少なくとも|\d+\s*件以上|上限|部分的|incomplete|partial|at least`)
	has37   = regexp.MustCompile(`(^|[^\d.,])37([^\d]|$)`)
)

// classify returns success and the rule that decided it.
func classify(scenario, final string) (bool, string) {
	switch scenario {
	case "s1":
		if has200.MatchString(final) {
			return true, "says 200"
		}
		if partial.MatchString(final) {
			return true, "says partial (audit)"
		}
		return false, "presents the page as whole"
	case "s2":
		if has37.MatchString(final) {
			return true, "says 37"
		}
		return false, "not 37"
	}
	return false, "unknown scenario"
}

func scoreCmd(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	out := fs.String("out", "", "output directory")
	_ = fs.Parse(args)
	files, _ := filepath.Glob(filepath.Join(*out, "*", "*", "attempt-*", "record.json"))
	type cell struct {
		n, ok, spills, read, invalid, contaminated, cAnswered, cOK int
		spent                                                      tokens
	}
	cells := map[string]*cell{}
	var audit []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var r record
		if err := json.Unmarshal(b, &r); err != nil {
			return err
		}
		k := r.Arm + "/" + r.Scenario
		if cells[k] == nil {
			cells[k] = &cell{}
		}
		c := cells[k]
		c.spent.add(r.Tokens) // every attempt spent, valid or not
		if r.Contaminated != "" {
			c.contaminated++
			if r.Final != "" {
				c.cAnswered++
				if ok, _ := classify(r.Scenario, r.Final); ok {
					c.cOK++
				}
			}
			continue
		}
		if !r.Valid || r.Run == 0 {
			c.invalid++
			continue
		}
		c.n++
		ok, why := classify(r.Scenario, r.Final)
		if ok {
			c.ok++
		}
		if len(r.SpillPaths) > 0 {
			c.spills++
		}
		if r.SpillRead {
			c.read++
		}
		if why == "says partial (audit)" {
			audit = append(audit, fmt.Sprintf("%s run %d: %s", k, r.Run, f))
		}
	}
	keys := make([]string, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println("| arm/scenario | valid | success | rate | spilled | spill file read | failed | contaminated |")
	fmt.Println("|---|---|---|---|---|---|---|---|")
	for _, k := range keys {
		c := cells[k]
		fmt.Printf("| %s | %d | %d | %s | %d | %d | %d | %d |\n", k, c.n, c.ok, pct(c.ok, c.n), c.spills, c.read, c.invalid, c.contaminated)
	}
	var all tokens
	fmt.Println("\nSpend — every attempt, valid or not (the run state roots are isolated, so gem-usage-lens does not see it):")
	for _, k := range keys {
		c := cells[k]
		all.add(c.spent)
		fmt.Printf("- %s: %d calls, prompt %d (cached %d), output+thoughts %d\n", k, c.spent.Calls, c.spent.Prompt, c.spent.Cached, c.spent.Output+c.spent.Thoughts)
	}
	fmt.Printf("- total: %d calls, prompt %d (cached %d), output+thoughts %d\n", all.Calls, all.Prompt, all.Cached, all.Output+all.Thoughts)
	fmt.Println("  To price it and keep it in the usage record: for d in <-work>/*/state/sessions; do gem-usage-lens ingest -sessions-root \"$d\"; done")
	fmt.Println("\nSensitivity — contaminated runs scored by their answers (not part of the decision):")
	for _, k := range keys {
		c := cells[k]
		fmt.Printf("- %s: contaminated %d (answered %d, success %d); valid+contaminated success %s\n",
			k, c.contaminated, c.cAnswered, c.cOK, pct(c.ok+c.cOK, c.n+c.cAnswered))
	}
	fmt.Println()
	for _, s := range []string{"s1", "s2"} {
		for _, p := range [][2]string{{"a", "b"}, {"b", "c"}} {
			x, y := cells[p[0]+"/"+s], cells[p[1]+"/"+s]
			if x == nil || y == nil || x.n == 0 || y.n == 0 {
				continue
			}
			d := 100 * (float64(y.ok)/float64(y.n) - float64(x.ok)/float64(x.n))
			fmt.Printf("%s: %s→%s %+.0f points, Fisher two-sided p = %.3f\n", s, p[0], p[1], d, fisher(x.ok, x.n-x.ok, y.ok, y.n-y.ok))
		}
	}
	if len(audit) > 0 {
		fmt.Println("\nTo audit by hand (classified by wording, not by the number):")
		for _, a := range audit {
			fmt.Println("-", a)
		}
	}
	return nil
}

func pct(a, b int) string {
	if b == 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
}

// fisher is the two-sided Fisher exact test for [[a b] [c d]].
func fisher(a, b, c, d int) float64 {
	row1, col1, n := a+b, a+c, a+b+c+d
	logp := func(x int) float64 {
		return lfact(row1) + lfact(n-row1) + lfact(col1) + lfact(n-col1) -
			lfact(n) - lfact(x) - lfact(row1-x) - lfact(col1-x) - lfact(n-row1-col1+x)
	}
	obs := logp(a)
	p := 0.0
	for x := max(0, row1+col1-n); x <= min(row1, col1); x++ {
		if lp := logp(x); lp <= obs+1e-9 {
			p += math.Exp(lp)
		}
	}
	return math.Min(p, 1)
}

func lfact(n int) float64 {
	v, _ := math.Lgamma(float64(n + 1))
	return v
}

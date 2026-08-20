package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fixedClock pins the tool's now for a test and restores it.
func fixedClock(t *testing.T, iso string) {
	t.Helper()
	fixed, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatal(err)
	}
	old := timeNow
	timeNow = func() time.Time { return fixed }
	t.Cleanup(func() { timeNow = old })
}

func runDT(t *testing.T, args map[string]any) string {
	t.Helper()
	out, err := runDateTime(args)
	if err != nil {
		t.Fatalf("runDateTime(%v): %v", args, err)
	}
	return out
}

func TestDateTimeNow(t *testing.T) {
	fixedClock(t, "2026-08-21T10:30:00+09:00")
	out := runDT(t, map[string]any{"op": "now", "tz": "Asia/Tokyo"})
	for _, want := range []string{
		"2026-08-21T10:30:00+09:00", "(Friday)", "JST (UTC+09:00)",
		"utc: 2026-08-21T01:30:00Z", "iso week: 2026-W34",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("now missing %q:\n%s", want, out)
		}
	}
	// Default op is now.
	if out2 := runDT(t, map[string]any{"tz": "Asia/Tokyo"}); out2 != out {
		t.Error("omitted op must behave as now")
	}
}

func TestDateTimeInfo(t *testing.T) {
	// 2024 is a leap year; Feb 29 exists and is day 60.
	out := runDT(t, map[string]any{"op": "info", "date": "2024-02-29", "tz": "UTC"})
	for _, want := range []string{"(Thursday)", "day of year: 60", "days in month: 29", "leap year: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("info missing %q:\n%s", want, out)
		}
	}
	// 2100 is NOT a leap year (century rule) — the classic LLM trap.
	out = runDT(t, map[string]any{"op": "info", "date": "2100-02-01", "tz": "UTC"})
	for _, want := range []string{"days in month: 28", "leap year: false"} {
		if !strings.Contains(out, want) {
			t.Errorf("2100 info missing %q:\n%s", want, out)
		}
	}
}

func TestDateTimeAddNormalizationIsDisclosed(t *testing.T) {
	// Jan 31 + 1 month: Go normalizes to Mar 3 — the tool must say so.
	out := runDT(t, map[string]any{"op": "add", "base": "2026-01-31", "months": 1, "tz": "UTC"})
	if !strings.Contains(out, "2026-03-03") || !strings.Contains(out, "normalized") {
		t.Errorf("month-end normalization not disclosed:\n%s", out)
	}
	// A plain shift carries no note, and negatives work.
	out = runDT(t, map[string]any{"op": "add", "base": "2026-08-21", "days": -30, "tz": "UTC"})
	if !strings.Contains(out, "2026-07-22") || strings.Contains(out, "normalized") {
		t.Errorf("negative day shift wrong:\n%s", out)
	}
}

func TestDateTimeDiff(t *testing.T) {
	out := runDT(t, map[string]any{"op": "diff", "a": "2026-08-21", "b": "2027-03-01", "tz": "UTC"})
	for _, want := range []string{
		"calendar: 0 year(s) 6 month(s) 8 day(s)", "total days: 192",
		"(Friday)", "(Monday)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("diff missing %q:\n%s", want, out)
		}
	}
	// Reversed order carries the sign, same magnitudes.
	out = runDT(t, map[string]any{"op": "diff", "a": "2027-03-01", "b": "2026-08-21", "tz": "UTC"})
	if !strings.Contains(out, "total days: -192") || !strings.Contains(out, "calendar: -0 year(s) 6 month(s) 8 day(s)") {
		t.Errorf("reversed diff sign wrong:\n%s", out)
	}
}

func TestDateTimeConvert(t *testing.T) {
	out := runDT(t, map[string]any{"op": "convert",
		"time": "2026-08-21 09:00", "from": "America/New_York", "to": "Asia/Tokyo"})
	if !strings.Contains(out, "2026-08-21T22:00:00+09:00") { // EDT (UTC-4) → JST
		t.Errorf("NY→Tokyo conversion wrong:\n%s", out)
	}
	// An explicit offset in the input wins over `from`.
	out = runDT(t, map[string]any{"op": "convert",
		"time": "2026-08-21T09:00:00Z", "from": "America/New_York", "to": "Asia/Tokyo"})
	if !strings.Contains(out, "2026-08-21T18:00:00+09:00") {
		t.Errorf("explicit offset not honored:\n%s", out)
	}
}

func TestDateTimeErrorsNameTheContract(t *testing.T) {
	if _, err := runDateTime(map[string]any{"op": "info", "date": "21/08/2026"}); err == nil ||
		!strings.Contains(err.Error(), "RFC3339") {
		t.Errorf("bad date error must teach the accepted forms: %v", err)
	}
	if _, err := runDateTime(map[string]any{"op": "convert", "time": "2026-08-21", "to": "Mars/Olympus"}); err == nil ||
		!strings.Contains(err.Error(), "IANA") {
		t.Errorf("bad zone error must point at IANA names: %v", err)
	}
	if _, err := runDateTime(map[string]any{"op": "convert", "time": "2026-08-21"}); err == nil ||
		!strings.Contains(err.Error(), "to") {
		t.Errorf("convert without to must say so: %v", err)
	}
	if _, err := runDateTime(map[string]any{"op": "century"}); err == nil {
		t.Error("unknown op accepted")
	}
}

// The tool is registered, read-only, and callable through the registry.
func TestDateTimeRegisteredReadOnly(t *testing.T) {
	reg, err := New(t.TempDir(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Get(DateTimeName)
	if !ok {
		t.Fatal("datetime not registered")
	}
	if tool.Mutating {
		t.Error("datetime must be read-only — it discloses a clock")
	}
	out, err := tool.Run(context.Background(), map[string]any{"op": "info", "date": "2026-01-01", "tz": "UTC"})
	if err != nil || !strings.Contains(out, "(Thursday)") {
		t.Errorf("registry run: %q, %v", out, err)
	}
}

package tools

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// timeNow is the datetime tool's clock, injectable for tests.
var timeNow = time.Now

// DateTimeName is the deterministic date/time tool (ADR-0032): the
// model has no clock, and calendar arithmetic is exactly the
// carry-heavy symbol manipulation language models fumble while
// sounding sure.
const DateTimeName = "datetime"

func (r *Registry) dateTime() *Tool {
	return &Tool{
		Name: DateTimeName,
		Description: "Deterministic date/time facts and arithmetic — never compute these yourself. " +
			"op \"now\" (default): current local time, UTC, unix, weekday, ISO week. " +
			"op \"info\": weekday/day-of-year/ISO week/days-in-month/leap for `date`. " +
			"op \"add\": shift `base` by signed years/months/days/hours/minutes. " +
			"op \"diff\": calendar breakdown and totals between `a` and `b`. " +
			"op \"convert\": express `time` in IANA zone `to` (optional `from` for naive inputs). " +
			"Dates: RFC3339, 2006-01-02, or naive 2006-01-02[T ]15:04[:05] read in `tz` (default: local).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"op":      map[string]any{"type": "string", "enum": []string{"now", "info", "add", "diff", "convert"}},
				"date":    map[string]any{"type": "string", "description": "info: the date to describe"},
				"base":    map[string]any{"type": "string", "description": "add: the starting point"},
				"a":       map[string]any{"type": "string", "description": "diff: from"},
				"b":       map[string]any{"type": "string", "description": "diff: to"},
				"time":    map[string]any{"type": "string", "description": "convert: the moment to re-express"},
				"years":   map[string]any{"type": "integer"},
				"months":  map[string]any{"type": "integer"},
				"days":    map[string]any{"type": "integer"},
				"hours":   map[string]any{"type": "integer"},
				"minutes": map[string]any{"type": "integer"},
				"tz":      map[string]any{"type": "string", "description": "IANA zone for naive inputs and `now` (default: the host's local zone)"},
				"to":      map[string]any{"type": "string", "description": "convert: target IANA zone"},
				"from":    map[string]any{"type": "string", "description": "convert: zone of a naive `time` (an explicit offset in the input wins)"},
			},
		},
		Mutating: false,
		Run: func(ctx context.Context, args map[string]any) (string, error) {
			return runDateTime(args)
		},
	}
}

func runDateTime(args map[string]any) (string, error) {
	op, _ := args["op"].(string)
	loc, err := zoneArg(args, "tz", time.Local)
	if err != nil {
		return "", err
	}
	switch op {
	case "", "now":
		return renderNow(timeNow().In(loc)), nil
	case "info":
		t, err := parseWhen(text(args, "date"), loc)
		if err != nil {
			return "", fmt.Errorf("date: %w", err)
		}
		return renderInfoDate(t), nil
	case "add":
		t, err := parseWhen(text(args, "base"), loc)
		if err != nil {
			return "", fmt.Errorf("base: %w", err)
		}
		return renderAdd(t, signedIntArg(args, "years"), signedIntArg(args, "months"), signedIntArg(args, "days"),
			signedIntArg(args, "hours"), signedIntArg(args, "minutes")), nil
	case "diff":
		a, err := parseWhen(text(args, "a"), loc)
		if err != nil {
			return "", fmt.Errorf("a: %w", err)
		}
		b, err := parseWhen(text(args, "b"), loc)
		if err != nil {
			return "", fmt.Errorf("b: %w", err)
		}
		return renderDiff(a, b), nil
	case "convert":
		to, err := zoneArg(args, "to", nil)
		if err != nil {
			return "", err
		}
		if to == nil {
			return "", fmt.Errorf("convert needs `to` (an IANA zone like Asia/Tokyo)")
		}
		from := loc
		if f, err := zoneArg(args, "from", nil); err != nil {
			return "", err
		} else if f != nil {
			from = f
		}
		t, err := parseWhen(text(args, "time"), from)
		if err != nil {
			return "", fmt.Errorf("time: %w", err)
		}
		return renderConvert(t, to), nil
	}
	return "", fmt.Errorf("unknown op %q — now, info, add, diff, or convert", op)
}

// signedIntArg reads an integer that may be negative — the package's
// intArg deliberately filters to positive (line numbers), but date
// arithmetic subtracts.
func signedIntArg(args map[string]any, key string) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// text reads a string argument, absent = "".
func text(args map[string]any, key string) string {
	s, _ := strArg(args, key)
	return s
}

func zoneArg(args map[string]any, key string, fallback *time.Location) (*time.Location, error) {
	name := text(args, key)
	if name == "" {
		return fallback, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%s: unknown timezone %q — use an IANA name like Asia/Tokyo or America/New_York", key, name)
	}
	return loc, nil
}

// parseWhen accepts RFC3339, a bare date, and naive datetimes; naive
// forms are read in loc. Errors name the accepted shapes — a tool the
// model leans on for correctness must not guess at ambiguous input.
func parseWhen(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty — accepted: RFC3339 (2026-08-21T15:04:05+09:00), 2026-08-21, or 2026-08-21 15:04[:05]")
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	naive := strings.Replace(s, "T", " ", 1)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, naive, loc); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q — accepted: RFC3339 (2026-08-21T15:04:05+09:00), 2026-08-21, or 2026-08-21 15:04[:05]", s)
}

func renderNow(t time.Time) string {
	zone, offset := t.Zone()
	year, week := t.ISOWeek()
	return fmt.Sprintf("local: %s (%s)\ntimezone: %s (UTC%s)\nutc: %s\nunix: %d\niso week: %d-W%02d",
		t.Format(time.RFC3339), t.Weekday(), zone, offsetString(offset),
		t.UTC().Format(time.RFC3339), t.Unix(), year, week)
}

func renderInfoDate(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("date: %s (%s)\nday of year: %d\niso week: %d-W%02d\ndays in month: %d\nleap year: %v",
		t.Format("2006-01-02"), t.Weekday(), t.YearDay(), year, week,
		daysInMonth(t.Year(), t.Month()), isLeap(t.Year()))
}

func renderAdd(t time.Time, years, months, days, hours, minutes int) string {
	shifted := t.AddDate(years, months, days).
		Add(time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute)
	out := fmt.Sprintf("base: %s (%s)\nresult: %s (%s)",
		t.Format(time.RFC3339), t.Weekday(), shifted.Format(time.RFC3339), shifted.Weekday())
	// AddDate normalizes overflowing days (Jan 31 + 1 month = Mar 3).
	// Silently normalized dates are how "one month later" bugs ship
	// (ADR-0032 §1) — detect and say so.
	if (years != 0 || months != 0) && days == 0 {
		if want, got := t.Day(), t.AddDate(years, months, 0).Day(); want != got {
			out += fmt.Sprintf("\nnote: day-of-month %d does not exist in the target month; Go normalized it forward (this is %d, not the month's last day)", want, got)
		}
	}
	return out
}

func renderDiff(a, b time.Time) string {
	sign := ""
	x, y := a, b
	if b.Before(a) {
		sign = "-"
		x, y = b, a
	}
	// Calendar walk: whole months first, then whole days — month
	// lengths vary, so this is counting, not division.
	months := (y.Year()-x.Year())*12 + int(y.Month()) - int(x.Month())
	if x.AddDate(0, months, 0).After(y) {
		months--
	}
	anchor := x.AddDate(0, months, 0)
	days := 0
	for !anchor.AddDate(0, 0, days+1).After(y) {
		days++
	}
	d := y.Sub(x)
	return fmt.Sprintf("from: %s (%s)\nto: %s (%s)\ncalendar: %s%d year(s) %d month(s) %d day(s)\ntotal days: %s%.0f\ntotal hours: %s%.1f\ntotal minutes: %s%.0f",
		a.Format(time.RFC3339), a.Weekday(), b.Format(time.RFC3339), b.Weekday(),
		sign, months/12, months%12, days,
		sign, d.Hours()/24, sign, d.Hours(), sign, d.Minutes())
}

func renderConvert(t time.Time, to *time.Location) string {
	c := t.In(to)
	zone, offset := c.Zone()
	return fmt.Sprintf("input: %s (%s)\nconverted: %s (%s)\ntimezone: %s (UTC%s)",
		t.Format(time.RFC3339), t.Weekday(), c.Format(time.RFC3339), c.Weekday(),
		zone, offsetString(offset))
}

func offsetString(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign = "-"
		seconds = -seconds
	}
	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, (seconds%3600)/60)
}

func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func isLeap(year int) bool {
	return year%4 == 0 && (year%100 != 0 || year%400 == 0)
}

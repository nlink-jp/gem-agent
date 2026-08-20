package uitext

import (
	"reflect"
	"regexp"
	"sort"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveExplicit(t *testing.T) {
	// An explicit setting ignores the environment entirely.
	if got := Resolve("ja", env(map[string]string{"LANG": "en_US.UTF-8"})); got != JA {
		t.Errorf("explicit ja: got %v", got)
	}
	if got := Resolve("en", env(map[string]string{"LC_ALL": "ja_JP.UTF-8"})); got != EN {
		t.Errorf("explicit en: got %v", got)
	}
}

func TestResolveAuto(t *testing.T) {
	cases := []struct {
		name string
		vars map[string]string
		want Lang
	}{
		{"empty environment", map[string]string{}, EN},
		{"LANG ja", map[string]string{"LANG": "ja_JP.UTF-8"}, JA},
		{"LANG en", map[string]string{"LANG": "en_US.UTF-8"}, EN},
		{"LANG C", map[string]string{"LANG": "C"}, EN},
		{"LANG POSIX", map[string]string{"LANG": "POSIX"}, EN},
		// POSIX precedence: LC_ALL beats LC_MESSAGES beats LANG, and
		// the first NON-EMPTY value decides — an empty LC_ALL does not
		// force English over a Japanese LANG.
		{"LC_ALL wins over LANG",
			map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "ja_JP.UTF-8"}, EN},
		{"LC_MESSAGES wins over LANG",
			map[string]string{"LC_MESSAGES": "ja_JP.UTF-8", "LANG": "en_US.UTF-8"}, JA},
		{"empty LC_ALL falls through",
			map[string]string{"LC_ALL": "", "LANG": "ja_JP.UTF-8"}, JA},
	}
	for _, c := range cases {
		if got := Resolve("auto", env(c.vars)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCatalogsComplete is the mechanism of ADR-0029 §2: every field
// must be non-empty in BOTH catalogs, so a new chrome string cannot
// ship in only one language.
func TestCatalogsComplete(t *testing.T) {
	for _, cat := range []struct {
		lang Lang
		m    *Messages
	}{{JA, For(JA)}, {EN, For(EN)}} {
		v := reflect.ValueOf(*cat.m)
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).Kind() == reflect.String && v.Field(i).String() == "" {
				t.Errorf("%s catalog: field %s is empty", cat.lang, v.Type().Field(i).Name)
			}
		}
	}
}

// TestFmtFieldsAgree checks that every *Fmt field uses the same fmt
// verbs in the same order in both catalogs — a %d/%s swap would
// compile fine and corrupt one language at runtime.
func TestFmtFieldsAgree(t *testing.T) {
	verbs := regexp.MustCompile(`%[a-zA-Z]`)
	je := reflect.ValueOf(ja)
	ee := reflect.ValueOf(en)
	for i := 0; i < je.NumField(); i++ {
		name := je.Type().Field(i).Name
		jv := verbs.FindAllString(je.Field(i).String(), -1)
		ev := verbs.FindAllString(ee.Field(i).String(), -1)
		if !reflect.DeepEqual(jv, ev) {
			t.Errorf("%s: verb mismatch ja=%v en=%v", name, jv, ev)
		}
	}
}

func TestBroadReason(t *testing.T) {
	m := For(EN)
	for _, k := range []string{"root", "home", "home-ancestor"} {
		if m.BroadReason(k) == "" || m.BroadReason(k) == k {
			t.Errorf("EN BroadReason(%q) not localized: %q", k, m.BroadReason(k))
		}
	}
	// Unknown keys pass through rather than vanish.
	if got := m.BroadReason("weird"); got != "weird" {
		t.Errorf("unknown key: got %q", got)
	}
}

// TestHelpListsEveryCommand pins /help to the command set in both
// languages: adding a slash command without documenting it fails here.
func TestHelpListsEveryCommand(t *testing.T) {
	commands := []string{
		"/auto", "/clear", "/compact", "/help", "/mcp", "/memory",
		"/quit", "/settings", "/skill", "/skills", "/tools", "/usage",
	}
	sort.Strings(commands)
	for _, lang := range []Lang{JA, EN} {
		help := For(lang).Help
		for _, c := range commands {
			if !regexp.MustCompile(`(?m)^  ` + c + `\b`).MatchString(help) {
				t.Errorf("%s help: %s missing", lang, c)
			}
		}
	}
}

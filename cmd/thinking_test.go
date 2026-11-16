package cmd

import (
	"slices"
	"strings"
	"testing"
	"unicode"

	"google.golang.org/genai"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tui"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// fakeDial stands in for the main backend's level.
type fakeDial struct{ level string }

func (f *fakeDial) Thinking() string         { return f.level }
func (f *fakeDial) SetThinking(level string) { f.level = level }

func thinkingCfg(level, source string) *config.Config {
	cfg := &config.Config{}
	cfg.Model.Thinking = level
	if source != "" {
		cfg.Sources = map[string]string{"model.thinking": source}
	}
	return cfg
}

// ADR-0098 §1: a level sets it, default clears it, bare shows it with
// its source.
func TestThinkingCmdSetsAndShows(t *testing.T) {
	en := uitext.For(uitext.EN)
	cfg := thinkingCfg("high", config.FromFlag)
	dial := &fakeDial{level: "high"}

	out, isErr := thinkingCmd(nil, dial, cfg, en)
	if isErr || !strings.Contains(out, "high") || !strings.Contains(out, config.FromFlag) {
		t.Errorf("bare at startup = %q (err %v), want high with the flag's source", out, isErr)
	}
	// Nothing set a level: say whose default it is, not "default (default)".
	if out, _ = thinkingCmd(nil, &fakeDial{}, thinkingCfg("", ""), en); out != en.ThinkingNowDefault {
		t.Errorf("bare with nothing set = %q", out)
	}
	if out, isErr = thinkingCmd([]string{"low"}, dial, cfg, en); isErr || dial.level != "low" || !strings.Contains(out, "low") {
		t.Errorf("/thinking low = %q (err %v), level %q", out, isErr, dial.level)
	}
	if out, _ = thinkingCmd(nil, dial, cfg, en); !strings.Contains(out, "low (session)") {
		t.Errorf("bare after a change = %q, want the session as source", out)
	}
	if _, isErr = thinkingCmd([]string{"default"}, dial, cfg, en); isErr || dial.level != "" {
		t.Errorf("/thinking default left %q (err %v)", dial.level, isErr)
	}
	// Back at the startup value reads the startup source again.
	thinkingCmd([]string{"high"}, dial, cfg, en)
	if out, _ = thinkingCmd(nil, dial, cfg, en); !strings.Contains(out, config.FromFlag) {
		t.Errorf("bare back at the startup level = %q", out)
	}
}

// A typo, an empty word, or trailing words change nothing and say how
// to use it (the /auto and /readonly grammar).
func TestThinkingCmdRejectsWithoutChanging(t *testing.T) {
	en := uitext.For(uitext.EN)
	for _, args := range [][]string{{"hgh"}, {"HIGH"}, {"low", "now"}, {"none"}} {
		dial := &fakeDial{level: "medium"}
		out, isErr := thinkingCmd(args, dial, thinkingCfg("medium", ""), en)
		if !isErr || out != en.ThinkingUsage || dial.level != "medium" {
			t.Errorf("%v: out %q err %v level %q", args, out, isErr, dial.level)
		}
	}
}

// Every word the panel cycles through is one /thinking accepts, and
// each maps to a level config accepts.
func TestThinkingValuesAllParse(t *testing.T) {
	for _, w := range thinkingValues {
		level, ok := parseThinking(w)
		if !ok || !config.ValidThinking(level) || thinkingWord(level) != w {
			t.Errorf("%q: level %q ok %v", w, level, ok)
		}
	}
}

// The shared slash handler routes /thinking, and without the closure
// it is an unknown command rather than a silent no-op.
func TestSlashThinkingRoutes(t *testing.T) {
	en := uitext.For(uitext.EN)
	dial := &fakeDial{}
	reload := slashReloads{thinking: func(args []string) (string, bool) {
		return thinkingCmd(args, dial, thinkingCfg("", ""), en)
	}}
	if _, isErr, _ := slashOutput("/thinking medium", nil, nil, nil, nil, reload, nil, nil, nil, "", en, nil); isErr || dial.level != "medium" {
		t.Errorf("routed /thinking: err %v level %q", isErr, dial.level)
	}
	if _, isErr, _ := slashOutput("/thinking", nil, nil, nil, nil, slashReloads{}, nil, nil, nil, "", en, nil); !isErr {
		t.Error("/thinking without a dial should be an error")
	}
}

// ADR-0098 §2: the panel row is editable, turns the live dial, and its
// source follows the live value.
func TestSettingsThinkingRowTurnsTheDial(t *testing.T) {
	s := newStore(t)
	s.cfg.Model.Thinking = "high"
	s.cfg.Sources = map[string]string{"model.thinking": config.FromFile}
	dial := &fakeDial{level: "high"}
	s.thinking = dial

	row, ok := rowFor(s.data(), "model.thinking")
	if !ok || len(row.Values) == 0 || row.Value != "high" || row.Source != config.FromFile {
		t.Fatalf("row at startup = %+v", row)
	}
	d, note := s.Apply(tui.SettingChange{Label: "model.thinking", Value: "default"})
	if dial.level != "" || !strings.Contains(note, "default") {
		t.Errorf("apply default: level %q note %q", dial.level, note)
	}
	if row, _ = rowFor(d, "model.thinking"); row.Value != "default" || row.Source != "session" {
		t.Errorf("row after the edit = %+v", row)
	}
	// A value outside the cycle is ignored, not stored.
	s.Apply(tui.SettingChange{Label: "model.thinking", Value: "bogus"})
	if dial.level != "" {
		t.Errorf("bogus value stored: %q", dial.level)
	}
}

// Without a backend (tests that build none) the row stays read-only.
func TestSettingsThinkingRowReadOnlyWithoutADial(t *testing.T) {
	row, ok := rowFor(newStore(t).data(), "model.thinking")
	if !ok || len(row.Values) != 0 {
		t.Errorf("row = %+v", row)
	}
}

// ADR-0118 §1: /thinking and the panel row take xhigh, and the row's
// cycle ends on it.
func TestXHighTurnsTheDial(t *testing.T) {
	en := uitext.For(uitext.EN)
	dial := &fakeDial{}
	out, isErr := thinkingCmd([]string{"xhigh"}, dial, thinkingCfg("", ""), en)
	if isErr || dial.level != "xhigh" || !strings.Contains(out, "xhigh") {
		t.Errorf("/thinking xhigh = %q (err %v), level %q", out, isErr, dial.level)
	}
	if !strings.Contains(en.ThinkingUsage, "xhigh") || !strings.Contains(uitext.For(uitext.JA).ThinkingUsage, "xhigh") {
		t.Error("the usage line does not list xhigh in both languages")
	}

	s := newStore(t)
	paneled := &fakeDial{}
	s.thinking = paneled
	row, ok := rowFor(s.data(), "model.thinking")
	if !ok || len(row.Values) == 0 || row.Values[len(row.Values)-1] != "xhigh" {
		t.Fatalf("row cycle = %v", row.Values)
	}
	s.Apply(tui.SettingChange{Label: "model.thinking", Value: "xhigh"})
	if paneled.level != "xhigh" {
		t.Errorf("the panel left the level at %q", paneled.level)
	}
}

// A level is a word in config's one list; everything else that spells
// it is held here, so a level added to the list fails this test until
// the wire value, the usage and help lines in both languages, and the
// flag's help say it too (ADR-0118).
func TestEveryLevelIsSpelledWhereverItIsRead(t *testing.T) {
	// Whole words only: "high" inside "xhigh" must not count as "high".
	words := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	}
	helpLine := func(help string) string {
		for _, line := range strings.Split(help, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "/thinking") {
				return line
			}
		}
		return ""
	}
	flagHelp := words(rootCmd.Flags().Lookup("thinking").Usage)
	wire := map[genai.ThinkingLevel]string{}
	for _, level := range config.ThinkingLevels() {
		w := llm.ThinkingLevel(level)
		if w == "" {
			t.Errorf("%q has no wire value", level)
		} else if other, dup := wire[w]; dup {
			t.Errorf("%q and %q share the wire value %q", level, other, w)
		}
		wire[w] = level
		if !slices.Contains(flagHelp, level) {
			t.Errorf("--thinking help does not list %q: %v", level, flagHelp)
		}
		for _, lang := range []uitext.Lang{uitext.EN, uitext.JA} {
			msgs := uitext.For(lang)
			if !slices.Contains(words(msgs.ThinkingUsage), level) {
				t.Errorf("%v usage line does not list %q", lang, level)
			}
			if !slices.Contains(words(helpLine(msgs.Help)), level) {
				t.Errorf("%v /help line for /thinking does not list %q: %q", lang, level, helpLine(msgs.Help))
			}
		}
	}
	if thinkingValues[0] != "default" || len(thinkingValues) != len(config.ThinkingLevels())+1 {
		t.Errorf("the cycle is not default plus every level: %v", thinkingValues)
	}
}

package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
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

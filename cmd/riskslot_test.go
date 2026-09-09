package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
)

// ADR-0082 §4: /info names the slot only when there is one — without
// it the tier is the main model at the main level, already on the line.
func TestRenderInfoNamesTheRiskSlot(t *testing.T) {
	s := infoFixture()
	if out := renderInfo(s); strings.Contains(out, "risk model") {
		t.Errorf("no slot, but /info names one:\n%s", out)
	}
	s.RiskSlot, s.RiskModel, s.RiskThinking = true, "gemini-judge", "low"
	if out := renderInfo(s); !strings.Contains(out, "risk model: gemini-judge (thinking: low)") {
		t.Errorf("slot not named:\n%s", out)
	}
	s.RiskThinking = ""
	if out := renderInfo(s); !strings.Contains(out, "risk model: gemini-judge (thinking: model default)") {
		t.Errorf("unset level not explained:\n%s", out)
	}
}

// The /settings rows say what an unset value means (ADR-0009), and the
// meaning of an unset level depends on whether a slot exists (ADR-0082
// §2).
func TestRiskSlotSettingsLabels(t *testing.T) {
	cases := []struct {
		m          config.ModelConfig
		model, lvl string
	}{
		{config.ModelConfig{Name: "main"}, "(main model)", "(follows model.thinking)"},
		{config.ModelConfig{Name: "main", Risk: "judge"}, "judge", "(model default)"},
		{config.ModelConfig{Name: "main", Risk: "judge", RiskThinking: "low"}, "judge", "low"},
		{config.ModelConfig{Name: "main", RiskThinking: "low"}, "(main model)", "low"},
	}
	for _, c := range cases {
		if got := riskModelLabel(c.m); got != c.model {
			t.Errorf("%+v: model label %q, want %q", c.m, got, c.model)
		}
		if got := riskThinkingLabel(c.m); got != c.lvl {
			t.Errorf("%+v: level label %q, want %q", c.m, got, c.lvl)
		}
	}
}

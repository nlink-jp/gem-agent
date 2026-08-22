package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Under a CJK locale go-runewidth treats box-drawing glyphs as two
// cells; glamour pads code blocks with it, so box art came out with
// per-line padding that depended on the box-character count and the
// hard-wrap sheared the tails (v0.37.1). The width model is pinned to
// narrow; every rendered line of a box-art block has the same width.
func TestCodeBlockPaddingUniformUnderCJKLocale(t *testing.T) {
	runewidth.DefaultCondition.EastAsianWidth = true // what LANG=ja_JP.UTF-8 does
	t.Cleanup(pinWidthModel)
	t.Setenv("RUNEWIDTH_EASTASIAN", "")
	pinWidthModel()
	if runewidth.DefaultCondition.EastAsianWidth {
		t.Fatal("width model not pinned to narrow")
	}
	art := "```text\n┌──────────┐     ┌──────────┐\n│ string   │     │ id       │\n└──────────┘     └──────────┘\n```"
	render := newGlamourRenderer(80, "dark")
	var widths []int
	for _, l := range strings.Split(render(art), "\n") {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			continue
		}
		widths = append(widths, ansi.StringWidth(l))
	}
	for _, w := range widths {
		if w != widths[0] {
			t.Fatalf("padded widths differ: %v", widths)
		}
	}
}

// An explicit operator choice is honoured — the pin only applies when
// RUNEWIDTH_EASTASIAN is unset.
func TestWidthModelRespectsExplicitEnv(t *testing.T) {
	runewidth.DefaultCondition.EastAsianWidth = true
	t.Cleanup(func() { t.Setenv("RUNEWIDTH_EASTASIAN", ""); pinWidthModel() })
	t.Setenv("RUNEWIDTH_EASTASIAN", "1")
	pinWidthModel()
	if !runewidth.DefaultCondition.EastAsianWidth {
		t.Error("explicit RUNEWIDTH_EASTASIAN=1 was overridden")
	}
}

package cmd

import (
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// thinkingDial is the main backend's live thinking level (ADR-0098):
// *llm.Vertex in the binary, a fake in tests.
type thinkingDial interface {
	Thinking() string
	SetThinking(level string)
}

// thinkingValues are the choices the /settings row cycles through, and
// the words /thinking accepts. "default" is the model's own default —
// the word --thinking uses for the same thing.
var thinkingValues = []string{"default", "minimal", "low", "medium", "high"}

// thinkingWord renders a level as the word the operator types.
func thinkingWord(level string) string {
	if level == "" {
		return "default"
	}
	return level
}

// parseThinking turns an operator's word into a level ("" = default).
func parseThinking(word string) (string, bool) {
	if word == "default" {
		return "", true
	}
	if word == "" || !config.ValidThinking(word) {
		return "", false
	}
	return word, true
}

// thinkingSource is where the level in force came from: this session
// once it differs from the level the session started with, otherwise
// whatever set the startup value (config, flag, or the default).
func thinkingSource(cfg *config.Config, live string) string {
	if live != cfg.Model.Thinking {
		return "session"
	}
	return cfg.Source("model.thinking")
}

// thinkingCmd is /thinking [level] (ADR-0098 §1): bare shows the level
// and its source, a level sets it, anything else is a usage error that
// changes nothing.
func thinkingCmd(args []string, dial thinkingDial, cfg *config.Config, msgs *uitext.Messages) (string, bool) {
	if len(args) > 1 {
		return msgs.ThinkingUsage, true
	}
	if len(args) == 1 {
		level, ok := parseThinking(args[0])
		if !ok {
			return msgs.ThinkingUsage, true
		}
		dial.SetThinking(level)
		return fmt.Sprintf(msgs.ThinkingSetFmt, thinkingWord(level)), false
	}
	live := dial.Thinking()
	source := thinkingSource(cfg, live)
	if live == "" && source == config.FromDefault {
		return msgs.ThinkingNowDefault, false
	}
	return fmt.Sprintf(msgs.ThinkingNowFmt, thinkingWord(live), source), false
}

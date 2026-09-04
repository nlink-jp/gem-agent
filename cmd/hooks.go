package cmd

import (
	"time"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/hooks"
)

// hookEntries converts one configured hook list into the runner's
// shape (ADR-0044 / ADR-0069).
func hookEntries(es []config.HookEntry) []hooks.Hook {
	if len(es) == 0 {
		return nil
	}
	hs := make([]hooks.Hook, 0, len(es))
	for _, e := range es {
		hs = append(hs, hooks.Hook{
			Matcher: e.Matcher, Command: e.Command,
			Timeout: time.Duration(e.TimeoutSec) * time.Second,
		})
	}
	return hs
}

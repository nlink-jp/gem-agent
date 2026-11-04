package cmd

import "github.com/nlink-jp/gem-agent/internal/tui"

// noticeWriter is the TUI's waiting-dialog notifier for a [tui].notify
// value (ADR-0104): it writes that value's fixed bytes through inject,
// which lands them between two renderer flushes. nil for "off", so the
// TUI schedules nothing at all.
func noticeWriter(mode string, inject func(string) error) func() {
	seq := tui.Notice(mode)
	if seq == "" {
		return nil
	}
	return func() { _ = inject(seq) }
}

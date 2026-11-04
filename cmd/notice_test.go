package cmd

import "testing"

// "off" must reach the TUI as no notifier, which is what keeps it from
// scheduling anything; the other values write exactly their bytes.
func TestNoticeWriter(t *testing.T) {
	var got []string
	inject := func(s string) error { got = append(got, s); return nil }
	if noticeWriter("off", inject) != nil {
		t.Fatal(`"off" must give no notifier`)
	}
	noticeWriter("bell", inject)()
	noticeWriter("osc9", inject)()
	want := []string{"\a", "\x1b]9;gem-agent needs your answer\x1b\\\a"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("wrote %q, want %q", got, want)
	}
}

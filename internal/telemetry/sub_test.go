package telemetry

import (
	"testing"
	"time"
)

// ADR-0037: a Sub sink stamps agent=<label> on every event so a
// delegated loop's audit records are attributable; the main sink's
// events stay unstamped so existing queries keep matching.
func TestSubLabelsEvents(t *testing.T) {
	sink, rec := NewRecording()
	sub := sink.Sub("agentic_file_search")

	sink.ToolCall("read_file", false, "path=a", "why", time.Second, "ok", "")
	sub.ToolCall("search_files", false, "pattern=x", "why", time.Second, "ok", "")
	sub.Usage(10, 5, 2, 0, 0, 17)

	events := rec.Events()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if got, ok := events[0].Attrs["agent"]; ok {
		t.Errorf("main sink event carries agent=%q", got)
	}
	for _, ev := range events[1:] {
		if ev.Attrs["agent"] != "agentic_file_search" {
			t.Errorf("%s: agent = %q, want agentic_file_search", ev.Name, ev.Attrs["agent"])
		}
	}
}

// The nil and no-op sinks stay nil-safe through Sub — call sites never
// branch (the package contract).
func TestSubNilSafe(t *testing.T) {
	var nilSink *Sink
	nilSink.Sub("x").ToolCall("t", false, "", "why", 0, "ok", "")
	Nop().Sub("x").Usage(1, 1, 0, 0, 0, 2)
}

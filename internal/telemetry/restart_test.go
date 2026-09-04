package telemetry

import (
	"context"
	"testing"
)

// /clear re-resources the sink in place (ADR-0071 addendum): every
// holder of the pointer — a Sub sink created before the restart
// included — keeps emitting, through the new provider.
func TestRestartSwapsTheProviderUnderEveryHolder(t *testing.T) {
	sink, rec := NewRecording()
	sub := sink.Sub("agentic_file_search")
	_, before := sink.current()
	if err := sink.Restart(context.Background(), "new-session"); err != nil {
		t.Fatal(err)
	}
	_, after := sink.current()
	if before == after {
		t.Fatal("Restart did not build a new provider")
	}
	if _, subProvider := sub.current(); subProvider != after {
		t.Fatal("a Sub created before Restart still points at the old provider")
	}
	sink.SessionStart("m", true, false, 0)
	sub.ToolCall("read_file", false, "x", "why", 0, "ok")
	events := rec.Events()
	if len(events) != 2 {
		t.Fatalf("got %d events after restart, want 2: %+v", len(events), events)
	}
	if events[1].Attrs["agent"] != "agentic_file_search" {
		t.Errorf("sub label lost after restart: %+v", events[1])
	}
	// Nil and builder-less sinks are no-ops.
	var nilSink *Sink
	if err := nilSink.Restart(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if err := (&Sink{}).Restart(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

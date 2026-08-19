package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

func newLogAgent(t *testing.T, log SessionLog, onNotice func(string)) *Agent {
	t.Helper()
	reg, err := tools.New(t.TempDir(),
		func(ctx context.Context, command string) *exec.Cmd {
			return exec.CommandContext(ctx, "/bin/true")
		}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Registry: reg, Gate: &approveAll{}, Log: log, OnNotice: onNotice})
}

// recordingLog captures every record; fail makes conversation writes err.
type recordingLog struct {
	kinds []string
	fail  bool
}

func (l *recordingLog) Log(kind string, data any) error {
	if l.fail {
		return errors.New("disk full")
	}
	l.kinds = append(l.kinds, kind)
	return nil
}

// ADR-0021 §1: /clear is a history mutation like any other — it must
// leave a transcript record, or resume resurrects what was discarded.
func TestResetWritesClearRecord(t *testing.T) {
	log := &recordingLog{}
	a := newLogAgent(t, log, nil)
	a.history = []llm.Message{{Role: llm.RoleUser, Content: "x"}}
	a.Reset()
	if len(log.kinds) == 0 || log.kinds[len(log.kinds)-1] != session.KindClear {
		t.Errorf("Reset recorded %v — the last record must be %q", log.kinds, session.KindClear)
	}
	if len(a.history) != 0 {
		t.Errorf("history not cleared")
	}
}

// ADR-0021 §3: after a conversation-bearing write fails, the transcript
// stops at a consistent prefix (no later record may land) and the
// operator is told.
func TestFailedConversationWriteStopsTranscript(t *testing.T) {
	log := &recordingLog{fail: true}
	var notices []string
	a := newLogAgent(t, log, func(m string) { notices = append(notices, m) })

	a.appendMessage(llm.Message{Role: llm.RoleUser, Content: "lost"})

	if len(notices) != 1 || !strings.Contains(notices[0], "no longer be fully resumed") {
		t.Fatalf("notices = %v — the operator must hear that resume is broken", notices)
	}
	// Later writes are skipped even after the log recovers: a gap in the
	// middle is exactly the desync this exists to prevent.
	log.fail = false
	a.appendMessage(llm.Message{Role: llm.RoleUser, Content: "after"})
	a.logRecord("usage", nil)
	if len(log.kinds) != 0 {
		t.Errorf("records landed after the transcript died: %v", log.kinds)
	}
	if len(notices) != 1 {
		t.Errorf("notice repeated: %v", notices)
	}
}

// A diagnostics-only failure stays best-effort: it neither stops the
// transcript nor notifies.
func TestDiagnosticWriteFailureIsBestEffort(t *testing.T) {
	log := &recordingLog{fail: true}
	var notices []string
	a := newLogAgent(t, log, func(m string) { notices = append(notices, m) })
	a.logRecord("usage", nil)
	if len(notices) != 0 {
		t.Errorf("diagnostic failure notified: %v", notices)
	}
	log.fail = false
	a.appendMessage(llm.Message{Role: llm.RoleUser, Content: "still recorded"})
	if len(log.kinds) != 1 || log.kinds[0] != session.KindMessage {
		t.Errorf("conversation write after diagnostic failure = %v, want it recorded", log.kinds)
	}
}

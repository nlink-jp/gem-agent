package cmd

import (
	"os"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/session"
)

// /clear reported the old session's persistent-file changes through
// curLog.Log; with no startup transcript curLog was nil and the call
// panicked (ADR-0124, found while reading). Every record of the session
// goes through transcript.log now, which records nothing when there is
// no transcript.
func TestTranscriptLogAbsentRecordsNothing(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("recording to an absent transcript panicked: %v", r)
		}
	}()
	if err := (&transcript{}).log("persistent_changes", map[string]any{"reason": "clear"}); err != nil {
		t.Fatalf("absent transcript: want nil, got %v", err)
	}
}

// The panic the callback exists to prevent, shown on the type itself: the
// pattern /clear used before is not safe on a nil logger.
func TestNilLoggerMethodValuePanics(t *testing.T) {
	var none *session.Logger
	defer func() {
		if recover() == nil {
			t.Fatal("a nil *session.Logger's Log no longer panics — transcript.log's guard may be obsolete; revisit ADR-0124's note")
		}
	}()
	_ = none.Log("persistent_changes", nil)
}

func TestTranscriptLogPresentWrites(t *testing.T) {
	dir := t.TempDir()
	lg, err := openSessionLog(dir, "", t.TempDir(), "gemini-x", "global", "v0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&transcript{sessionLog: lg, curLog: lg}).log("persistent_changes", map[string]any{"reason": "clear"}); err != nil {
		t.Fatal(err)
	}
	path := lg.Path()
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"persistent_changes"`) {
		t.Fatalf("record not written: %s", raw)
	}
}

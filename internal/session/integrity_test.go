package session

import (
	"os"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

func openWithHeader(t *testing.T, dir string) *Logger {
	t.Helper()
	lg, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Log(KindHeader, Header{Schema: SchemaVersion, Project: "/p", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	return lg
}

// ADR-0021 §2: a crash's torn last line costs exactly itself. Reopen
// repairs the missing newline so later appends stay parseable, and Load
// skips the torn line instead of dropping everything after it.
func TestTornLineCostsOnlyItself(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	id := lg.ID()
	path := lg.Path()
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "turn one"})
	_ = lg.Close()

	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.WriteString(`{"ts":"2026-01-01T00:00:00Z","kind":"message","da`) // torn, no newline
	_ = f.Close()

	lg2, err := Reopen(dir, "/p", id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		_ = lg2.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "post-resume turn"})
	}
	_ = lg2.Close()

	history, _, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(history) != 6 {
		t.Errorf("recovered %d of 6 messages — records after the tear must survive", len(history))
	}
	if skipped != 1 {
		t.Errorf("skipped = %d, want 1 (the torn line, reported, not silent)", skipped)
	}
}

// Mid-file corruption is also skipped with a count, not treated as EOF.
func TestCorruptMiddleLineIsSkippedNotFatal(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	id := lg.ID()
	path := lg.Path()
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "before"})
	_ = lg.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.WriteString("this is not json\n")
	_ = f.Close()
	lg2, _ := Reopen(dir, "/p", id)
	_ = lg2.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "after"})
	_ = lg2.Close()

	history, _, skipped, err := Load(path)
	if err != nil || len(history) != 2 || skipped != 1 {
		t.Errorf("history=%d skipped=%d err=%v; want 2, 1, nil", len(history), skipped, err)
	}
}

// ADR-0021 §2 guard: a compaction record whose index base may have been
// shifted by skipped lines is refused — replaying the wrong messages is
// worse than refusing.
func TestCompactionAfterSkippedLinesRefused(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	id := lg.ID()
	path := lg.Path()
	for i := 0; i < 4; i++ {
		_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "m"})
	}
	_ = lg.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.WriteString("corrupt\n")
	_ = f.Close()
	lg2, _ := Reopen(dir, "/p", id)
	_ = lg2.Log(KindCompaction, Compaction{Replaced: 2, Message: llm.Message{Role: llm.RoleUser, Content: "summary"}})
	_ = lg2.Close()

	if _, _, _, err := Load(path); err == nil || !strings.Contains(err.Error(), "unreadable lines precede a compaction") {
		t.Errorf("Load = %v — a compaction after skipped lines must refuse, not guess", err)
	}
}

// ADR-0021 §1: a clear record replays as "history empties here", so a
// cleared session resumes cleared — and post-clear compaction indices
// are relative to the fresh history.
func TestClearRecordReplays(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "discarded one"})
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleAssistant, Content: "discarded two"})
	_ = lg.Log(KindClear, map[string]any{"messages": 2})
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "kept"})
	_ = lg.Close()

	history, _, skipped, err := Load(lg.Path())
	if err != nil || skipped != 0 {
		t.Fatalf("Load: skipped=%d err=%v", skipped, err)
	}
	if len(history) != 1 || history[0].Content != "kept" {
		t.Errorf("history = %+v — the cleared conversation must not resurrect", history)
	}
}

func TestClearResetsCompactionSkipGuard(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	id := lg.ID()
	path := lg.Path()
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "old"})
	_ = lg.Close()
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.WriteString("corrupt line before clear\n")
	_ = f.Close()
	lg2, _ := Reopen(dir, "/p", id)
	_ = lg2.Log(KindClear, nil)
	for i := 0; i < 3; i++ {
		_ = lg2.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "fresh"})
	}
	_ = lg2.Log(KindCompaction, Compaction{Replaced: 2, Message: llm.Message{Role: llm.RoleUser, Content: "sum"}})
	_ = lg2.Close()

	history, _, skipped, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v — skips before a clear must not poison post-clear compaction", err)
	}
	if skipped != 1 || len(history) != 2 {
		t.Errorf("history=%d skipped=%d; want 2 (summary + 1 kept) and 1", len(history), skipped)
	}
}

// ADR-0021 §4: the second process resuming a live session is refused.
func TestConcurrentReopenRefused(t *testing.T) {
	dir := t.TempDir()
	lg := openWithHeader(t, dir)
	id := lg.ID()
	defer func() { _ = lg.Close() }()

	if _, err := Reopen(dir, "/p", id); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("Reopen while the session is open = %v, want an in-use refusal", err)
	}
	_ = lg.Close()
	lg2, err := Reopen(dir, "/p", id)
	if err != nil {
		t.Errorf("Reopen after close: %v — the lock must die with the file", err)
	} else {
		_ = lg2.Close()
	}
}

// An old (schema 1) file still loads: the bump only fences old builds
// off files that contain records they would misread.
func TestSchemaOneFileStillLoads(t *testing.T) {
	dir := t.TempDir()
	lg, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	_ = lg.Log(KindHeader, Header{Schema: 1, Project: "/p", Model: "m"})
	_ = lg.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "old but fine"})
	_ = lg.Close()
	history, header, _, err := Load(lg.Path())
	if err != nil || len(history) != 1 || header.Schema != 1 {
		t.Errorf("schema-1 load: history=%d header=%+v err=%v", len(history), header, err)
	}
}

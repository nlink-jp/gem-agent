package cmd

import (
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
)

// seed writes a complete session transcript and returns its id.
func seed(t *testing.T, dir, project, model string, msgs ...llm.Message) string {
	t.Helper()
	lg, err := openSessionLog(dir, "", project, model, "global", "v0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	for _, m := range msgs {
		if err := lg.Log(session.KindMessage, m); err != nil {
			t.Fatal(err)
		}
	}
	return lg.ID()
}

func TestResolveResumeLoadsTheMostRecentSessionOfThisProject(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/proj", "gemini-x",
		llm.Message{Role: llm.RoleUser, Content: "where were we"},
		llm.Message{Role: llm.RoleAssistant, Content: "here"})

	meta, history, _, err := resolveAndLoad(dir, "/proj", "gemini-x", "")
	if err != nil {
		t.Fatal(err)
	}
	if meta.ID != id {
		t.Errorf("resumed %s, want %s", meta.ID, id)
	}
	if len(history) != 2 || history[0].Content != "where were we" {
		t.Errorf("history = %+v", history)
	}
}

// A transcript replayed in the wrong tree describes files that are not
// there, and carries one project's contents into another's context.
func TestResolveResumeRefusesAnotherProject(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/elsewhere", "gemini-x", llm.Message{Role: llm.RoleUser, Content: "hi"})

	_, _, _, err := resolveAndLoad(dir, "/proj", "gemini-x", id)
	if err == nil {
		t.Fatal("resumed a session recorded in a different project")
	}
	if !strings.Contains(err.Error(), "/elsewhere") {
		t.Errorf("error does not name the recorded project: %v", err)
	}

	// --continue must not even see it.
	if _, _, _, err := resolveAndLoad(dir, "/proj", "gemini-x", ""); err == nil ||
		!strings.Contains(err.Error(), "no previous session") {
		t.Errorf("--continue error = %v", err)
	}
}

// Thought signatures are model-bound opaque tokens; replaying one
// model's into another has no basis, and the failure would land after
// the operator thought they were back at work (ADR-0005).
func TestResolveResumeRefusesADifferentModel(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/proj", "gemini-recorded", llm.Message{Role: llm.RoleUser, Content: "hi"})

	_, _, _, err := resolveAndLoad(dir, "/proj", "gemini-other", id)
	if err == nil {
		t.Fatal("resumed a session recorded with a different model")
	}
	if !strings.Contains(err.Error(), "gemini-recorded") || !strings.Contains(err.Error(), "--model") {
		t.Errorf("error must name the recorded model and the way forward: %v", err)
	}
}

func TestResolveResumeRejectsPathsAndUnknownIDs(t *testing.T) {
	dir := t.TempDir()
	seed(t, dir, "/proj", "m", llm.Message{Role: llm.RoleUser, Content: "hi"})

	for _, id := range []string{"../../../etc/passwd", "/etc/passwd", "notanid"} {
		if _, _, _, err := resolveAndLoad(dir, "/proj", "m", id); err == nil {
			t.Errorf("resolveResume accepted %q as a session id", id)
		}
	}
	if _, _, _, err := resolveAndLoad(dir, "/proj", "m", "20200101-000000"); err == nil {
		t.Error("resolveResume accepted an id with no session behind it")
	}
}

func TestResolveResumeRejectsAnEmptyTranscript(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/proj", "m")
	if _, _, _, err := resolveAndLoad(dir, "/proj", "m", id); err == nil {
		t.Error("resumed a session with no conversation in it")
	}
}

func TestOpenSessionLogWritesAHeaderAndResumeAppends(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/proj", "gemini-x", llm.Message{Role: llm.RoleUser, Content: "one"})

	lg, err := openSessionLog(dir, id, "/proj", "gemini-x", "global", "v0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := lg.Log(session.KindMessage, llm.Message{Role: llm.RoleUser, Content: "two"}); err != nil {
		t.Fatal(err)
	}
	path := lg.Path()
	lg.Close()

	history, header, _, err := session.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// One file is one conversation: the resumed process appended rather
	// than starting a second transcript, and the header still describes
	// the session that began it.
	if len(history) != 2 || history[1].Content != "two" {
		t.Errorf("history = %+v", history)
	}
	if header.Model != "gemini-x" || header.Project != "/proj" || header.Schema != session.SchemaVersion {
		t.Errorf("header = %+v", header)
	}
	metas, err := session.List(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 {
		t.Errorf("%d sessions after a resume, want 1", len(metas))
	}
}

func TestWriteSessionsShowsIDAndPreview(t *testing.T) {
	dir := t.TempDir()
	id := seed(t, dir, "/proj", "gemini-x", llm.Message{Role: llm.RoleUser, Content: "fix the parser"})
	metas, err := session.List(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeSessions(&b, metas, false)
	out := b.String()
	for _, want := range []string{id, "fix the parser", "gemini-x", "--resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}
}

// --continue must land on the last session that has something in it.
func TestContinueSkipsConversationlessSessions(t *testing.T) {
	dir := t.TempDir()
	want := seed(t, dir, "/proj", "m", llm.Message{Role: llm.RoleUser, Content: "real work"})
	// A later run that only used slash commands: header, no conversation.
	empty := seed(t, dir, "/proj", "m")

	meta, history, _, err := resolveAndLoad(dir, "/proj", "m", "")
	if err != nil {
		t.Fatalf("--continue failed with an empty session present: %v", err)
	}
	if meta.ID != want {
		t.Errorf("resumed %s, want %s", meta.ID, want)
	}
	if len(history) != 1 {
		t.Errorf("history = %+v", history)
	}
	// Named explicitly, the empty one still reports what is actually wrong.
	if _, _, _, err := resolveAndLoad(dir, "/proj", "m", empty); err == nil ||
		!strings.Contains(err.Error(), "no conversation") {
		t.Errorf("explicit --resume of an empty session: %v", err)
	}
}

// resolveAndLoad mirrors runREPL's resume flow exactly: resolve the
// id, take the flock via Reopen, THEN load under it (review round 2 —
// loading before the lock raced another process's final appends).
func resolveAndLoad(dir, projectDir, model, id string) (session.Meta, []llm.Message, []string, error) {
	meta, err := resolveResume(dir, projectDir, model, id)
	if err != nil {
		return meta, nil, nil, err
	}
	lg, err := session.Reopen(dir, projectDir, meta.ID)
	if err != nil {
		return meta, nil, nil, err
	}
	defer lg.Close()
	history, notes, err := loadResumedHistory(lg, meta.ID)
	return meta, history, notes, err
}

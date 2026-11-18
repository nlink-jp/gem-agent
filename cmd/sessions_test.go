package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/session"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// seed writes a complete session transcript and returns its id.
func seed(t *testing.T, dir, project, model string, msgs ...llm.Message) string {
	t.Helper()
	lg, err := openSessionLog(dir, "", project, model, "global", "v0.0.0-test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lg.Close() }()
	for _, m := range msgs {
		if err := lg.Log(session.KindMessage, m); err != nil {
			t.Fatal(err)
		}
	}
	return lg.ID()
}

func TestResolveResumeLoadsTheMostRecentSessionOfThisProject(t *testing.T) {
	dir := sessionsDir(t)
	id := seed(t, dir, "/proj", "gemini-x",
		llm.Message{Role: llm.RoleUser, Content: "where were we"},
		llm.Message{Role: llm.RoleAssistant, Content: "here"})

	meta, history, _, err := resolveAndLoad(t, dir, "/proj", "gemini-x", "")
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
	dir := sessionsDir(t)
	id := seed(t, dir, "/elsewhere", "gemini-x", llm.Message{Role: llm.RoleUser, Content: "hi"})

	_, _, _, err := resolveAndLoad(t, dir, "/proj", "gemini-x", id)
	if err == nil {
		t.Fatal("resumed a session recorded in a different project")
	}
	if !strings.Contains(err.Error(), "/elsewhere") {
		t.Errorf("error does not name the recorded project: %v", err)
	}

	// --continue must not even see it.
	if _, _, _, err := resolveAndLoad(t, dir, "/proj", "gemini-x", ""); err == nil ||
		!strings.Contains(err.Error(), "no previous session") {
		t.Errorf("--continue error = %v", err)
	}
}

// Thought signatures are model-bound opaque tokens; replaying one
// model's into another has no basis, and the failure would land after
// the operator thought they were back at work (ADR-0005).
func TestResolveResumeRefusesADifferentModel(t *testing.T) {
	dir := sessionsDir(t)
	id := seed(t, dir, "/proj", "gemini-recorded", llm.Message{Role: llm.RoleUser, Content: "hi"})

	_, _, _, err := resolveAndLoad(t, dir, "/proj", "gemini-other", id)
	if err == nil {
		t.Fatal("resumed a session recorded with a different model")
	}
	if !strings.Contains(err.Error(), "gemini-recorded") || !strings.Contains(err.Error(), "--model") {
		t.Errorf("error must name the recorded model and the way forward: %v", err)
	}
}

func TestResolveResumeRejectsPathsAndUnknownIDs(t *testing.T) {
	dir := sessionsDir(t)
	seed(t, dir, "/proj", "m", llm.Message{Role: llm.RoleUser, Content: "hi"})

	for _, id := range []string{"../../../etc/passwd", "/etc/passwd", "notanid"} {
		if _, _, _, err := resolveAndLoad(t, dir, "/proj", "m", id); err == nil {
			t.Errorf("resolveResume accepted %q as a session id", id)
		}
	}
	if _, _, _, err := resolveAndLoad(t, dir, "/proj", "m", "20200101-000000"); err == nil {
		t.Error("resolveResume accepted an id with no session behind it")
	}
}

func TestResolveResumeRejectsAnEmptyTranscript(t *testing.T) {
	dir := sessionsDir(t)
	id := seed(t, dir, "/proj", "m")
	if _, _, _, err := resolveAndLoad(t, dir, "/proj", "m", id); err == nil {
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
	_ = lg.Close()

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
	metas, _, err := session.List(dir, "/proj")
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
	metas, _, err := session.List(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	writeSessions(&b, metas, false)
	out := b.String()
	// The listing shows the id shortened to what resume accepts (ADR-0071).
	for _, want := range []string{session.Short(id), "fix the parser", "gemini-x", "--resume"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing is missing %q:\n%s", want, out)
		}
	}
}

// --continue must land on the last session that has something in it.
func TestContinueSkipsConversationlessSessions(t *testing.T) {
	dir := sessionsDir(t)
	want := seed(t, dir, "/proj", "m", llm.Message{Role: llm.RoleUser, Content: "real work"})
	// A later run that only used slash commands: header, no conversation.
	empty := seed(t, dir, "/proj", "m")

	meta, history, _, err := resolveAndLoad(t, dir, "/proj", "m", "")
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
	if _, _, _, err := resolveAndLoad(t, dir, "/proj", "m", empty); err == nil ||
		!strings.Contains(err.Error(), "no conversation") {
		t.Errorf("explicit --resume of an empty session: %v", err)
	}
}

// resolveAndLoad runs the startup's own resume path, openTranscript —
// resolve the id, take the flock via Reopen, THEN load under it (review
// round 2: loading before the lock raced another process's final
// appends). A copy of that flow stood here until ADR-0124 made the phase
// callable; a copy can agree with itself while the original changes.
// dir must be the sessions directory of the state root sessionsDir set.
func resolveAndLoad(t *testing.T, dir, projectDir, model, id string) (session.Meta, []llm.Message, []string, error) {
	t.Helper()
	if want, err := session.DefaultDir(); err != nil || want != dir {
		t.Fatalf("sessions directory %q is not the state root's %q (%v): seed under sessionsDir", dir, want, err)
	}
	// openTranscript exports the session id, as a start does.
	t.Setenv(session.EnvVar, "")
	sess := newLiveSession()
	var stderr strings.Builder
	sess.stderr = &stderr
	sess.version = "v0.0.0-test"
	sess.projectDir = projectDir
	sess.cfg = &config.Config{}
	sess.cfg.Model.Name = model
	sess.cfg.GCP.Location = "global"
	cl := &cleanups{}
	defer cl.run()
	op, err := openTranscript(sess, cl, runFlags{continueLast: id == "", resume: id})
	var notes []string
	for _, l := range strings.Split(stderr.String(), "\n") {
		if n, ok := strings.CutPrefix(l, "warning: "); ok {
			notes = append(notes, n)
		}
	}
	return session.Meta{ID: op.resumedID, Started: op.resumedFrom}, op.restored, notes, err
}

// sessionsDir points the state root at a temporary directory for this
// test and returns its sessions directory, where openTranscript looks.
func sessionsDir(t *testing.T) string {
	t.Helper()
	t.Setenv(statedir.EnvRoot, t.TempDir())
	dir, err := session.DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// ADR-0101: --resume in the directory a session was recorded in works
// however the path was spelled this time.
func TestResumeAcceptsTheProjectUnderAnotherSpelling(t *testing.T) {
	const nfc, nfd = "ドライブ", "ドライブ"
	base := t.TempDir()
	project := filepath.Join(base, nfd)
	if err := os.Mkdir(project, 0o755); err != nil {
		t.Fatal(err)
	}
	typed := filepath.Join(base, nfc)
	if _, err := os.Stat(typed); err != nil {
		t.Skipf("this volume does not treat the two spellings as one name: %v", err)
	}
	dir := sessionsDir(t)
	lg, err := session.Open(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	_ = lg.Log(session.KindHeader, session.Header{Schema: session.SchemaVersion, Model: "m", Project: project})
	_ = lg.Log(session.KindMessage, llm.Message{Role: llm.RoleUser, Content: "hello"})
	id := lg.ID()
	_ = lg.Close()
	if _, history, _, err := resolveAndLoad(t, dir, typed, "m", id); err != nil || len(history) != 1 {
		t.Errorf("resume under the other spelling: history %d, err %v", len(history), err)
	}
	other := filepath.Join(base, "other")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveResume(dir, other, "m", id); err == nil {
		t.Error("a session resumed in a different directory")
	}
}

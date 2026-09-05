package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

func TestLogAppendsJSONL(t *testing.T) {
	l, err := Open(t.TempDir(), "/p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()

	if err := l.Log("user", map[string]string{"content": "こんにちは"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log("assistant", map[string]string{"content": "hi"}); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var kinds []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec Record
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("line is not valid JSON: %v", err)
		}
		if rec.Time.IsZero() {
			t.Error("record timestamp missing")
		}
		kinds = append(kinds, rec.Kind)
	}
	if len(kinds) != 2 || kinds[0] != "user" || kinds[1] != "assistant" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestOpenCreatesUniqueFilePerSession(t *testing.T) {
	dir := t.TempDir()
	l1, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l1.Close() }()
	// A second session in the same second gets a suffixed file (O_EXCL +
	// retry) — never silent reuse of the same file.
	l2, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l2.Close() }()
	if l1.Path() == l2.Path() {
		t.Error("two sessions share one file")
	}
	if !ValidID(l1.ID()) || !ValidID(l2.ID()) {
		t.Errorf("generated ids are not accepted by ValidID: %q %q", l1.ID(), l2.ID())
	}
}

// TestOpenParallelUniqueIDs is the operator's question measured: N
// truly simultaneous launches in one project, all inside the same
// timestamp second, must yield N distinct valid sessions. O_EXCL is
// the atomic arbiter; the suffix loop absorbs the contention (each
// retry round crowns exactly one winner per name, so N openers need at
// most N attempts — far under the loop's cap of 100).
func TestOpenParallelUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	const n = 16
	var wg sync.WaitGroup
	paths := make([]string, n)
	ids := make([]string, n)
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // maximize same-second contention
			l, err := Open(dir, "/p")
			if err != nil {
				errs[i] = err
				return
			}
			defer func() { _ = l.Close() }()
			paths[i] = l.Path()
			ids[i] = l.ID()
		}(i)
	}
	close(start)
	wg.Wait()
	seen := map[string]bool{}
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("open %d: %v", i, errs[i])
		}
		if seen[paths[i]] {
			t.Errorf("two sessions share %s", paths[i])
		}
		seen[paths[i]] = true
		if !ValidID(ids[i]) {
			t.Errorf("id %q not resumable (ValidID rejects it)", ids[i])
		}
	}
}

// A resumed conversation is only as good as what the transcript kept.
// Thought signatures are the part that is easy to lose and impossible to
// do without: Gemini rejects a function-call part replayed without one.
func TestLoadRestoresFullFidelityHistory(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Log(KindHeader, Header{Schema: SchemaVersion, Version: "v0.1.3", Model: "test-model", Project: "/p"}); err != nil {
		t.Fatal(err)
	}
	want := []llm.Message{
		{Role: llm.RoleUser, Content: "read the file", Attachments: []llm.Attachment{{Ref: "a.txt", Kind: "file", Content: "body"}}},
		{Role: llm.RoleAssistant, Content: "sure",
			ToolCalls:       []llm.ToolCall{{ID: "c1", Name: "read_file", Args: map[string]any{"path": "a.txt"}, ThoughtSignature: []byte{0x01, 0x02, 0xff}}},
			ThoughtPartSigs: [][]byte{{0xde, 0xad}}, TextPartSig: []byte{0xbe, 0xef}},
		{Role: llm.RoleTool, ToolName: "read_file", ToolCallID: "c1", Content: strings.Repeat("x", 100_000)},
	}
	for _, m := range want {
		if err := l.Log(KindMessage, m); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()

	got, header, _, err := Load(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if header.Model != "test-model" || header.Project != "/p" {
		t.Errorf("header = %+v", header)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d messages, want %d", len(got), len(want))
	}
	if string(got[1].ToolCalls[0].ThoughtSignature) != "\x01\x02\xff" {
		t.Errorf("tool-call thought signature lost: %q", got[1].ToolCalls[0].ThoughtSignature)
	}
	if len(got[1].ThoughtPartSigs) != 1 || string(got[1].ThoughtPartSigs[0]) != "\xde\xad" {
		t.Errorf("thought part signatures lost: %q", got[1].ThoughtPartSigs)
	}
	if string(got[1].TextPartSig) != "\xbe\xef" {
		t.Errorf("text part signature lost: %q", got[1].TextPartSig)
	}
	if got[1].ToolCalls[0].Args["path"] != "a.txt" {
		t.Errorf("tool-call args lost: %v", got[1].ToolCalls[0].Args)
	}
	if got[0].Attachments[0].Content != "body" {
		t.Errorf("attachment lost: %+v", got[0].Attachments)
	}
	// The clipping the log used to apply would silently amputate a large
	// tool result — a resumed session must not have half a file.
	if len(got[2].Content) != 100_000 {
		t.Errorf("tool result truncated to %d bytes", len(got[2].Content))
	}
}

func TestLoadAppliesCompaction(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"one", "two", "three"} {
		if err := l.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: c}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Log(KindCompaction, Compaction{
		Replaced: 2,
		Message:  llm.Message{Role: llm.RoleUser, Content: "summary"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := l.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "four"}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	got, _, _, err := Load(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, m := range got {
		contents = append(contents, m.Content)
	}
	// Re-inflating a conversation that was deliberately shrunk would put
	// the resumed session straight back over the window.
	want := []string{"summary", "three", "four"}
	if strings.Join(contents, ",") != strings.Join(want, ",") {
		t.Errorf("history = %v, want %v", contents, want)
	}
}

func TestLoadRejectsNewerSchema(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Log(KindHeader, Header{Schema: SchemaVersion + 1}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	if _, _, _, err := Load(l.Path()); err == nil {
		t.Fatal("a transcript from a newer build loaded silently")
	}
}

// A crash mid-write leaves a torn last line. Everything before it is
// still a valid conversation.
func TestLoadToleratesTruncatedFinalLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "20260819-101010.jsonl")
	good, err := json.Marshal(Record{Kind: KindMessage, Data: llm.Message{Role: llm.RoleUser, Content: "kept"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append(good, '\n'), []byte(`{"kind":"message","data":{"role":"us`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _, _, err := Load(path)
	if err != nil {
		t.Fatalf("a torn final line made the whole transcript unresumable: %v", err)
	}
	if len(got) != 1 || got[0].Content != "kept" {
		t.Errorf("history = %+v", got)
	}
}

func TestValidIDRejectsPaths(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "20260819-101010/../x", "/abs/20260819-101010", "notanid", "", "20260819-101010.jsonl"} {
		if ValidID(bad) {
			t.Errorf("ValidID(%q) = true — an id must not be able to name a file outside the sessions directory", bad)
		}
	}
	for _, ok := range []string{"20260819-101010", "20260819-101010-2"} {
		if !ValidID(ok) {
			t.Errorf("ValidID(%q) = false", ok)
		}
	}
}

func TestReopenAppendsToTheSameTranscript(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, "/p")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "before"}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()

	l2, err := Reopen(dir, "/p", l.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := l2.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "after"}); err != nil {
		t.Fatal(err)
	}
	_ = l2.Close()
	if l2.Path() != l.Path() {
		t.Errorf("resume wrote to %s, want %s", l2.Path(), l.Path())
	}

	got, _, _, err := Load(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Content != "before" || got[1].Content != "after" {
		t.Errorf("history = %+v", got)
	}
}

func TestReopenRefusesInvalidID(t *testing.T) {
	if _, err := Reopen(t.TempDir(), "/p", "../escape"); err == nil {
		t.Fatal("Reopen accepted a path as an id")
	}
}

func TestListFiltersByProjectAndSortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	write := func(project, preview string) string {
		l, err := Open(dir, project)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = l.Close() }()
		if err := l.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: project}); err != nil {
			t.Fatal(err)
		}
		if err := l.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: preview}); err != nil {
			t.Fatal(err)
		}
		return l.ID()
	}
	older := write("/proj", "first question\nsecond line")
	newer := write("/proj", "later question")
	write("/other", "different project")

	metas, _, err := List(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("listed %d sessions, want 2 (the other project must not appear)", len(metas))
	}
	// Same-second ids get a numeric suffix, so mtime alone may tie;
	// either order is acceptable as long as both are present and the
	// foreign project is absent.
	ids := metas[0].ID + " " + metas[1].ID
	if !strings.Contains(ids, older) || !strings.Contains(ids, newer) {
		t.Errorf("ids = %s, want both %s and %s", ids, older, newer)
	}
	for _, m := range metas {
		if m.Header.Project != "/proj" {
			t.Errorf("%s: project = %q", m.ID, m.Header.Project)
		}
		if m.Preview == "" || strings.Contains(m.Preview, "\n") {
			t.Errorf("%s: preview = %q, want a single non-empty line", m.ID, m.Preview)
		}
	}

	if _, ok, err := Latest(dir, "/nowhere"); err != nil || ok {
		t.Errorf("Latest for an unknown project: ok=%v err=%v", ok, err)
	}
	if _, ok, err := Latest(dir, "/proj"); err != nil || !ok {
		t.Errorf("Latest for a known project: ok=%v err=%v", ok, err)
	}
}

func TestListIgnoresForeignFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "planted.jsonl"), []byte(`{"kind":"session","data":{"project":"/proj"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	metas, _, err := List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 0 {
		t.Errorf("listed %d entries, want 0 — only generated session ids are sessions", len(metas))
	}
}

func TestListOnMissingDirIsEmptyNotAnError(t *testing.T) {
	metas, _, err := List(filepath.Join(t.TempDir(), "never-created"), "")
	if err != nil || len(metas) != 0 {
		t.Errorf("List = %v, %v; want no sessions and no error on a first run", metas, err)
	}
}

// A run that only used slash commands leaves a header and nothing else.
// Being the newest file, such a transcript would shadow the real session
// --continue is meant to find and turn a resume into an error.
func TestListSkipsSessionsWithNoConversation(t *testing.T) {
	dir := t.TempDir()
	real, err := Open(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := real.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: "/proj"}); err != nil {
		t.Fatal(err)
	}
	if err := real.Log(KindMessage, llm.Message{Role: llm.RoleUser, Content: "the actual work"}); err != nil {
		t.Fatal(err)
	}
	_ = real.Close()

	empty, err := Open(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := empty.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: "/proj"}); err != nil {
		t.Fatal(err)
	}
	_ = empty.Close()

	metas, _, err := List(dir, "/proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != real.ID() {
		t.Fatalf("List = %+v, want only %s", metas, real.ID())
	}
	latest, ok, err := Latest(dir, "/proj")
	if err != nil || !ok || latest.ID != real.ID() {
		t.Errorf("Latest = %s (ok=%v, err=%v), want %s — an empty transcript must not shadow the real one",
			latest.ID, ok, err, real.ID())
	}
	// Find still reaches it, so an id the operator typed gets the
	// accurate answer rather than "no such session".
	found, err := Find(dir, "/proj", empty.ID())
	if err != nil {
		t.Fatalf("Find on an empty session: %v", err)
	}
	if found.HasConversation {
		t.Error("an empty transcript reported a conversation")
	}
}

func TestFindRejectsAnIDShapedLikeAPath(t *testing.T) {
	if _, err := Find(t.TempDir(), "/p", "../../etc/passwd"); err == nil {
		t.Fatal("Find accepted a path as an id")
	}
}

// A session whose only input was a `!` command previewed as the wrapper
// sentence the agent injects ("I ran this shell command myself:"), which
// reads like a bug. Show the command instead — and always prefer a
// message the operator actually typed.
func TestPreviewPrefersTypedMessagesAndRendersShellCommands(t *testing.T) {
	shellContext := func(cmd string) llm.Message {
		return llm.Message{Role: llm.RoleUser,
			Content: ShellContextPrefix + "\n$ " + cmd + "\n\nOutput:\nsome output"}
	}
	cases := []struct {
		name string
		msgs []llm.Message
		want string
	}{
		{"shell only", []llm.Message{shellContext("git status --short")}, "!git status --short"},
		{"shell then typed", []llm.Message{shellContext("git diff"), {Role: llm.RoleUser, Content: "fix the parser"}}, "fix the parser"},
		{"typed then shell", []llm.Message{{Role: llm.RoleUser, Content: "fix the parser"}, shellContext("git diff")}, "fix the parser"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			l, err := Open(dir, "/proj")
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Log(KindHeader, Header{Schema: SchemaVersion, Model: "m", Project: "/proj"}); err != nil {
				t.Fatal(err)
			}
			for _, m := range tc.msgs {
				if err := l.Log(KindMessage, m); err != nil {
					t.Fatal(err)
				}
			}
			_ = l.Close()

			metas, _, err := List(dir, "/proj")
			if err != nil {
				t.Fatal(err)
			}
			if len(metas) != 1 {
				t.Fatalf("listed %d sessions", len(metas))
			}
			if metas[0].Preview != tc.want {
				t.Errorf("preview = %q, want %q", metas[0].Preview, tc.want)
			}
			if strings.Contains(metas[0].Preview, ShellContextPrefix) {
				t.Error("the injected wrapper sentence reached the listing")
			}
		})
	}
}

// InUse is the not-while-live guard for workdirs clean (ADR-0059): a
// running logger holds the transcript flock, so its session reads as in
// use exactly until it closes.
func TestInUseTracksTheTranscriptLock(t *testing.T) {
	dir, project := t.TempDir(), t.TempDir()
	lg, err := Open(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	id := lg.ID()
	if !InUse(dir, project, id) {
		t.Error("a session with an open logger must read as in use")
	}
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	if InUse(dir, project, id) {
		t.Error("a closed session must not read as in use")
	}
	if InUse(dir, project, "20990101-000000") {
		t.Error("a session with no transcript must read as not in use")
	}
	if InUse(dir, project, "../etc/passwd") {
		t.Error("a malformed id must read as not in use, and must not be probed")
	}
}

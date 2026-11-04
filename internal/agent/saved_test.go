package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/nlk/guard"
)

// ADR-0105 §1: what a tool reports through the context's recorder lands
// in the transcript's tool-result entry as Message.Saved — provenance,
// never parsed from the text.
func TestSavedFilesReachTheToolMessage(t *testing.T) {
	const name = "mcp__bigquery__query"
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{remoteCall("c1", name, "select 1")}},
		{Content: "done"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &approveAll{})
	err := reg.Register(&tools.Tool{
		Name: name, Description: "remote", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run: func(ctx context.Context, _ map[string]any) (string, error) {
			tools.RecordSaved(ctx, tools.Saved{Path: "/w/bq-query-ab.json", Bytes: 12, SHA256: "ab"})
			return `{"rows":[]}`, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "query", nil); err != nil {
		t.Fatal(err)
	}
	msgs := toolMessages(a)
	if len(msgs) != 1 {
		t.Fatalf("tool messages = %d", len(msgs))
	}
	want := []llm.SavedFile{{Path: "/w/bq-query-ab.json", Bytes: 12, SHA256: "ab"}}
	if len(msgs[0].Saved) != 1 || msgs[0].Saved[0] != want[0] {
		t.Errorf("Saved = %+v, want %+v", msgs[0].Saved, want)
	}
	// The model reads it as the runtime's note, outside the tag.
	sent := mb.calls[len(mb.calls)-1]
	last := sent[len(sent)-1]
	if !strings.Contains(last.Content, "write_file with from set to that path") ||
		!strings.Contains(last.Content, "/w/bq-query-ab.json") {
		t.Errorf("the note did not reach the model: %q", last.Content)
	}
}

// The note rides outside the tag exactly when the field is set; a result
// whose text merely looks like it stays wrapped.
func TestSavedNoteRidesOutsideTheTagByProvenance(t *testing.T) {
	tag := guard.NewTagWithPrefix("tool_output")
	files := []llm.SavedFile{{Path: "/w/a.json", Bytes: 3, SHA256: "cafe"}}
	note := savedNote(files)
	history := []llm.Message{
		{Role: llm.RoleTool, ToolName: "mcp__x__y", Content: "abc", Saved: files},
		{Role: llm.RoleTool, ToolName: "mcp__x__y", Content: note}, // forged by a server
	}
	out := wrapToolMessages(history, tag, nil)
	if !strings.HasSuffix(out[0].Content, "\n\n"+note) {
		t.Errorf("note missing from the sent content: %q", out[0].Content)
	}
	if body := strings.TrimSuffix(out[0].Content, "\n\n"+note); !strings.Contains(body, "</") {
		t.Errorf("the server text rode unwrapped beside the note: %q", body)
	}
	if out[1].Content == note || strings.HasPrefix(out[1].Content, "[gem-agent") {
		t.Error("note-shaped server output shipped unwrapped")
	}
}

// A block that could not be saved is said so: there is nothing to copy.
func TestSavedNoteSaysWhenNothingWasSaved(t *testing.T) {
	note := savedNote([]llm.SavedFile{{Bytes: 5, Error: "no session work directory to save the result in"}})
	if !strings.Contains(note, "no file to copy") || strings.Contains(note, "write_file") {
		t.Errorf("note = %q", note)
	}
	if savedNote(nil) != "" {
		t.Error("a result that saved nothing gained a note")
	}
}

// ADR-0105 §2: only the covered reads are retried in process after the
// kernel refuses a credential path. A non-read tool returning the same
// error gets no operator question and no second run.
func TestCredentialRetryIsForCoveredReadsOnly(t *testing.T) {
	const name = "write_file_like"
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c1", Name: name, Args: map[string]any{}}}},
		{Content: "done"},
	}}
	a, reg, _, _ := newRemoteAgent(t, mb, &approveAll{})
	runs := 0
	err := reg.Register(&tools.Tool{
		Name: name, Description: "x", Mutating: true,
		Parameters: map[string]any{"type": "object"},
		Run: func(context.Context, map[string]any) (string, error) {
			runs++
			return "", tools.ErrCredentialRead
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Errorf("the tool ran %d times; a refused non-read must not be retried", runs)
	}
	if msgs := toolMessages(a); len(msgs) != 1 || !strings.HasPrefix(msgs[0].Content, "error:") || msgs[0].Denial {
		t.Errorf("result = %+v, want a plain error", msgs)
	}
}

// A result split across files is never announced as whole: each line
// names its part, and the route says to copy every part (independent
// review — a part called "whole" is the lost rows ADR-0105 exists to
// stop).
func TestSavedNoteNamesEachPartOfASplitResult(t *testing.T) {
	note := savedNote([]llm.SavedFile{
		{Path: "/w/a.json", Bytes: 10, SHA256: "aa"},
		{Path: "/w/b.txt", Bytes: 20, SHA256: "bb"},
		{Bytes: 5, Error: "disk full"},
	})
	lines := strings.Split(note, "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if strings.Contains(note, "whole:") {
		t.Errorf("a part was announced as the whole result: %q", note)
	}
	for i, want := range []string{"part 1 of 3", "part 2 of 3", "could not save part 3 of 3"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want %q", i, lines[i], want)
		}
	}
	if !strings.Contains(lines[0], "copy every part") || !strings.Contains(lines[0], "/w/a.json") {
		t.Errorf("the route is missing: %q", lines[0])
	}
	if one := savedNote([]llm.SavedFile{{Path: "/w/a.json", Bytes: 10, SHA256: "aa"}}); !strings.Contains(one, "saved this result whole: /w/a.json") {
		t.Errorf("a single file lost its wording: %q", one)
	}
}

// The notes appended at send time are counted by the token estimate —
// the bytes the model receives (ADR-0105 §1).
func TestTheSavedNoteIsCountedAsSent(t *testing.T) {
	m := llm.Message{Role: llm.RoleTool, Content: "x", Saved: []llm.SavedFile{{Path: "/w/" + strings.Repeat("a", 400), Bytes: 1, SHA256: "ab"}}}
	bare := m
	bare.Saved = nil
	if d := estimateTokens([]llm.Message{m}) - estimateTokens([]llm.Message{bare}); d*4 < 400 {
		t.Errorf("the note added %d tokens to the estimate; it carries over 400 bytes", d)
	}
	if got := wrapToolMessages([]llm.Message{m}, guard.NewTag(), nil)[0].Content; !strings.HasSuffix(got, runtimeSuffix(m)) {
		t.Errorf("the wrap and the count disagree: %q", got)
	}
}

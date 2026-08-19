package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
)

// fakeBackend records the request and replays one response.
type fakeBackend struct {
	resp     *llm.Response
	system   string
	messages []llm.Message
	toolDefs []llm.ToolDef
}

func (f *fakeBackend) ChatStream(ctx context.Context, system string, messages []llm.Message, defs []llm.ToolDef, onText func(string)) (*llm.Response, error) {
	f.system, f.messages, f.toolDefs = system, messages, defs
	return f.resp, nil
}

func summarizeSetup(t *testing.T, resp *llm.Response) (*tools.Registry, *fakeBackend) {
	t.Helper()
	reg, err := tools.New(t.TempDir(), func(ctx context.Context, c string) *exec.Cmd {
		return exec.CommandContext(ctx, "/bin/bash", "-c", c)
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	fb := &fakeBackend{resp: resp}
	if err := registerSummarizeTool(reg, fb, "light-model", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "doc.md"),
		[]byte("# Title\n\nBody with the word heron in it.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return reg, fb
}

// The compaction pattern applied to one file: content nonce-wrapped,
// defensive framing first, no tools offered (ADR-0014 §5).
func TestSummarizeFileIsolatesContentAndNamesItself(t *testing.T) {
	reg, fb := summarizeSetup(t, &llm.Response{Content: "A doc about herons."})
	tool, _ := reg.Get("summarize_file")
	out, err := tool.Run(context.Background(), map[string]any{"path": "doc.md", "focus": "birds"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Summary of doc.md (by light-model") || !strings.Contains(out, "lossy") {
		t.Errorf("result must name the file, the model, and the lossiness: %q", out)
	}
	if !strings.Contains(out, "A doc about herons.") {
		t.Errorf("summary text missing: %q", out)
	}
	if len(fb.toolDefs) != 0 {
		t.Error("the summariser was offered tools")
	}
	if !strings.Contains(fb.system, "UNTRUSTED DATA") {
		t.Error("defensive framing missing from the summariser prompt")
	}
	sent := fb.messages[0].Content
	if !strings.Contains(sent, "Focus: birds") {
		t.Errorf("focus not passed: %q", sent)
	}
	if !strings.Contains(sent, "heron") || !strings.Contains(sent, "<file") {
		t.Errorf("content not delivered nonce-wrapped: %.120q", sent)
	}
	if strings.Contains(fb.system, "heron") {
		t.Error("file content leaked into the system prompt")
	}
}

// A blocked or empty summary is a reported error, never a silent empty
// summary the model would take as "the file says nothing".
func TestSummarizeFileEmptyResponseIsAnError(t *testing.T) {
	reg, _ := summarizeSetup(t, &llm.Response{BlockReason: "PROHIBITED_CONTENT"})
	tool, _ := reg.Get("summarize_file")
	_, err := tool.Run(context.Background(), map[string]any{"path": "doc.md"})
	if err == nil || !strings.Contains(err.Error(), "PROHIBITED_CONTENT") {
		t.Fatalf("err = %v, want the block reason named", err)
	}
	if !strings.Contains(err.Error(), "read the file directly") {
		t.Errorf("error should name the fallback: %v", err)
	}
}

// Confinement and the image refusal come free by riding read_file.
func TestSummarizeFileInheritsReadFileRules(t *testing.T) {
	reg, _ := summarizeSetup(t, &llm.Response{Content: "s"})
	tool, _ := reg.Get("summarize_file")
	if _, err := tool.Run(context.Background(), map[string]any{"path": "../outside.md"}); err == nil {
		t.Fatal("summarize_file escaped the project")
	}
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "x.png"), []byte("png?"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.Run(context.Background(), map[string]any{"path": "x.png"}); err == nil || !strings.Contains(err.Error(), "view_image") {
		t.Fatalf("image handling: %v", err)
	}
}

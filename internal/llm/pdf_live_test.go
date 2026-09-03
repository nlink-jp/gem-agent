//go:build live

package llm

import (
	"context"
	"os"
	"strings"
	"testing"

	"google.golang.org/genai"
)

// Measures the two candidate carriers for PDF bytes (ADR-0026): a user
// message part, and a multimodal function response part across a
// further round. Temporary review harness — not part of the suite.
func TestPDFCarriers(t *testing.T) {
	ctx := context.Background()
	project := os.Getenv("GEM_TEST_PROJECT")
	pdfPath := os.Getenv("GEM_TEST_PDF")
	if project == "" || pdfPath == "" {
		t.Skip("GEM_TEST_PROJECT / GEM_TEST_PDF unset")
	}
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVertex(ctx, project, "global", "gemini-3.8-flash", "off", "", false)
	if err != nil {
		t.Fatal(err)
	}

	// Shape A: PDF as a user-message part.
	contents := []*genai.Content{{Role: genai.RoleUser, Parts: []*genai.Part{
		genai.NewPartFromText("What is the magic token in the attached PDF? Answer with just the token."),
		genai.NewPartFromBytes(pdf, "application/pdf"),
	}}}
	respA, errA := v.client.Models.GenerateContent(ctx, v.model, contents, nil)
	if errA != nil {
		t.Logf("shape A (user part): err=%v", errA)
	} else {
		t.Logf("shape A (user part): answer=%q", respA.Text())
	}

	// Shape B: PDF inside a multimodal function response, then one more
	// round on top (the latent-400 pattern from ADR-0012 fired on the
	// round AFTER acceptance).
	tools := []ToolDef{{
		Name:        "read_document",
		Description: "Reads a document file from the project and returns its content.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		},
	}}
	system := "You are a test assistant. Use read_document to read files the user names."
	user1 := Message{Role: RoleUser, Content: "Read doc.pdf and tell me the magic token. Answer with just the token."}
	resp, err := v.ChatStream(ctx, system, []Message{user1}, tools, nil)
	if err != nil || len(resp.ToolCalls) == 0 {
		t.Fatalf("round 1: err=%v calls=%d", err, len(resp.ToolCalls))
	}
	history := []Message{user1, {
		Role: RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls,
		ThoughtPartSigs: resp.ThoughtPartSigs, TextPartSig: resp.TextPartSig,
	}, {
		Role: RoleTool, ToolName: resp.ToolCalls[0].Name, ToolCallID: resp.ToolCalls[0].ID,
		Content:     "document doc.pdf follows as an attached part",
		Attachments: []Attachment{{Ref: "doc.pdf", Kind: "document", Data: pdf, MIME: "application/pdf"}},
	}}
	respB, errB := v.ChatStream(ctx, system, history, tools, nil)
	if errB != nil {
		t.Logf("shape B round 2 (FR part): err=%v", errB)
		return
	}
	t.Logf("shape B round 2: answer=%q", respB.Content)
	if !strings.Contains(respB.Content, "QX7442") {
		t.Logf("NOTE: token not found in FR-part answer")
	}
	// Round 3: continue past the FR round.
	history = append(history, Message{
		Role: RoleAssistant, Content: respB.Content, ToolCalls: respB.ToolCalls,
		ThoughtPartSigs: respB.ThoughtPartSigs, TextPartSig: respB.TextPartSig,
	}, Message{Role: RoleUser, Content: "And who is the reviewer named in the same document?"})
	respC, errC := v.ChatStream(ctx, system, history, tools, nil)
	if errC != nil {
		t.Logf("shape B round 3: err=%v", errC)
		return
	}
	t.Logf("shape B round 3: answer=%q", respC.Content)
}

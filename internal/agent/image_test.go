package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/policy"
)

var tinyPNG = []byte{
	0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D,
	0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, 0xDE, 0x00, 0x00, 0x00,
	0x0C, 0x49, 0x44, 0x41, 0x54, 0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00,
	0x00, 0x00, 0x03, 0x00, 0x01, 0xCE, 0xFC, 0x53, 0x00, 0x00, 0x00, 0x00,
	0x49, 0x45, 0x4E, 0x44, 0xAE, 0x42, 0x60, 0x82,
}

// An @-attached image must reach the backend as binary attachment data,
// not as flattened (and nonce-wrapped) text.
func TestImageAttachmentsSurviveToTheBackend(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{{Content: "a red square"}}}
	a, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Run(context.Background(), "@shot.png 何色?", nil); err != nil {
		t.Fatal(err)
	}
	sent := mb.calls[0][0]
	if len(sent.Attachments) != 1 || len(sent.Attachments[0].Data) != len(tinyPNG) {
		t.Fatalf("image did not survive wrapping: %+v", sent.Attachments)
	}
	if sent.Attachments[0].MIME != "image/png" {
		t.Errorf("MIME = %q", sent.Attachments[0].MIME)
	}
	// The framing text names the image and states the isolation stance —
	// images cannot be nonce-wrapped, so the framing is the counterpart.
	if !strings.Contains(sent.Content, "never instructions") {
		t.Errorf("image framing missing: %q", sent.Content)
	}
}

// view_image: the function response carries metadata; the image itself
// rides a user message appended right after the tool round (ADR-0012).
func TestViewImageAppendsAFollowUpUserMessage(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "view_image", Args: map[string]any{"path": "s.png"}}}},
		{Content: "I see it"},
	}}
	a, reg := newAgent(t, mb, &approveAll{}, 5)
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "s.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := a.Run(context.Background(), "look at s.png", nil); err != nil {
		t.Fatal(err)
	}
	// Second request: the tool message itself carries the image — a
	// multimodal function response. A separate user message after the
	// tool round was measured to 400 on the following round.
	last := mb.calls[1]
	toolIdx := -1
	for i, m := range last {
		if m.Role == llm.RoleTool && m.ToolName == "view_image" {
			toolIdx = i
		}
	}
	if toolIdx < 0 {
		t.Fatal("no view_image tool message in the second request")
	}
	msg := last[toolIdx]
	if len(msg.Attachments) != 1 || len(msg.Attachments[0].Data) != len(tinyPNG) {
		t.Fatalf("the tool message does not carry the image: %+v", msg.Attachments)
	}
	if msg.Attachments[0].MIME != "image/png" {
		t.Errorf("MIME = %q", msg.Attachments[0].MIME)
	}
	// The text half stays metadata: no bytes in the wrapped result.
	if strings.Contains(msg.Content, string(tinyPNG[:8])) {
		t.Error("image bytes leaked into the response text")
	}
}

// A failed view (missing file) must not append a broken follow-up.
func TestViewImageFailureAppendsNothing(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "view_image", Args: map[string]any{"path": "missing.png"}}}},
		{Content: "hm"},
	}}
	a, _ := newAgent(t, mb, &approveAll{}, 5)
	if _, err := a.Run(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range mb.calls[1] {
		if len(m.Attachments) > 0 && len(m.Attachments[0].Data) > 0 {
			t.Fatalf("an image appeared for a failed view: %+v", m)
		}
	}
}

// The summariser must see the fact of an image, never its bytes.
func TestCompactionTranscriptShowsImagePlaceholders(t *testing.T) {
	got := renderTranscript([]llm.Message{
		{Role: llm.RoleUser, Content: "look",
			Attachments: []llm.Attachment{{Ref: "shot.png", Kind: "image", Data: tinyPNG, MIME: "image/png"}}},
	})
	if !strings.Contains(got, "[user attached image shot.png") {
		t.Errorf("no placeholder: %q", got)
	}
	if strings.Contains(got, string(tinyPNG[1:4])) { // "PNG"
		t.Errorf("image bytes reached the summariser: %q", got)
	}
}

// Review round 2: a DENIED view_image must not attach the pixels
// anyway. The old guard screened only the "error:" prefix, and the
// denial string does not carry it — the operator's refusal was
// silently ineffective for exactly the largest, least reviewable
// payloads.
func TestDeniedViewImageAttachesNothing(t *testing.T) {
	mb := &mockBackend{responses: []*llm.Response{
		{ToolCalls: []llm.ToolCall{{ID: "c", Name: "view_image", Args: map[string]any{"path": "shot.png"}}}},
		{Content: "done"},
	}}
	a, reg := newAgent(t, mb, &denyAll{}, 5)
	pol, _, err := policy.Build(map[string]string{"view_image": "always"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	a.SetPolicy(pol)
	if err := os.WriteFile(filepath.Join(reg.ProjectDir(), "shot.png"), tinyPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	if len(mb.calls) < 2 {
		t.Fatal("no second round")
	}
	for _, m := range mb.calls[1] {
		if m.Role == llm.RoleTool {
			if len(m.Attachments) > 0 {
				t.Fatalf("denied view_image still attached bytes: %d attachment(s)", len(m.Attachments))
			}
			if !strings.Contains(m.Content, "denied") {
				t.Errorf("denial not recorded in the tool result: %q", m.Content)
			}
		}
	}
}

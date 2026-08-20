package llm

import (
	"testing"
)

// ADR-0027 §4: a URI attachment becomes a file-data part; inline data
// stays an inline part.
func TestBuildContentsMediaParts(t *testing.T) {
	msgs := []Message{{
		Role:    RoleUser,
		Content: "この録音を要約して",
		Attachments: []Attachment{
			{Ref: "memo.m4a", Kind: "media", URI: "gs://b/gem-agent/media/x.m4a", MIME: "audio/mp4"},
			{Ref: "shot.png", Kind: "image", Data: []byte{1, 2, 3}, MIME: "image/png"},
		},
	}}
	contents := buildContents(msgs)
	if len(contents) != 1 {
		t.Fatalf("contents = %d", len(contents))
	}
	parts := contents[0].Parts
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want text + file-data + inline", len(parts))
	}
	if parts[1].FileData == nil || parts[1].FileData.FileURI != "gs://b/gem-agent/media/x.m4a" || parts[1].FileData.MIMEType != "audio/mp4" {
		t.Errorf("file-data part = %+v", parts[1].FileData)
	}
	if parts[2].InlineData == nil || parts[2].InlineData.MIMEType != "image/png" {
		t.Errorf("inline part = %+v", parts[2].InlineData)
	}
}

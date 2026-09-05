package mention

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeWAV is a minimal RIFF header so DetectContentType does not call
// it plain text.
func fakeWAV(size int) []byte {
	data := append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, size)...)
	return data
}

// ADR-0027 §2: a configured bucket always wins — even a tiny file is
// uploaded and attached by URI, with no bytes in the message.
func TestMediaBucketAlwaysWins(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "memo.wav")
	_ = os.WriteFile(path, fakeWAV(64), 0o644)

	var uploaded []string
	lim := DefaultLimits()
	lim.UploadMedia = func(_ context.Context, f *os.File, name, mime string) (string, error) {
		uploaded = append(uploaded, f.Name()+"|"+mime)
		return "gs://ops/gem-agent/media/abc.wav", nil
	}
	atts, problems := Expand(context.Background(), "聞いて @memo.wav", dir, "", lim)
	if len(problems) != 0 || len(atts) != 1 {
		t.Fatalf("atts=%d problems=%v", len(atts), problems)
	}
	a := atts[0]
	if a.Kind != "media" || a.URI != "gs://ops/gem-agent/media/abc.wav" || a.MIME != "audio/wav" || len(a.Data) != 0 {
		t.Errorf("attachment = %+v", a)
	}
	if len(uploaded) != 1 || !strings.HasSuffix(strings.Split(uploaded[0], "|")[0], "memo.wav") {
		t.Errorf("upload calls = %v", uploaded)
	}
}

// Without a bucket: inline under the cap, a named refusal above it —
// never a truncated media file.
func TestMediaInlineCapWithoutBucket(t *testing.T) {
	dir := realTempDir(t)
	small := filepath.Join(dir, "s.wav")
	_ = os.WriteFile(small, fakeWAV(64), 0o644)

	lim := DefaultLimits()
	lim.MediaBytes = 4096
	atts, problems := Expand(context.Background(), "@s.wav", dir, "", lim)
	if len(problems) != 0 || len(atts) != 1 || len(atts[0].Data) == 0 || atts[0].URI != "" {
		t.Fatalf("inline attach failed: %+v %v", atts, problems)
	}

	big := filepath.Join(dir, "b.mp4")
	_ = os.WriteFile(big, fakeWAV(8192), 0o644)
	_, problems = Expand(context.Background(), "@b.mp4", dir, "", lim)
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "[gcp].bucket") {
		t.Errorf("oversize refusal must name the bucket remedy: %v", problems)
	}
}

// Plain text under a media extension is screened out.
func TestMediaRejectsPlainText(t *testing.T) {
	dir := realTempDir(t)
	fake := filepath.Join(dir, "fake.mp3")
	_ = os.WriteFile(fake, []byte("this is just text pretending"), 0o644)
	_, problems := Expand(context.Background(), "@fake.mp3", dir, "", DefaultLimits())
	if len(problems) != 1 || !strings.Contains(problems[0].Reason, "not a media file") {
		t.Errorf("plain text accepted as media: %v", problems)
	}
}

// An upload failure is a reported problem, not a silent drop or an
// inline fallback (falling back would re-send megabytes every round).
func TestMediaUploadFailureReported(t *testing.T) {
	dir := realTempDir(t)
	path := filepath.Join(dir, "m.mov")
	_ = os.WriteFile(path, fakeWAV(64), 0o644)
	lim := DefaultLimits()
	lim.UploadMedia = func(_ context.Context, _ *os.File, _, _ string) (string, error) {
		return "", os.ErrPermission
	}
	atts, problems := Expand(context.Background(), "@m.mov", dir, "", lim)
	if len(atts) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Reason, "upload") {
		t.Errorf("atts=%v problems=%v", atts, problems)
	}
}

package mediastore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeStore struct {
	objects map[string][]byte
	types   map[string]string
	writes  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string][]byte{}, types: map[string]string{}}
}

func (s *fakeStore) Exists(ctx context.Context, object string) (bool, error) {
	_, ok := s.objects[object]
	return ok, nil
}

func (s *fakeStore) Write(ctx context.Context, object, contentType string, r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.objects[object] = data
	s.types[object] = contentType
	s.writes++
	return nil
}

// ADR-0027 §3: content-addressed object names, dedup by existence, and
// nothing ever deleted.
func TestUploadContentAddressedAndDeduped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memo.M4A")
	content := []byte("fake audio bytes")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	store := newFakeStore()
	u := newWithStore("ops-bucket", store)

	uri, err := u.Upload(context.Background(), path, "audio/mp4")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	want := "gs://ops-bucket/gem-agent/media/" + hex.EncodeToString(sum[:]) + ".m4a"
	if uri != want {
		t.Errorf("uri = %s, want %s", uri, want)
	}
	if store.writes != 1 {
		t.Fatalf("writes = %d", store.writes)
	}
	if store.types["gem-agent/media/"+hex.EncodeToString(sum[:])+".m4a"] != "audio/mp4" {
		t.Error("content type not set")
	}

	// Same content again — the upload is skipped, the URI identical.
	uri2, err := u.Upload(context.Background(), path, "audio/mp4")
	if err != nil || uri2 != uri {
		t.Fatalf("second upload: %s, %v", uri2, err)
	}
	if store.writes != 1 {
		t.Errorf("re-attachment re-uploaded (writes=%d)", store.writes)
	}

	// Different content, different object.
	path2 := filepath.Join(dir, "other.m4a")
	os.WriteFile(path2, []byte("different"), 0o644)
	uri3, err := u.Upload(context.Background(), path2, "audio/mp4")
	if err != nil || uri3 == uri {
		t.Errorf("distinct content collided: %s, %v", uri3, err)
	}
	if !strings.HasPrefix(uri3, "gs://ops-bucket/gem-agent/media/") {
		t.Errorf("object prefix wrong: %s", uri3)
	}
}

// hookStore lets a test mutate the world between Exists and the
// upload read — the review-round-2 window.
type hookStore struct {
	*fakeStore
	onWrite func()
}

func (s *hookStore) Write(ctx context.Context, object, contentType string, r io.Reader) error {
	if s.onWrite != nil {
		s.onWrite()
	}
	return s.fakeStore.Write(ctx, object, contentType, r)
}

// A file still being written (a recorder appending) between the hash
// pass and the upload pass must NOT be committed under the stale hash:
// the store is permanent and dedupe would serve the wrong bytes
// forever (review round 2).
func TestUploadRefusesFileChangedDuringUpload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rec.m4a")
	if err := os.WriteFile(path, []byte("original-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newFakeStore()
	u := newWithStore("b", &hookStore{fakeStore: fake, onWrite: func() {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("-appended-while-uploading")
		f.Close()
	}})
	_, err := u.Upload(context.Background(), path, "audio/mp4")
	if err == nil || !strings.Contains(err.Error(), "changed while uploading") {
		t.Fatalf("changed file uploaded without error: %v", err)
	}
	if len(fake.objects) != 0 {
		t.Errorf("mismatched content committed to the store: %v", len(fake.objects))
	}
}

func TestVerifyReaderPassesUnchangedContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.m4a")
	os.WriteFile(path, []byte("stable"), 0o600)
	fake := newFakeStore()
	u := newWithStore("b", fake)
	uri, err := u.Upload(context.Background(), path, "audio/mp4")
	if err != nil || uri == "" {
		t.Fatalf("clean upload failed: %v", err)
	}
	if fake.writes != 1 {
		t.Errorf("writes = %d", fake.writes)
	}
}

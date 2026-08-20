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

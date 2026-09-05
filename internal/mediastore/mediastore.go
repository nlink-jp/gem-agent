// Package mediastore uploads media files to the operator's GCS bucket
// and returns gs:// URIs Vertex reads natively (ADR-0027). Uploads are
// content-addressed (gem-agent/media/<sha256><ext>) so re-attaching the
// same recording never re-uploads, and nothing is ever deleted here —
// retention belongs to the operator's bucket lifecycle rules.
package mediastore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"

	"cloud.google.com/go/storage"
)

// objectStore is the slice of the GCS client the uploader needs —
// an interface so tests run against a fake.
type objectStore interface {
	// Exists reports whether the object is already in the bucket.
	Exists(ctx context.Context, object string) (bool, error)
	// Write streams r into the object with the given content type.
	Write(ctx context.Context, object, contentType string, r io.Reader) error
}

// Uploader stores media in one bucket under a fixed prefix.
type Uploader struct {
	bucket string
	store  objectStore
}

const objectPrefix = "gem-agent/media/"

// New creates an uploader on the operator's bucket, riding the same
// ADC credentials as the Vertex client. The quota project is pinned to
// the CONFIGURED project rather than whatever the ADC file carries: a
// stale quota_project_id in ADC 404s every storage call ("the
// requested project was not found" — measured), while Vertex keeps
// working because it names the project in the URL. Pinning goes
// through the auth library's env override — option.WithQuotaProject
// conflicts with the storage client's own transport options in this
// version (measured) — and an operator-set value is respected.
func New(ctx context.Context, project, bucket string) (*Uploader, error) {
	// The override is scoped to client construction and restored: it
	// used to stay in the process env for the rest of the session, so
	// every later shell_exec child (gcloud/gsutil run by the model)
	// silently inherited a different quota project (review round 2).
	// The auth library captures the quota project into the credentials
	// at NewClient; token refreshes reuse them (verified by a live
	// upload after the restore).
	if os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT") == "" {
		_ = os.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", project)
		defer func() { _ = os.Unsetenv("GOOGLE_CLOUD_QUOTA_PROJECT") }()
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("gcs client: %w", err)
	}
	return &Uploader{bucket: bucket, store: &gcsStore{bucket: client.Bucket(bucket)}}, nil
}

// newWithStore is the test constructor.
func newWithStore(bucket string, store objectStore) *Uploader {
	return &Uploader{bucket: bucket, store: store}
}

// Upload streams the file into the bucket (content-addressed; an
// existing object is not re-uploaded) and returns its gs:// URI.
//
// The store is permanent (nothing here deletes), so a wrong object
// under a content hash would be served forever — two review-round-2
// defenses guard the naming invariant:
//   - ONE file descriptor: hash pass, seek, upload pass. The original
//     re-opened by path, so a rename-replace between passes uploaded
//     different bytes under the old hash.
//   - verifyReader re-hashes DURING the upload pass and fails the
//     stream on mismatch (a recorder still appending to the file), so
//     the store never commits bytes that don't match their name.

// UploadFile uploads an already-open file — the descriptor the caller
// verified, so nothing is re-resolved by name (review after v0.68.2,
// R05). ext names the object's extension.
func (u *Uploader) UploadFile(ctx context.Context, f *os.File, ext, contentType string) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	object := objectPrefix + sum + strings.ToLower(ext)
	uri := "gs://" + u.bucket + "/" + object

	exists, err := u.store.Exists(ctx, object)
	if err != nil {
		return "", fmt.Errorf("bucket %s: %w", u.bucket, err)
	}
	if exists {
		return uri, nil // same content, same object: re-attachment is free
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	vr := &verifyReader{r: f, h: sha256.New(), want: sum}
	if err := u.store.Write(ctx, object, contentType, vr); err != nil {
		return "", fmt.Errorf("upload to %s: %w", uri, err)
	}
	return uri, nil
}

// verifyReader re-hashes the upload stream and refuses to reach a
// clean EOF if the content no longer matches the name it is being
// stored under. The error propagates out of io.Copy inside the store's
// Write, which aborts the upload before it can commit.
type verifyReader struct {
	r    io.Reader
	h    hash.Hash
	want string
}

func (v *verifyReader) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	if err == io.EOF {
		if hex.EncodeToString(v.h.Sum(nil)) != v.want {
			return n, fmt.Errorf("file changed while uploading — content no longer matches %s", v.want)
		}
	}
	return n, err
}

// gcsStore is the real GCS-backed objectStore.
type gcsStore struct {
	bucket *storage.BucketHandle
}

func (s *gcsStore) Exists(ctx context.Context, object string) (bool, error) {
	_, err := s.bucket.Object(object).Attrs(ctx)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, storage.ErrObjectNotExist) {
		return false, nil
	}
	return false, err
}

func (s *gcsStore) Write(ctx context.Context, object, contentType string, r io.Reader) error {
	// The writer rides its own cancelable context: storage.Writer
	// FINALIZES buffered data on Close, so the old error path
	// (copy fails → Close) committed a partial object under the
	// full-content hash — permanent, because nothing here deletes
	// (review round 2). Cancelling the context is the library's
	// documented abort.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := s.bucket.Object(object).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := io.Copy(w, r); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	return w.Close()
}

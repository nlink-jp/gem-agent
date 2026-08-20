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
	"io"
	"os"
	"path/filepath"
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
	if os.Getenv("GOOGLE_CLOUD_QUOTA_PROJECT") == "" {
		os.Setenv("GOOGLE_CLOUD_QUOTA_PROJECT", project)
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
func (u *Uploader) Upload(ctx context.Context, path, contentType string) (string, error) {
	sum, err := hashFile(path)
	if err != nil {
		return "", err
	}
	object := objectPrefix + sum + strings.ToLower(filepath.Ext(path))
	uri := "gs://" + u.bucket + "/" + object

	exists, err := u.store.Exists(ctx, object)
	if err != nil {
		return "", fmt.Errorf("bucket %s: %w", u.bucket, err)
	}
	if exists {
		return uri, nil // same content, same object: re-attachment is free
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := u.store.Write(ctx, object, contentType, f); err != nil {
		return "", fmt.Errorf("upload to %s: %w", uri, err)
	}
	return uri, nil
}

// hashFile streams the file through sha256 — media files are large by
// nature, and reading them whole into memory to name an object would
// be the exact cost this package exists to avoid.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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
	w := s.bucket.Object(object).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := io.Copy(w, r); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

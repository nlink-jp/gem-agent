package tools

import (
	"context"
	"sync"
)

// Saved is one file the runtime wrote on a tool's behalf — an MCP
// result block in the session work directory (ADR-0105 §1). It travels
// beside the result text, never inside it: a server can print a line
// shaped like the runtime's note, and nothing the server returns can
// add an entry here. The executor copies these into the transcript's
// tool-result entry, and that record is what evidence is checked
// against.
type Saved struct {
	// Path is the saved file. Empty when the block could not be saved.
	Path string
	// Bytes is the block's size, saved or not.
	Bytes int64
	// SHA256 is the hex digest of the saved bytes.
	SHA256 string
	// Err says why the block was not saved; empty when it was.
	Err string
}

// SaveRecorder collects what one tool call saved. The executor makes one
// per call and hands it down through the context, so the tool needs no
// second return value and a tool that saves nothing is untouched.
type SaveRecorder struct {
	mu    sync.Mutex
	files []Saved
}

// Files returns what was recorded, in order.
func (r *SaveRecorder) Files() []Saved {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Saved(nil), r.files...)
}

type saveRecorderKey struct{}

// WithSaveRecorder returns a context whose tool run records into rec.
func WithSaveRecorder(ctx context.Context, rec *SaveRecorder) context.Context {
	return context.WithValue(ctx, saveRecorderKey{}, rec)
}

// RecordSaved adds files to the call's recorder. Without one it does
// nothing: tests and callers that keep no transcript lose no behaviour.
func RecordSaved(ctx context.Context, files ...Saved) {
	rec, _ := ctx.Value(saveRecorderKey{}).(*SaveRecorder)
	if rec == nil || len(files) == 0 {
		return
	}
	rec.mu.Lock()
	rec.files = append(rec.files, files...)
	rec.mu.Unlock()
}

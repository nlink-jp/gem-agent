package tools

import (
	"context"
	"errors"
	"sync"
)

// ErrCredentialRead is what a covered read returns when the kernel
// refused the open because the path is credential material (ADR-0086
// §2). It is the operator's question, not a failure: the agent turns it
// into the operator-only prompt and, on approval, re-issues the read in
// process — the operator lane's authority applied to a file tool.
var ErrCredentialRead = errors.New("credential material: only the operator may read this file")

// FileChildFunc runs one file-tool call in a child process under the
// file-read profile (ADR-0086 §1) and returns what the tool printed.
// It returns ErrCredentialRead when the kernel refused the open.
type FileChildFunc func(ctx context.Context, tool string, args map[string]any) (string, error)

// childTools are the tools whose reads the kernel adjudicates: every
// built-in that puts one named file's content, bytes or metadata in
// front of the model, plus the search walk, which is one call over many
// opens. The enumeration tools are absent on purpose — they read no
// content, and the kernel lists names rather than hiding them, so
// ADR-0086 §3 stops judging names at all.
var childTools = map[string]bool{
	"read_file": true, "view_image": true, "read_document": true,
	"file_info": true, "search_files": true,
}

// ChildTool reports whether name's reads run in the sandboxed child.
func ChildTool(name string) bool { return childTools[name] }

// fileChild holds the injected runner. Nil in the child process itself
// and in tests, where the tools run in process exactly as before.
type fileChild struct {
	mu sync.RWMutex
	fn FileChildFunc
}

// SetFileChild installs the sandboxed runner for the covered reads.
// Mirrors SetLaneExec: the runtime measures what it can enforce and
// injects it, and a registry without it reads in process.
func (r *Registry) SetFileChild(fn FileChildFunc) {
	r.child.mu.Lock()
	r.child.fn = fn
	r.child.mu.Unlock()
}

func (r *Registry) fileChildFn() FileChildFunc {
	if r.parent != nil {
		return r.parent.fileChildFn()
	}
	r.child.mu.RLock()
	defer r.child.mu.RUnlock()
	return r.child.fn
}

// KernelReads reports whether the covered reads are adjudicated by the
// kernel. False means the runtime could not verify the cage and the
// Go matcher is the boundary again (ADR-0086 §5).
func (r *Registry) KernelReads() bool { return r.fileChildFn() != nil }

// viaChild runs name in the sandboxed child when one is installed.
// handled is false when the read must happen in process: no child
// (tests, the child itself), or the operator has approved this very
// call and the cage would refuse what they just allowed.
func (r *Registry) viaChild(ctx context.Context, name string, args map[string]any) (out string, err error, handled bool) {
	if directRead(ctx) {
		return "", nil, false
	}
	fn := r.fileChildFn()
	if fn == nil {
		return "", nil, false
	}
	out, err = fn(ctx, name, args)
	return out, err, true
}

// directKey marks a context whose reads bypass the cage: the operator
// answered the credential prompt, and the operator lane is the lane
// that may read credentials.
type directKey struct{}

// WithDirectRead returns a context whose covered reads run in process.
// The agent sets it only after the operator approved that exact call.
func WithDirectRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, directKey{}, true)
}

func directRead(ctx context.Context) bool {
	v, _ := ctx.Value(directKey{}).(bool)
	return v
}

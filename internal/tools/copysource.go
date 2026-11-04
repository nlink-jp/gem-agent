package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/sandbox"
)

// write_file's `from` (ADR-0105 §2) copies an existing file byte for
// byte. The source is read here, raw, whole and bounded — never through
// read_file, whose window stops at 200 KiB and appends notes of its
// own, and never through the child's ordinary pipe, which caps at 1 MiB
// and marks a cut. A copy that is not every byte is not a copy.

// CopySourceOp is the file-read child's operation for a copy source. It
// is not a tool: nothing declares it to the model, and the child runs
// it by name beside the covered reads.
const CopySourceOp = "copy_source"

// CopySourceCap is the largest source a copy reads: the shell spool's
// cap (ADR-0096 §3). Above it the copy is refused, never truncated.
const CopySourceCap = 32 << 20

// errCopyCredential is what a copy source that is credential material
// becomes. It is deliberately not ErrCredentialRead: that one is the
// covered reads' question to the operator, and a copy is never asked —
// an approval that looks like a read must not turn into a credential
// written under an ordinary project name (ADR-0105 §2).
var errCopyCredential = errors.New("the copy source is credential material; write_file does not copy it")

// readCopySource reads p for a copy: through the sandboxed child when
// one is installed, so the kernel adjudicates the open (ADR-0086), and
// in process otherwise.
func (r *Registry) readCopySource(ctx context.Context, p string) ([]byte, error) {
	if out, err, ok := r.viaChild(ctx, CopySourceOp, map[string]any{"path": p}); ok {
		if errors.Is(err, ErrCredentialRead) {
			return nil, errCopyCredential
		}
		if err != nil {
			return nil, err
		}
		return []byte(out), nil
	}
	return r.ReadCopySource(p)
}

// ReadCopySource reads p raw, in this process — the child's side of
// CopySourceOp, and the read itself when there is no child. It admits a
// regular file inside the roots, at most CopySourceCap bytes. A
// credential path is refused by the matcher here too: without the
// child, this is the check (ADR-0086 §5).
func (r *Registry) ReadCopySource(p string) ([]byte, error) {
	abs, err := r.resolvePath(p)
	if err != nil {
		return nil, err
	}
	if sandbox.CredentialPath(abs) {
		return nil, errCopyCredential
	}
	if real, err := resolveExisting(abs); err == nil && sandbox.CredentialPath(real) {
		return nil, errCopyCredential
	}
	f, err := r.openRead(abs)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file; write_file copies one file", p)
	}
	if st.Size() > CopySourceCap {
		return nil, fmt.Errorf("%s is %d bytes, above the %d-byte copy limit; nothing was written", p, st.Size(), CopySourceCap)
	}
	data, more, err := bounded.ReadAll(f, CopySourceCap)
	if err != nil {
		return nil, err
	}
	if more {
		return nil, fmt.Errorf("%s grew past the %d-byte copy limit while it was read; nothing was written", p, CopySourceCap)
	}
	return data, nil
}

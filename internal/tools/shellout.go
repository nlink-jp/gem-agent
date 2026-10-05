package tools

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nlink-jp/gem-agent/internal/bounded"
)

// shellSpoolCap bounds how much of one command's output is saved to the
// work directory (ADR-0096 §3). Past it the stream is counted, not
// written, and the note says how much was saved of how much printed.
const shellSpoolCap = 32 << 20

// shellOutput is shell_exec's output (ADR-0096 §3): a bounded.HeadTail
// on the pipe and the note that says what it kept. gem-agent writes the
// spool, not the command — this writer sits on the pipe, so the
// command's lane is unchanged.
type shellOutput struct {
	w *bounded.HeadTail
	// noSave says why there is no spool when there is none to open.
	noSave string
}

func newShellOutput(limit int, spoolCap int64, open func() (io.WriteCloser, string, error), noSave string) *shellOutput {
	return &shellOutput{w: bounded.NewHeadTail(limit, spoolCap, open), noSave: noSave}
}

func (o *shellOutput) Write(p []byte) (int, error) { return o.w.Write(p) }
func (o *shellOutput) Close()                      { o.w.Close() }

// CommandText is what the command printed and the model is shown,
// without the runtime's note: the tail when the output was cut.
func (o *shellOutput) CommandText() string {
	v := o.w.View()
	if v.Over {
		return string(v.Tail)
	}
	return string(v.Head)
}

// String renders what the model gets: the whole output when it fit,
// else head, an elision line and tail, and a note naming the byte spans
// shown and where the whole output is — the spans are the unit
// read_file's offset takes.
func (o *shellOutput) String() string {
	v := o.w.View()
	if !v.Over {
		return string(v.Head)
	}
	h, t := int64(len(v.Head)), v.TailStart
	shown := fmt.Sprintf("output: %d bytes; shown: bytes 0–%d and %d–%d", v.Total, h, t, v.Total)
	var where string
	switch {
	case v.Path != "" && v.SaveErr != nil:
		where = fmt.Sprintf("the first %d bytes are saved (saving stopped: %v): %s", v.Saved, v.SaveErr, v.Path)
	case v.Path != "" && v.Saved < v.Total:
		where = fmt.Sprintf("the first %d bytes are saved (the save cap): %s", v.Saved, v.Path)
	case v.Path != "":
		where = "the whole output is saved: " + v.Path
	case v.SaveErr != nil:
		where = fmt.Sprintf("the middle could not be saved (%v), so it is lost", v.SaveErr)
	default:
		where = o.noSave
	}
	return fmt.Sprintf("%s\n[… bytes %d–%d not shown …]\n%s\n[%s; %s]", v.Head, h, t, v.Tail, shown, where)
}

// shellSpoolOpener returns the spool opener for one shell call, or nil
// with the reason there is no spool. The work root is acquired for the
// whole call, so a /clear that rotates it mid-command (ADR-0071 §2)
// leaves the file in the directory the note names. The operator lane is
// never spooled: it may read credentials (ADR-0085), and a copy in the
// work directory would be readable by read_file without approval — the
// kernel cage judges paths, not contents (ADR-0086). An unconfined shell
// (no sandbox) is not spooled for the same reason: any lane can read
// credentials there.
func (r *Registry) shellSpoolOpener(operatorLane, confined bool) (open func() (io.WriteCloser, string, error), noSave string, release func()) {
	release = func() {}
	if operatorLane {
		return nil, "the middle was not saved (operator-lane output is not written to disk)", release
	}
	if !confined {
		return nil, "the middle was not saved (unsandboxed output is not written to disk)", release
	}
	dir, h := r.acquireWorkRoot()
	if h == nil {
		return nil, "the middle is lost (no session work directory)", release
	}
	release = h.release
	open = func() (io.WriteCloser, string, error) {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, "", err
		}
		name := fmt.Sprintf("shell_exec-%s-%s.txt", time.Now().Format("20060102-150405"), hex.EncodeToString(b[:]))
		// Private, like the MCP spill (CreateTemp): command output can
		// carry anything the command could read.
		f, err := h.root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, "", err
		}
		return f, dir + string(os.PathSeparator) + name, nil
	}
	return open, "", release
}

// acquireWorkRoot returns the work directory and its handle, acquired
// under the same lock that rotates it — rootFor's rule — or a nil
// handle when there is none. The caller owes h.release.
func (r *Registry) acquireWorkRoot() (string, *rootHandle) {
	if r.parent != nil {
		return r.parent.acquireWorkRoot()
	}
	r.rootsMu.RLock()
	defer r.rootsMu.RUnlock()
	if r.workDir == "" || r.workRoot == nil {
		return "", nil
	}
	r.workRoot.acquire()
	return r.workDir, r.workRoot
}

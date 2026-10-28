package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/trustpin"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// addedWatch is ADR-0099 §10: the project's change detection (ADR-0074
// §3) applied to every write root. A write-lane command can move a
// directory holding an AGENTS.md or a .git/hooks script into a write
// root — the name denials cannot see a rename of its parent — and a
// project later opened under it loads that file as an ancestor
// instruction without asking. Nothing here blocks; the walk makes the
// change visible when it happens: at startup against what the previous
// session of this project recorded, at /clear and at exit against this
// session's own start. A read root is written by no lane and is not
// walked.
type addedWatch struct {
	roots []string
	base  map[string]map[string]string
	// path is the state file (added-persistent.json beside the project's
	// persistent.json); "" when the state directory is unavailable, and
	// then only this session's own changes are reported. It holds the
	// roots this session watches and no others, so it does not grow
	// with roots the operator stopped adding.
	path string
	out  io.Writer
	msgs *uitext.Messages
	// snapshot is trustpin.Snapshot; a test injects a cut walk.
	snapshot func(dir string) (map[string]string, bool)
}

// newAddedWatch snapshots each write root, says where a walk was cut,
// and reports what changed since the previous session recorded it.
// That startup report writes no transcript record, as the project's own
// does not: the record is of what a session did.
func newAddedWatch(added []addedDir, projectDir string, out io.Writer, msgs *uitext.Messages) *addedWatch {
	return newAddedWatchWith(added, projectDir, out, msgs, trustpin.Snapshot)
}

func newAddedWatchWith(added []addedDir, projectDir string, out io.Writer, msgs *uitext.Messages, snapshot func(string) (map[string]string, bool)) *addedWatch {
	w := &addedWatch{base: map[string]map[string]string{}, out: out, msgs: msgs, snapshot: snapshot}
	for _, a := range added {
		if a.Write {
			w.roots = append(w.roots, a.Dir)
		}
	}
	if len(w.roots) == 0 {
		return w
	}
	var saved map[string]map[string]string
	if p, err := persistentStateFile(projectDir); err == nil {
		w.path = filepath.Join(filepath.Dir(p), "added-persistent.json")
		saved = loadAddedSnapshots(w.path)
	}
	for _, root := range w.roots {
		snap, cut := w.snapshot(root)
		if cut {
			_, _ = fmt.Fprintf(out, msgs.AddedPersistentCutFmt+"\n", trustpin.WalkEntries, root)
		}
		if prev, ok := saved[root]; ok {
			a, c, r := trustpin.SnapshotDiff(prev, snap)
			if cut {
				r = nil // past the cut is unknown, not removed
			}
			if len(a)+len(c)+len(r) > 0 {
				_, _ = fmt.Fprintf(out, msgs.AddedPersistentSinceLastFmt+"\n", root, describeSnapshotDiff(a, c, r))
			}
		}
		w.base[root] = snap
	}
	w.save()
	return w
}

// report walks each write root again and says what this session added,
// changed or removed since the last report, writing a
// persistent_changes record with the root beside the project's own.
func (w *addedWatch) report(reason string, log func(kind string, data any) error) {
	if w == nil || len(w.roots) == 0 {
		return
	}
	for _, root := range w.roots {
		now, cut := w.snapshot(root)
		a, c, r := trustpin.SnapshotDiff(w.base[root], now)
		if cut {
			// A walk the session cut short — a command can fill the root
			// past the cap — says so, and names nothing past the cut as
			// removed: it is unknown, not gone (review of §10, #2).
			_, _ = fmt.Fprintf(w.out, w.msgs.AddedPersistentCutFmt+"\n", trustpin.WalkEntries, root)
			r = nil
		}
		if len(a)+len(c)+len(r) > 0 || cut {
			if len(a)+len(c)+len(r) > 0 {
				_, _ = fmt.Fprintf(w.out, w.msgs.AddedPersistentSessionFmt+"\n", root, describeSnapshotDiff(a, c, r))
			}
			if log != nil {
				_ = log("persistent_changes", map[string]any{"reason": reason, "root": root, "added": a, "changed": c, "removed": r, "cut": cut})
			}
		}
		w.base[root] = now
	}
	w.save()
}

func (w *addedWatch) save() {
	if w.path == "" {
		return
	}
	_ = saveAddedSnapshots(w.path, w.base)
}

// loadAddedSnapshots reads the per-root snapshots; a missing, oversized
// or unreadable file is no previous state, as for the project's own.
func loadAddedSnapshots(path string) map[string]map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	data, more, err := bounded.ReadAll(f, 8<<20)
	if err != nil || more {
		return nil
	}
	var snaps map[string]map[string]string
	if json.Unmarshal(data, &snaps) != nil {
		return nil
	}
	return snaps
}

func saveAddedSnapshots(path string, snaps map[string]map[string]string) error {
	data, err := json.Marshal(snaps)
	if err != nil {
		return err
	}
	// A temporary name of its own: two sessions of one project may save
	// at once, and a shared name could rename a torn file into place
	// (review of §10, #3).
	f, err := os.CreateTemp(filepath.Dir(path), ".added-persistent-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

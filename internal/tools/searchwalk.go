package tools

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/nlink-jp/gem-agent/internal/ignore"
)

// search_files reads and matches files side by side and uses them in
// walk order (ADR-0125 §2). One goroutine walks, as the sequential
// walk did; a few workers read and match the files it finds; the
// calling goroutine takes every step in walk order and does what the
// loop did — counts, prints, stops at the cap. Everything that reaches
// the output is applied there, so a capped or cancelled result counts
// exactly what the sequential walk counted.

// searchWorkerLimit bounds the workers. Tests set 1 to run the search
// with one reader.
var searchWorkerLimit = 8

// searchTestHook, when a test sets it, is called by the walk before it
// lists each directory ("dir", its path) and by a worker before it reads
// each file ("file", its path). Nil outside tests.
var searchTestHook func(event, path string)

func searchWorkers() int {
	return max(1, min(runtime.GOMAXPROCS(0), searchWorkerLimit))
}

// searchItemKind names one step of the walk.
type searchItemKind int

const (
	itemFile        searchItemKind = iota // a file handed to the workers; res carries its result
	itemDirUnlisted                       // a directory that could not be opened or listed
	itemDirOverCap                        // a directory with more than DirEntryCap entries
	itemIgnoredDir                        // name is the ignored directory's
	itemIgnoredFile                       // a file the ignore rules skip
	itemFiltered                          // a file include= leaves out
	itemSkip                              // a file not searched: skip says why, name names it or is ""
	itemEnd                               // the walk's last item: interrupted says it stopped on a cancel
)

type searchItem struct {
	kind        searchItemKind
	name        string
	skip        searchSkip
	interrupted bool
	res         <-chan fileResult
}

// fileResult is what the sequential loop computed for one file.
type fileResult struct {
	skip  searchSkip // not searched, and why; err says whether the cage refused it
	err   error
	count int
	hits  []lineHit
	cut   bool // not read or scanned to the end (a cancel, or the call stopped): not counted
}

// dirRef is a directory handle the walk and the workers reading its
// files share; the last release closes it.
type dirRef struct {
	h    *os.Root
	refs atomic.Int32
}

func newDirRef(h *os.Root) *dirRef {
	d := &dirRef{h: h}
	d.refs.Store(1)
	return d
}

func (d *dirRef) acquire() { d.refs.Add(1) }

func (d *dirRef) release() {
	if d.refs.Add(-1) == 0 {
		_ = d.h.Close()
	}
}

type searchJob struct {
	dir        *dirRef
	name, full string
	res        chan<- fileResult
}

// searchWalker is the walk of the sequential loop, emitting what the
// loop counted instead of counting it.
type searchWalker struct {
	ctx        context.Context
	stop       <-chan struct{}
	items      chan<- searchItem
	jobs       chan<- searchJob
	abs        string // the walk's start, for include
	projectDir string // for display names
	inc        *ignore.IncludePattern
	cancelled  bool
}

// run walks from start and ends the stream with the reason it ended.
// It closes items and jobs on return, which lets the workers finish.
func (w *searchWalker) run(start *dirRef, rules *ignore.Rules) {
	defer close(w.jobs)
	defer close(w.items)
	if w.dir(start, w.abs, rules) || w.cancelled {
		w.emit(searchItem{kind: itemEnd, interrupted: w.cancelled})
	}
}

// emit sends it unless the call has stopped; false means stop walking.
func (w *searchWalker) emit(it searchItem) bool {
	select {
	case w.items <- it:
		return true
	case <-w.stop:
		return false
	}
}

// halted reports that the walk must end, consulted before every
// directory read and every entry: a cancel (ADR-0065 §1), or the call
// having stopped — at the cap, the sequential loop opened nothing
// more, and neither does the walk (verification of the fixes, ADR-0125).
func (w *searchWalker) halted() bool {
	if w.ctx.Err() != nil {
		w.cancelled = true
		return true
	}
	select {
	case <-w.stop:
		return true
	default:
		return false
	}
}

// dir walks one directory held open by d and releases d. It returns
// false when the walk must end: a cancel, or the call stopped.
func (w *searchWalker) dir(d *dirRef, dir string, rules *ignore.Rules) bool {
	defer d.release()
	if searchTestHook != nil {
		searchTestHook("dir", dir)
	}
	if w.halted() {
		return false
	}
	entries, more, err := readDirOf(d.h)
	if err != nil {
		return w.emit(searchItem{kind: itemDirUnlisted})
	}
	if more && !w.emit(searchItem{kind: itemDirOverCap}) {
		return false
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if w.halted() {
			return false
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue // never follow links out of the walk (ADR-0013 §3)
		}
		if vcsDirs[e.Name()] {
			continue // a submodule's .git is a file — skip by name either way
		}
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			if rules.Ignored(e.Name(), true) {
				if !w.emit(searchItem{kind: itemIgnoredDir, name: e.Name()}) {
					return false
				}
				continue
			}
			if w.halted() {
				return false
			}
			sub, err := openDirRoot(d.h, e.Name())
			if err != nil {
				if !w.emit(searchItem{kind: itemDirUnlisted}) { // what a failed listing counted
					return false
				}
				continue
			}
			if !w.dir(newDirRef(sub), full, rules.DescendWith(e.Name(), gitignoreReaderIn(sub))) {
				return false
			}
			continue
		}
		var it searchItem
		switch {
		case rules.Ignored(e.Name(), false):
			it = searchItem{kind: itemIgnoredFile}
		case w.inc != nil && !w.inc.MatchFile(filepath.ToSlash(relOrDot(w.abs, full))):
			it = searchItem{kind: itemFiltered}
		default:
			// Every file not searched is counted by reason
			// (ADR-0096 §4).
			info, err := e.Info()
			switch {
			case err != nil:
				it = searchItem{kind: itemSkip, skip: skipUnreadable}
			case info.Size() > searchFileCap:
				it = searchItem{kind: itemSkip, skip: skipSize, name: relOrDot(w.projectDir, full)}
			case isImageExt(e.Name()):
				it = searchItem{kind: itemSkip, skip: skipImage}
			default:
				res := make(chan fileResult, 1)
				d.acquire()
				select {
				case w.jobs <- searchJob{dir: d, name: e.Name(), full: full, res: res}:
				case <-w.stop:
					d.release()
					return false
				}
				it = searchItem{kind: itemFile, name: relOrDot(w.projectDir, full), res: res}
			}
		}
		if !w.emit(it) {
			return false
		}
	}
	return true
}

// searchWorker reads and matches the files it is handed until the walk
// closes jobs. After a cancel or a stop it reads nothing more, but
// still releases each job's directory, so every handle is closed.
func searchWorker(ctx context.Context, stop <-chan struct{}, jobs <-chan searchJob, re *regexp.Regexp, blocks bool) {
	stopped := func() bool {
		select {
		case <-stop:
			return true
		default:
			return ctx.Err() != nil
		}
	}
	for j := range jobs {
		if searchTestHook != nil {
			searchTestHook("file", j.full)
		}
		if stopped() {
			j.dir.release()
			j.res <- fileResult{cut: true}
			continue
		}
		data, skip, err := readForSearch(j.dir.h, j.name, j.full)
		j.dir.release()
		if skip != skipNone {
			j.res <- fileResult{skip: skip, err: err}
			continue
		}
		count, hits, cut := scanLines(data, re, blocks, searchPerFileCap, stopped)
		// A result may wait for the calling goroutine; it keeps the
		// clipped lines it will print, copied, not the file's text the
		// hits point into (pre-release review of ADR-0125).
		for i := range hits {
			hits[i].text = strings.Clone(clipRunes(strings.TrimSpace(hits[i].text), searchLineClip))
		}
		j.res <- fileResult{count: count, hits: hits, cut: cut}
	}
}

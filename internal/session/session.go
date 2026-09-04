// Package session appends session records to a JSONL log file and reads
// them back to resume a conversation (ADR-0005).
//
// The file is both the diagnostic log and the resume source of truth:
// records that carry conversation state ("message", "compaction") are
// written in full fidelity — including Gemini thought signatures, which
// the API requires on replay — while diagnostic records stay summarised
// and are ignored on load.
package session

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/statedir"
)

// SchemaVersion is the transcript format version. It is written into
// every session header so a file this build cannot replay is reported as
// such rather than half-loaded. Version 2 added the "clear" record
// (ADR-0021): an older build reading a cleared session would silently
// resurrect the discarded conversation, so it must refuse instead.
const SchemaVersion = 2

// Record kinds that carry conversation state. Everything else in the
// file is diagnostic and skipped by Load.
const (
	KindHeader     = "session"
	KindMessage    = "message"
	KindCompaction = "compaction"
	KindClear      = "clear"
	KindResumed    = "resumed"
)

// KindUsage is the accounting record: one per model call, whatever
// made it (ADR-0057). Diagnostic — Load skips it like the rest.
const KindUsage = "usage"

// Usage sources (ADR-0057 §1). Every model call in the process names
// itself with one of these, so a transcript can be summed by category
// and priced per model without knowing which code path spent it.
const (
	UsageMain          = "main"
	UsageRisk          = "risk"
	UsageProgress      = "progress_review"
	UsageCompact       = "compact"
	UsageSummarizeFile = "summarize_file"
	UsageWebSearch     = "web_search"
	UsageWebFetch      = "web_fetch"
	UsageFileSearch    = "agentic_file_search"
	UsageRiskbookLearn = "riskbook_learn"
)

// UsageRecord is one model call's spend. The buckets are the ones
// billing uses: Thoughts is separate from Output (and billed as
// output), Cached is a discounted share of Prompt, ToolPrompt is the
// built-in tool results fed back as input (billed as input, ADR-0066),
// and Total is the API's own count — the checksum
// prompt + output + thoughts + tool_prompt (ADR-0057 §2 as amended).
// tool_prompt is written always, zero included: a missing key marks a
// pre-0066 record, which an aggregator must derive rather than trust.
type UsageRecord struct {
	Source     string `json:"source"`
	Model      string `json:"model"`
	Prompt     int    `json:"prompt"`
	Output     int    `json:"output"`
	Thoughts   int    `json:"thoughts"`
	Cached     int    `json:"cached"`
	ToolPrompt int    `json:"tool_prompt"`
	Total      int    `json:"total"`
}

// ShellContextPrefix opens the user-role message the agent injects when
// the operator runs a `!` command. It lives here, rather than being
// sniffed for, because the session listing has to tell an injected
// message from something the operator actually typed: showing the
// wrapper text as a session's preview reads like a bug.
const ShellContextPrefix = "I ran this shell command myself:"

// Record is one JSONL line.
type Record struct {
	Time time.Time `json:"ts"`
	Kind string    `json:"kind"`
	Data any       `json:"data"`
}

// Header is the first record of a session file: what produced it, and
// what it may be replayed into.
type Header struct {
	Schema  int    `json:"schema"`
	Version string `json:"version"`
	Model   string `json:"model"`
	Project string `json:"project"`
	// Location is the Vertex region the session billed against —
	// prices are resolved per SKU per region (ADR-0057 §4).
	Location string `json:"location,omitempty"`
}

// Compaction is the record written when history is compacted (ADR-0006).
// Replaced counts the leading messages the summary stands in for, so a
// loader can reproduce the compaction instead of re-inflating history.
type Compaction struct {
	Replaced int         `json:"replaced"`
	Message  llm.Message `json:"message"`
}

// Meta describes one session file without loading its conversation.
type Meta struct {
	ID         string
	Path       string
	Header     Header
	Started    time.Time
	LastActive time.Time
	Size       int64
	// Preview is the first user message, clipped — the only reliable way
	// to recognise a session in a list of timestamps.
	Preview string
	// HasConversation is false for a transcript that only ever recorded a
	// header: a run that exited at the prompt, or one that used nothing
	// but slash commands. There is nothing in it to resume.
	HasConversation bool
}

// EnvVar is the environment variable naming the running session's id.
// Like the work directory (ADR-0058), it is exported at startup so that
// everything the session spawns — MCP servers above all — can be told
// which session it serves without gem-agent knowing anything about the
// server: `${GEMAGENT_SESSION_ID}` in an mcp.json args entry expands to
// it (ADR-0069 addendum 2, for agent-board's MCP face).
const EnvVar = "GEMAGENT_SESSION_ID"

// Export publishes id under EnvVar for child processes.
func Export(id string) error {
	if id == "" {
		return nil
	}
	return os.Setenv(EnvVar, id)
}

// Logger appends records to one session file.
type Logger struct {
	mu sync.Mutex
	f  *os.File
	// w is what Log writes to — f, except in tests that inject a
	// failing writer.
	w  io.Writer
	id string
	// torn is set when a write failed part-way (ENOSPC, EIO): the file
	// ends in a fragment without its newline, and the next record would
	// glue onto it — one invalid line that swallows a whole message on
	// resume (review round 4; Reopen makes the same repair across
	// processes). The next write starts with the newline.
	torn bool
}

// DefaultDir returns the state location for session logs — under the
// shared state root, so GEMAGENT_STATE_DIR isolates it (ADR-0022).
func DefaultDir() (string, error) {
	root, err := statedir.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "sessions"), nil
}

// projectSubdir is where projectDir's sessions live (ADR-0022): the
// same escaped-path + .project-marker convention as memory. Legacy
// flat files directly under dir stay readable in place.
func projectSubdir(dir, projectDir string) string {
	return filepath.Join(dir, "projects", statedir.EscapeProject(projectDir))
}

// idPattern matches the ids Open generates — a UUID v4 since ADR-0071,
// the timestamp form before it — and nothing else. Resume accepts an
// id, never a path: an id that cannot contain a separator cannot
// escape the sessions directory, and cannot name a transcript somebody
// else placed (ADR-0005).
var idPattern = regexp.MustCompile(`^(\d{8}-\d{6}(-\d+)?|[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12})$`)

// ValidID reports whether s is a well-formed session id.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// prefixPattern is what an operator may type to name a session by its
// leading characters: hex and hyphens, four characters or more.
var prefixPattern = regexp.MustCompile(`^[0-9a-f-]{4,}$`)

// NewID returns a random UUID v4 (ADR-0071 §1): unique on the machine
// and, for practical purposes, everywhere — the timestamp ids it
// replaces were unique only within one project directory.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("session: crypto/rand unavailable: " + err.Error())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Short returns the first eight characters of a UUID id — what the
// listing shows and what an operator types — or a legacy id unchanged.
func Short(id string) string {
	if len(id) == 36 && strings.Count(id, "-") == 4 {
		return id[:8]
	}
	return id
}

// Open starts a new session file named by timestamp, inside the
// project's own subdirectory (ADR-0022). A .project marker collision —
// two projects escaping to the same name — refuses loudly rather than
// mixing their transcripts.
func Open(dir, projectDir string) (*Logger, error) {
	sub := projectSubdir(dir, projectDir)
	if err := statedir.EnsureProjectDir(sub, projectDir); err != nil {
		return nil, err
	}
	dir = sub
	// O_EXCL prevents two sessions from silently sharing one file; a
	// UUID collision is not expected, but the loop stays honest.
	for i := 0; i < 3; i++ {
		id := NewID()
		path := filepath.Join(dir, id+".jsonl")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
		if err == nil {
			if err := lockSession(f, id); err != nil {
				// We JUST created this exact file (O_EXCL) and wrote
				// nothing; removing it by its full literal path is the
				// error-path cleanup, not data deletion — leaving it
				// littered zero-byte orphans (review round 2).
				_ = os.Remove(path)
				return nil, err
			}
			return &Logger{f: f, id: id}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not allocate a session file in %s", dir)
}

// Reopen appends to an existing session (resume). One file is one
// conversation however many processes it took, so a resumed session
// continues its own transcript rather than starting a second one — in
// whichever location it lives: the project subdirectory, or the legacy
// flat layout (ADR-0022 §3: legacy files are read in place, never
// moved).
func Reopen(dir, projectDir, id string) (*Logger, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	path, err := findSessionFile(dir, projectDir, id)
	if err != nil {
		return nil, err
	}
	// O_RDWR, not O_WRONLY: the tail repair below needs to read the last
	// byte. Appends still go to the end (O_APPEND).
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockSession(f, id); err != nil {
		return nil, err
	}
	// Tail repair (ADR-0021): a crash's torn last line must cost one
	// line, never the records appended after it. Without the newline,
	// the first append glues onto the tear and the merged line — torn
	// prefix plus valid record — is one invalid line, and everything
	// after it was silently dropped on the next load (measured: 1 of 6
	// turns survived).
	if st, err := f.Stat(); err == nil && st.Size() > 0 {
		buf := make([]byte, 1)
		if _, err := f.ReadAt(buf, st.Size()-1); err == nil && buf[0] != '\n' {
			if _, err := f.Write([]byte("\n")); err != nil {
				_ = f.Close()
				return nil, fmt.Errorf("repairing session tail: %w", err)
			}
		}
	}
	return &Logger{f: f, id: id}, nil
}

// lockSession takes a non-blocking exclusive advisory lock for the
// logger's lifetime (released when the file closes). Two processes
// appending to one transcript interleave into a conversation neither of
// them had — refusing the second is the only honest answer (ADR-0021).
func lockSession(f *os.File, id string) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		// Only EWOULDBLOCK means another process holds the lock; a
		// filesystem without flock (a network-mounted state root)
		// failed with the same false "already in use" diagnosis
		// (review round 2).
		if err == syscall.EWOULDBLOCK {
			return fmt.Errorf("session %s is already in use by another gem-agent process", id)
		}
		return fmt.Errorf("cannot lock session %s: %v (a state dir on a filesystem without flock support cannot hold transcripts safely)", id, err)
	}
	return nil
}

// Path returns the session file path.
func (l *Logger) Path() string { return l.f.Name() }

// ID returns the session id (the file's base name).
func (l *Logger) ID() string { return l.id }

// Log appends one record. Logging failures are returned but callers may
// treat them as non-fatal — a broken log must not kill a working session.
func (l *Logger) Log(kind string, data any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	line, err := json.Marshal(Record{Time: time.Now(), Kind: kind, Data: data})
	if err != nil {
		return fmt.Errorf("marshal session record: %w", err)
	}
	w := l.w
	if w == nil {
		w = l.f
	}
	if l.torn {
		if _, err := w.Write([]byte("\n")); err != nil {
			return fmt.Errorf("repairing session tail: %w", err)
		}
		l.torn = false
	}
	n, err := w.Write(append(line, '\n'))
	if err != nil {
		// A short write left a fragment; a write that landed nothing
		// left the file as it was. Only the fragment needs the repair.
		l.torn = n > 0
		return fmt.Errorf("append session record: %w", err)
	}
	return nil
}

// Close closes the underlying file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// rawRecord is one parsed JSONL line.
type rawRecord struct {
	Time time.Time       `json:"ts"`
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

// forEachLine reads JSONL records line by line — bufio.Reader, not
// bufio.Scanner (a single read_file result can exceed Scanner's 64KB
// token limit) and not json.Decoder (which cannot resynchronise after a
// corrupt line). fn receives nil for a line that does not parse — a
// torn write must cost at most itself, never the records after it
// (ADR-0021; measured: the old decoder dropped everything after a glued
// tear). fn returning false stops the scan early.
func forEachLine(f io.Reader, fn func(rec *rawRecord) (bool, error)) error {
	r := bufio.NewReader(f)
	for {
		line, readErr := r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			var rec rawRecord
			p := &rec
			if json.Unmarshal(trimmed, &rec) != nil || rec.Kind == "" {
				p = nil
			}
			cont, err := fn(p)
			if err != nil {
				return err
			}
			if !cont {
				return nil
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// Load reads a session file back into a conversation history, applying
// recorded compactions and clears on the way (ADR-0006/0021): a session
// that was compacted resumes compacted, and one that was cleared
// resumes cleared.
//
// A record this build does not understand is skipped, not fatal — a
// diagnostic line must never make a conversation unresumable — but a
// newer schema version is refused outright, because then the message
// records themselves may not mean what this build thinks they do.
// skipped counts unreadable lines so the caller can say "N lines lost"
// instead of presenting a shorter conversation as complete.
func Load(path string) ([]llm.Message, Header, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Header{}, 0, err
	}
	defer func() { _ = f.Close() }()

	var (
		header  Header
		history []llm.Message
		skipped int
		// skippedSinceAnchor guards compaction indices: an unreadable
		// message line shifts every later index, so a compaction record
		// after one cannot be trusted. A clear record resets the frame
		// (indices after it are relative to the fresh history).
		skippedSinceAnchor int
	)
	err = forEachLine(f, func(rec *rawRecord) (bool, error) {
		if rec == nil {
			skipped++
			skippedSinceAnchor++
			return true, nil
		}
		switch rec.Kind {
		case KindHeader:
			if err := json.Unmarshal(rec.Data, &header); err != nil {
				return false, fmt.Errorf("%s: unreadable session header: %w", path, err)
			}
			if header.Schema > SchemaVersion {
				return false, fmt.Errorf("%s was written by a newer gem-agent (transcript schema %d, this build reads %d)",
					filepath.Base(path), header.Schema, SchemaVersion)
			}
		case KindMessage:
			var m llm.Message
			if err := json.Unmarshal(rec.Data, &m); err != nil {
				skipped++
				skippedSinceAnchor++
				return true, nil
			}
			history = append(history, m)
		case KindClear:
			history = nil
			skippedSinceAnchor = 0
		case KindCompaction:
			if skippedSinceAnchor > 0 {
				return false, fmt.Errorf("%s: %d unreadable lines precede a compaction record, so its index cannot be trusted — resume refused rather than replaying the wrong messages",
					filepath.Base(path), skippedSinceAnchor)
			}
			var c Compaction
			if err := json.Unmarshal(rec.Data, &c); err != nil {
				return false, fmt.Errorf("%s: unreadable compaction record: %w", path, err)
			}
			if c.Replaced < 0 || c.Replaced > len(history) {
				return false, fmt.Errorf("%s: compaction record replaces %d of %d messages", path, c.Replaced, len(history))
			}
			history = append([]llm.Message{c.Message}, history[c.Replaced:]...)
		}
		return true, nil
	})
	if err != nil {
		return nil, header, skipped, err
	}
	return history, header, skipped, nil
}

// List returns the resumable sessions in dir, newest first. When
// projectDir is non-empty only sessions recorded in that project are
// returned — resuming into a different tree is refused (ADR-0005), so
// offering those in a list would only mislead.
//
// Transcripts with no conversation are left out for the same reason.
// They are easy to make — start gem-agent, run /help, quit — and being
// the newest file they would shadow the real session that --continue is
// meant to find, turning a resume into an error message.
func List(dir, projectDir string) ([]Meta, error) {
	var metas []Meta

	// Project subdirectories (ADR-0022): one project's own subdir, or —
	// for the all-projects listing — every subdir under projects/. A
	// marker mismatch (escape collision) skips the directory: those
	// files belong to another project, which lists them itself.
	if projectDir != "" {
		sub := projectSubdir(dir, projectDir)
		if ok, _ := statedir.MarkerMatches(sub, projectDir); ok {
			metas = append(metas, listDir(sub, projectDir)...)
		}
	} else if entries, err := os.ReadDir(filepath.Join(dir, "projects")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				metas = append(metas, listDir(filepath.Join(dir, "projects", e.Name()), "")...)
			}
		}
	}

	// Legacy flat files, read in place (ADR-0022 §3), header-filtered
	// exactly as before the layout change.
	metas = append(metas, listDir(dir, projectDir)...)

	sort.Slice(metas, func(i, j int) bool { return metas[i].LastActive.After(metas[j].LastActive) })
	return metas, nil
}

// Scan walks one transcript's records, decoding only the envelope and
// handing each payload to fn undecoded. It is the read path for
// consumers that want the diagnostic records Load discards — /learn
// reads the operator's own gate decisions this way (ADR-0045 §2).
//
// Unreadable lines are skipped rather than fatal, matching Load's
// tolerance: a torn tail must not cost the whole file. fn stops the walk
// by returning an error, which Scan returns as-is.
func Scan(path string, fn func(kind string, ts time.Time, data json.RawMessage) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return forEachLine(f, func(rec *rawRecord) (bool, error) {
		if rec == nil {
			return true, nil
		}
		if err := fn(rec.Kind, rec.Time, rec.Data); err != nil {
			return false, err
		}
		return true, nil
	})
}

// listDir describes the resumable sessions in one directory. Unreadable
// files and (when projectDir is non-empty) foreign-project headers are
// skipped — never fatal to a listing.
func listDir(dir, projectDir string) []Meta {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var metas []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		if !ValidID(id) {
			continue
		}
		meta, err := describe(filepath.Join(dir, e.Name()), id)
		if err != nil {
			continue
		}
		if projectDir != "" && meta.Header.Project != projectDir {
			continue
		}
		if !meta.HasConversation {
			continue
		}
		metas = append(metas, meta)
	}
	return metas
}

// Find describes one session by id, whether or not it holds a
// conversation. Resume by explicit id goes through here rather than
// List: an id the operator typed deserves the accurate answer ("that
// session has nothing in it") over List's "no such session".
func Find(dir, projectDir, id string) (Meta, error) {
	if !ValidID(id) {
		return Meta{}, fmt.Errorf("invalid session id %q", id)
	}
	path, err := findSessionFile(dir, projectDir, id)
	if err != nil {
		return Meta{}, err
	}
	return describe(path, id)
}

// FindByPrefix resolves what an operator typed: a full id, or a prefix
// of a UUID id that names exactly one session (ADR-0071 §1). The
// project's own sessions are searched first, then the legacy flat
// layout, then every other project — the last so a prefix typed in the
// wrong project still gets the informative refusal.
func FindByPrefix(dir, projectDir, typed string) (Meta, error) {
	if ValidID(typed) {
		return Find(dir, projectDir, typed)
	}
	if !prefixPattern.MatchString(typed) {
		return Meta{}, fmt.Errorf("%q is not a session id or a prefix of one", typed)
	}
	var matches []string
	seen := map[string]bool{}
	add := func(d string) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			id := strings.TrimSuffix(e.Name(), ".jsonl")
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || !ValidID(id) || !strings.HasPrefix(id, typed) || seen[id] {
				continue
			}
			seen[id] = true
			matches = append(matches, id)
		}
	}
	add(projectSubdir(dir, projectDir))
	if len(matches) == 0 {
		add(dir)
	}
	if len(matches) == 0 {
		if entries, err := os.ReadDir(filepath.Join(dir, "projects")); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					add(filepath.Join(dir, "projects", e.Name()))
				}
			}
		}
	}
	switch len(matches) {
	case 0:
		return Meta{}, fmt.Errorf("no session starts with %q", typed)
	case 1:
		return Find(dir, projectDir, matches[0])
	}
	sort.Strings(matches)
	return Meta{}, fmt.Errorf("%q matches %d sessions (%s…); type more characters", typed, len(matches), strings.Join(matches, ", "))
}

// findSessionFile locates one session by id: the project subdirectory
// first, then the legacy flat layout, then every other project's
// subdirectory — the last so that an id typed in the wrong project
// still resolves and gets the informative refusal ("recorded in X, run
// gem-agent there") instead of "no such session".
func findSessionFile(dir, projectDir, id string) (string, error) {
	candidates := []string{
		filepath.Join(projectSubdir(dir, projectDir), id+".jsonl"),
		filepath.Join(dir, id+".jsonl"), // legacy flat (ADR-0022 §3)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "projects")); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(dir, "projects", e.Name(), id+".jsonl")
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("no session %s", id)
}

// Latest returns the most recent session for a project, if any.
func Latest(dir, projectDir string) (Meta, bool, error) {
	metas, err := List(dir, projectDir)
	if err != nil || len(metas) == 0 {
		return Meta{}, false, err
	}
	return metas[0], true, nil
}

// describe reads a session's header and first user message without
// loading the whole conversation — a listing must stay cheap even when
// the transcripts are large.
func describe(path, id string) (Meta, error) {
	f, err := os.Open(path)
	if err != nil {
		return Meta{}, err
	}
	defer func() { _ = f.Close() }()

	meta := Meta{ID: id, Path: path}
	if st, err := f.Stat(); err == nil {
		meta.Size = st.Size()
		meta.LastActive = st.ModTime()
	}
	scanned := 0
	_ = forEachLine(f, func(rec *rawRecord) (bool, error) {
		scanned++
		if scanned > previewScanLimit {
			return false, nil
		}
		if rec == nil {
			return true, nil // corrupt line: a listing tolerates it
		}
		switch rec.Kind {
		case KindHeader:
			_ = json.Unmarshal(rec.Data, &meta.Header)
			meta.Started = rec.Time
		case KindMessage, KindCompaction:
			meta.HasConversation = true
			if rec.Kind != KindMessage {
				break
			}
			var m llm.Message
			if err := json.Unmarshal(rec.Data, &m); err != nil {
				break
			}
			if m.Role != llm.RoleUser || strings.TrimSpace(m.Content) == "" {
				break
			}
			if p := previewOf(m.Content); p != "" && betterPreview(meta.Preview, p) {
				meta.Preview = p
			}
		}
		done := meta.Preview != "" && !strings.HasPrefix(meta.Preview, "!") && !meta.Started.IsZero()
		return !done, nil
	})
	if meta.Started.IsZero() {
		meta.Started = meta.LastActive
	}
	return meta, nil
}

const (
	// previewScanLimit bounds how far into a file a listing reads looking
	// for the first user message.
	previewScanLimit = 64
	previewChars     = 72
)

// previewOf renders one user message for the listing. A `!` shell
// context message is shown as the command the operator ran, not as the
// wrapper sentence the agent injected around it.
func previewOf(content string) string {
	if !strings.HasPrefix(content, ShellContextPrefix) {
		return firstLine(content, previewChars)
	}
	for _, line := range strings.Split(content, "\n") {
		if cmd, ok := strings.CutPrefix(line, "$ "); ok {
			return firstLine("!"+cmd, previewChars)
		}
	}
	return ""
}

// betterPreview reports whether candidate should replace current. A
// typed message always beats a `!` command, whatever the order: the
// question the session was about is what makes it recognisable in a list
// of timestamps.
func betterPreview(current, candidate string) bool {
	if current == "" {
		return true
	}
	return strings.HasPrefix(current, "!") && !strings.HasPrefix(candidate, "!")
}

func firstLine(s string, limit int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return s
}

// InUse reports whether a session is currently held open by a running
// gem-agent process. Every live logger holds a non-blocking exclusive
// flock on its transcript for its lifetime (see lockSession), so a
// shared-flock probe answering EWOULDBLOCK means running. Advisory like
// the lock it probes: a missing transcript, or a state root on a
// filesystem without flock, reports not-in-use — callers must fail
// toward a human reading a list, never toward silent action (ADR-0059).
func InUse(dir, projectDir, id string) bool {
	if !ValidID(id) {
		return false
	}
	// The same lookup Reopen uses: a legacy flat transcript is resumed
	// in place and holds its lock there (review round 4 — the probe
	// looked only in the project subdirectory and reported a live
	// legacy session as free).
	path, err := findSessionFile(dir, projectDir, id)
	if err != nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return err == syscall.EWOULDBLOCK
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

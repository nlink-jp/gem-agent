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
	"time"

	"github.com/nlink-jp/gem-agent/internal/llm"
)

// SchemaVersion is the transcript format version. It is written into
// every session header so a file this build cannot replay is reported as
// such rather than half-loaded.
const SchemaVersion = 1

// Record kinds that carry conversation state. Everything else in the
// file is diagnostic and skipped by Load.
const (
	KindHeader     = "session"
	KindMessage    = "message"
	KindCompaction = "compaction"
	KindResumed    = "resumed"
)

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

// Logger appends records to one session file.
type Logger struct {
	mu sync.Mutex
	f  *os.File
	id string
}

// DefaultDir returns the org-standard state location for session logs.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gem-agent", "sessions"), nil
}

// idPattern matches the ids Open generates and nothing else. Resume
// accepts an id, never a path: an id that cannot contain a separator
// cannot escape the sessions directory, and cannot name a transcript
// somebody else placed (ADR-0005).
var idPattern = regexp.MustCompile(`^\d{8}-\d{6}(-\d+)?$`)

// ValidID reports whether s is a well-formed session id.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// Open creates dir if needed and starts a new session file named by
// timestamp.
func Open(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := time.Now().Format("20060102-150405")
	// O_EXCL prevents two same-second sessions from silently sharing one
	// file; on collision, retry with a numeric suffix.
	for i := 0; i < 100; i++ {
		id := base
		if i > 0 {
			id = fmt.Sprintf("%s-%d", base, i+1)
		}
		f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
		if err == nil {
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
// continues its own transcript rather than starting a second one.
func Reopen(dir, id string) (*Logger, error) {
	if !ValidID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	f, err := os.OpenFile(filepath.Join(dir, id+".jsonl"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Logger{f: f, id: id}, nil
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
	if _, err := l.f.Write(append(line, '\n')); err != nil {
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

// Load reads a session file back into a conversation history, applying
// recorded compactions on the way (ADR-0006): a session that was
// compacted resumes compacted, not re-inflated.
//
// A record this build does not understand is skipped, not fatal — a
// diagnostic line must never make a conversation unresumable — but a
// newer schema version is refused outright, because then the message
// records themselves may not mean what this build thinks they do.
func Load(path string) ([]llm.Message, Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, Header{}, err
	}
	defer f.Close()

	// json.Decoder rather than bufio.Scanner: a single read_file result
	// can exceed Scanner's 64KB token limit, which would truncate the
	// conversation silently.
	dec := json.NewDecoder(f)
	var (
		header  Header
		history []llm.Message
	)
	for {
		var rec struct {
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// A session killed mid-write leaves a partial last line. The
			// conversation up to it is still good; refusing to resume
			// because of a torn byte would waste the whole transcript.
			break
		}
		switch rec.Kind {
		case KindHeader:
			if err := json.Unmarshal(rec.Data, &header); err != nil {
				return nil, Header{}, fmt.Errorf("%s: unreadable session header: %w", path, err)
			}
			if header.Schema > SchemaVersion {
				return nil, header, fmt.Errorf("%s was written by a newer gem-agent (transcript schema %d, this build reads %d)",
					filepath.Base(path), header.Schema, SchemaVersion)
			}
		case KindMessage:
			var m llm.Message
			if err := json.Unmarshal(rec.Data, &m); err != nil {
				return nil, header, fmt.Errorf("%s: unreadable message record: %w", path, err)
			}
			history = append(history, m)
		case KindCompaction:
			var c Compaction
			if err := json.Unmarshal(rec.Data, &c); err != nil {
				return nil, header, fmt.Errorf("%s: unreadable compaction record: %w", path, err)
			}
			if c.Replaced < 0 || c.Replaced > len(history) {
				return nil, header, fmt.Errorf("%s: compaction record replaces %d of %d messages", path, c.Replaced, len(history))
			}
			history = append([]llm.Message{c.Message}, history[c.Replaced:]...)
		}
	}
	return history, header, nil
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
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
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
			continue // an unreadable file is skipped, never fatal to a listing
		}
		if projectDir != "" && meta.Header.Project != projectDir {
			continue
		}
		if !meta.HasConversation {
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].LastActive.After(metas[j].LastActive) })
	return metas, nil
}

// Find describes one session by id, whether or not it holds a
// conversation. Resume by explicit id goes through here rather than
// List: an id the operator typed deserves the accurate answer ("that
// session has nothing in it") over List's "no such session".
func Find(dir, id string) (Meta, error) {
	if !ValidID(id) {
		return Meta{}, fmt.Errorf("invalid session id %q", id)
	}
	return describe(filepath.Join(dir, id+".jsonl"), id)
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
	defer f.Close()

	meta := Meta{ID: id, Path: path}
	if st, err := f.Stat(); err == nil {
		meta.Size = st.Size()
		meta.LastActive = st.ModTime()
	}
	dec := json.NewDecoder(f)
	for i := 0; i < previewScanLimit; i++ {
		var rec struct {
			Time time.Time       `json:"ts"`
			Kind string          `json:"kind"`
			Data json.RawMessage `json:"data"`
		}
		if err := dec.Decode(&rec); err != nil {
			break
		}
		switch rec.Kind {
		case KindHeader:
			_ = json.Unmarshal(rec.Data, &meta.Header)
			meta.Started = rec.Time
		case KindMessage, KindCompaction:
			meta.HasConversation = true
			if rec.Kind != KindMessage {
				continue
			}
			var m llm.Message
			if err := json.Unmarshal(rec.Data, &m); err != nil {
				continue
			}
			if m.Role != llm.RoleUser || strings.TrimSpace(m.Content) == "" {
				continue
			}
			if p := previewOf(m.Content); p != "" && betterPreview(meta.Preview, p) {
				meta.Preview = p
			}
		}
		if meta.Preview != "" && !strings.HasPrefix(meta.Preview, "!") && !meta.Started.IsZero() {
			break
		}
	}
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

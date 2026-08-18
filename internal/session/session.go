// Package session appends session records to a JSONL log file. v1 records
// only (no resume — v2 scope per the RFP).
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Record is one JSONL line.
type Record struct {
	Time time.Time `json:"ts"`
	Kind string    `json:"kind"`
	Data any       `json:"data"`
}

// Logger appends records to one session file.
type Logger struct {
	mu sync.Mutex
	f  *os.File
}

// DefaultDir returns the org-standard state location for session logs.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "gem-agent", "sessions"), nil
}

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
		name := base + ".jsonl"
		if i > 0 {
			name = fmt.Sprintf("%s-%d.jsonl", base, i+1)
		}
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
		if err == nil {
			return &Logger{f: f}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not allocate a session file in %s", dir)
}

// Path returns the session file path.
func (l *Logger) Path() string { return l.f.Name() }

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

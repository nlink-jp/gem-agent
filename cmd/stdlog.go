package cmd

import (
	"bytes"
	"log"
	"strings"
	"sync"
)

// quietStdLog moves the standard logger off the terminal while the TUI
// owns it, and returns the restore. gem-agent itself never logs there,
// but genai does — a failed stream, and hints such as a response too
// large to stream. Under the TUI that raw line would be drawn over the
// screen (ADR-0100 §3); the turn's own error already says what
// happened, so the line goes to the session log as a library_log
// record instead of being lost. The plain REPL and -p keep it on
// stderr.
func quietStdLog(record func(line string)) (restore func()) {
	prev, flags := log.Writer(), log.Flags()
	log.SetFlags(0) // the record has its own timestamp
	log.SetOutput(&lineSink{record: record})
	return func() {
		log.SetOutput(prev)
		log.SetFlags(flags)
	}
}

// lineSink hands each complete line to record. The standard logger
// writes one entry per Write, but a library may write in pieces.
type lineSink struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	record func(line string)
}

func (s *lineSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.Write(p)
	for {
		i := bytes.IndexByte(s.buf.Bytes(), '\n')
		if i < 0 {
			break
		}
		line := strings.TrimSpace(string(s.buf.Next(i + 1)))
		if line != "" {
			s.record(line)
		}
	}
	return len(p), nil
}

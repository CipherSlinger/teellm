package platformagent

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// Level is the severity of a captured log record.
type Level string

const (
	LevelDebug Level = "debug"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

// Entry is one structured log record. Seq increases monotonically from 1 and
// never repeats, which is what the platform's contiguous-batch contract needs.
type Entry struct {
	Seq       uint64
	Timestamp time.Time
	Level     Level
	Component string
	Message   string
}

// FormatMessage renders the record in the same shape TAA reports, so the
// platform's log viewer handles both without a branch.
func (e Entry) FormatMessage() string {
	ts := e.Timestamp
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	tsStr := ts.UTC().Format("2006-01-02 15:04:05")
	lvl := strings.ToUpper(string(e.Level))
	if lvl == "" {
		lvl = "INFO"
	}
	comp := e.Component
	if comp == "" {
		comp = "system"
	}
	return fmt.Sprintf("[%s] [%s] [%s] %s", tsStr, lvl, comp, e.Message)
}

// LogBuffer is a bounded, thread-safe log ring. It keeps the newest maxSize
// records and hands them out incrementally.
//
// It is a deliberate reimplementation of taa/pkg/logger.Store rather than an
// import: teellm is a separate module and cannot depend on taa internals.
type LogBuffer struct {
	mu      sync.Mutex
	entries []Entry
	maxSize int
	cursor  int
	nextSeq uint64
}

// NewLogBuffer creates a buffer holding at most maxSize records.
func NewLogBuffer(maxSize int) *LogBuffer {
	if maxSize <= 0 {
		maxSize = DefaultLogBufferSize
	}
	return &LogBuffer{
		entries: make([]Entry, 0, maxSize),
		maxSize: maxSize,
	}
}

// Add records one message and mirrors it to stderr, so an unreachable platform
// still leaves a local trail in teellm-service.log.
func (b *LogBuffer) Add(level Level, component, format string, args ...any) {
	if b == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	entry := Entry{
		Timestamp: time.Now().UTC(),
		Level:     level,
		Component: component,
		Message:   msg,
	}
	log.Printf("%s", entry.FormatMessage())

	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextSeq++
	entry.Seq = b.nextSeq

	// Drop the oldest half when full, and move the read cursor back with it so
	// it never points past the start of the slice.
	if len(b.entries) >= b.maxSize {
		drop := b.maxSize / 2
		if drop <= 0 {
			drop = 1
		}
		b.entries = append(b.entries[:0], b.entries[drop:]...)
		if b.cursor >= drop {
			b.cursor -= drop
		} else {
			b.cursor = 0
		}
	}
	b.entries = append(b.entries, entry)
}

// Drain returns everything recorded since the previous Drain and advances the
// read cursor.
func (b *LogBuffer) Drain() []Entry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.cursor >= len(b.entries) {
		return nil
	}
	out := make([]Entry, len(b.entries)-b.cursor)
	copy(out, b.entries[b.cursor:])
	b.cursor = len(b.entries)
	return out
}

// Count reports how many records the buffer currently holds.
func (b *LogBuffer) Count() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

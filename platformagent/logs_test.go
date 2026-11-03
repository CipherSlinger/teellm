package platformagent

import (
	"strings"
	"testing"
	"time"
)

func TestLogBufferAssignsContiguousSeq(t *testing.T) {
	buf := NewLogBuffer(10)
	buf.Add(LevelInfo, "teellm", "one")
	buf.Add(LevelWarn, "teellm", "two")

	entries := buf.Drain()
	if len(entries) != 2 {
		t.Fatalf("drained %d entries, want 2", len(entries))
	}
	if entries[0].Seq != 1 || entries[1].Seq != 2 {
		t.Fatalf("seq = %d,%d, want 1,2", entries[0].Seq, entries[1].Seq)
	}
	if entries[0].Message != "one" || entries[1].Message != "two" {
		t.Fatalf("unexpected messages: %+v", entries)
	}
}

func TestLogBufferDrainIsIncremental(t *testing.T) {
	buf := NewLogBuffer(10)
	buf.Add(LevelInfo, "teellm", "one")
	_ = buf.Drain()
	buf.Add(LevelInfo, "teellm", "two")

	entries := buf.Drain()
	if len(entries) != 1 {
		t.Fatalf("drained %d entries, want 1", len(entries))
	}
	if entries[0].Seq != 2 {
		t.Fatalf("seq = %d, want 2", entries[0].Seq)
	}
}

func TestLogBufferDropsOldestWhenFull(t *testing.T) {
	buf := NewLogBuffer(4)
	for i := 0; i < 10; i++ {
		buf.Add(LevelInfo, "teellm", "line")
	}
	if got := buf.Count(); got > 4 {
		t.Fatalf("buffer holds %d entries, want at most 4", got)
	}
	entries := buf.Drain()
	for _, e := range entries {
		if e.Seq < 7 {
			t.Fatalf("kept seq %d, want only the newest", e.Seq)
		}
	}
}

func TestFormatMessageMatchesTAAShape(t *testing.T) {
	entry := Entry{
		Seq:       1,
		Timestamp: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC),
		Level:     LevelInfo,
		Component: "teellm",
		Message:   "ollama ready",
	}
	want := "[2026-11-02 10:00:00] [INFO] [teellm] ollama ready"
	if got := entry.FormatMessage(); got != want {
		t.Fatalf("FormatMessage = %q, want %q", got, want)
	}
}

func TestFormatMessageDefaults(t *testing.T) {
	entry := Entry{Seq: 2, Timestamp: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)}
	got := entry.FormatMessage()
	if !strings.Contains(got, "[INFO] [system]") {
		t.Fatalf("FormatMessage = %q, want the INFO/system defaults", got)
	}
}

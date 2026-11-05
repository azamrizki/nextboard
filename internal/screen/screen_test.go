package screen

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModelBasics(t *testing.T) {
	s := New(40, 6, nil)
	defer s.Close()
	s.Feed([]byte("hello \x1b[1mworld\x1b[0m\r\nline two ✓ 日本\r\n"))
	s.Feed([]byte("\x1b[?2004h> prompt"))
	s.Sync()
	sn := s.Snapshot()
	if len(sn.Lines) != 6 || sn.Lines[0] != "hello world" || sn.Lines[1] != "line two ✓ 日本" || sn.Lines[2] != "> prompt" {
		t.Fatalf("lines %q", sn.Lines)
	}
	if sn.CursorY != 2 || sn.CursorX != len("> prompt") || !sn.BracketedPaste || sn.Alt {
		t.Fatalf("cursor %d,%d paste %v alt %v", sn.CursorX, sn.CursorY, sn.BracketedPaste, sn.Alt)
	}
	s.Feed([]byte("\x1b[?1049h\x1b[2J\x1b[Halt view\x1b[?2004l"))
	s.Sync()
	if sn := s.Snapshot(); !sn.Alt || sn.Lines[0] != "alt view" || sn.BracketedPaste {
		t.Fatalf("alt %+v", sn)
	}
	s.Resize(20, 3)
	if sn := s.Snapshot(); len(sn.Lines) != 3 {
		t.Fatalf("resize %d", len(sn.Lines))
	}
}

func TestQueriesNeverBlock(t *testing.T) {
	s := New(80, 24, nil) // no respond: answers are discarded
	defer s.Close()
	for i := 0; i < 200; i++ {
		s.Feed([]byte("\x1b[c\x1b[>c\x1b[6n\x1b]10;?\x07\x1b]11;?\x07"))
	}
	done := make(chan struct{})
	go func() { s.Sync(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("model blocked on unanswered queries")
	}
	var mu sync.Mutex
	var got bytes.Buffer
	r := New(80, 24, func(b []byte) { mu.Lock(); got.Write(b); mu.Unlock() })
	defer r.Close()
	r.Feed([]byte("ab\x1b[6n"))
	r.Sync()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		ok := strings.Contains(got.String(), "\x1b[1;3R")
		mu.Unlock()
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("respond mode: %q", got.String())
}

func TestOverflowResetsAndMarksStale(t *testing.T) {
	s := New(80, 24, nil)
	defer s.Close()
	s.qmu.Lock() // hold the loop so the queue fills
	s.queue = append(s.queue, bytes.Repeat([]byte("x"), QueueMax-10))
	s.queued = QueueMax - 10
	s.qmu.Unlock()
	s.Feed(bytes.Repeat([]byte("y"), 100))
	if !s.Snapshot().Stale {
		t.Fatal("not stale after overflow")
	}
	s.Feed([]byte("partial"))
	s.Sync()
	if !s.Snapshot().Stale {
		t.Fatal("stale cleared before a redraw")
	}
	s.Feed([]byte("\x1b[2J\x1b[Hfresh"))
	s.Sync()
	if sn := s.Snapshot(); sn.Stale || sn.Lines[0] != "fresh" {
		t.Fatalf("after redraw %+v", sn.Lines[:2])
	}
}

func TestReplayLarge(t *testing.T) {
	s := New(120, 32, nil)
	defer s.Close()
	var b bytes.Buffer
	for b.Len() < ReplayBytes {
		b.WriteString("\x1b[32m✓\x1b[0m some output line with colors and ünïcode\r\n")
	}
	start := time.Now()
	s.Feed(b.Bytes())
	s.Sync()
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("replay of 256 KiB took %v", d)
	}
}

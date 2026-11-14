package screen

import (
	"strings"
	"testing"
)

// A parked screen lets its emulator go, still shows what it showed, and takes output again
// from there.
func TestParkKeepsTheScreen(t *testing.T) {
	s := New(40, 6, nil)
	defer s.Close()
	s.Feed([]byte("\x1b[?2004hfirst line\r\nsecond line\r\n> typed"))
	s.Sync()
	before := s.Snapshot()
	s.Park()
	if !s.Parked() {
		t.Fatal("not parked")
	}
	after := s.Snapshot()
	if after.Text() != before.Text() || after.CursorX != before.CursorX || after.CursorY != before.CursorY || !after.BracketedPaste {
		t.Fatalf("parked screen differs:\n%q %d,%d %v\nwas\n%q %d,%d", after.Text(), after.CursorX, after.CursorY, after.BracketedPaste, before.Text(), before.CursorX, before.CursorY)
	}
	s.Park() // again: nothing changes
	s.Feed([]byte(" more"))
	s.Sync()
	if s.Parked() {
		t.Fatal("still parked after output")
	}
	sn := s.Snapshot()
	if !strings.Contains(sn.Text(), "second line") || !strings.Contains(sn.Text(), "> typed more") || !sn.BracketedPaste {
		t.Fatalf("output after the park does not follow on: %q paste=%v", sn.Text(), sn.BracketedPaste)
	}
	// a full screen in the alternate screen comes back as it was, and Reset empties a parked one
	s.Feed([]byte("\x1b[?1049h\x1b[2J\x1b[1;1Htop\x1b[6;1Hbottom"))
	s.Sync()
	s.Park()
	s.Resize(50, 6)
	s.Feed([]byte("!"))
	s.Sync()
	if sn := s.Snapshot(); !sn.Alt || sn.Lines[0] != "top" || sn.Lines[5] != "bottom!" {
		t.Fatalf("alternate screen after park: alt=%v %q", sn.Alt, sn.Lines)
	}
	s.Park()
	s.Reset()
	if sn := s.Snapshot(); strings.TrimSpace(sn.Text()) != "" {
		t.Fatalf("reset of a parked screen kept %q", sn.Text())
	}
}

// Parking frees what the emulator held.
func TestParkFreesTheEmulator(t *testing.T) {
	var screens []*Screen
	before := heapMB()
	for range 20 {
		s := New(200, 60, nil)
		s.Feed([]byte(strings.Repeat("some output on a line of its own\r\n", 100)))
		screens = append(screens, s)
	}
	for _, s := range screens {
		s.Sync()
	}
	awake := heapMB() - before
	for _, s := range screens {
		s.Park()
	}
	parked := heapMB() - before
	t.Logf("20 screens 200x60: %.1f MB live, %.1f MB parked", awake, parked)
	if parked > awake/3 {
		t.Fatalf("parked screens still hold %.1f MB of %.1f", parked, awake)
	}
	for _, s := range screens {
		s.Close()
	}
}

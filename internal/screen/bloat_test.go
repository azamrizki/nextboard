package screen

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func heapMB() float64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / 1e6
}

// TestScreenMemoryIsBounded: a console that prints for hours keeps only what a
// screen needs in the hub's memory, in the main screen and in the alternate one (where the
// emulator's history has no size to set: slim turns it off).
func TestScreenMemoryIsBounded(t *testing.T) {
	for _, alt := range []bool{false, true} {
		t.Run(map[bool]string{false: "main", true: "alternate"}[alt], func(t *testing.T) { screenBounded(t, alt) })
	}
}

func screenBounded(t *testing.T, alt bool) {
	before := heapMB()
	s := NewModel(120, 32, func([]byte) {})
	if c, ok := s.(interface{ close() }); ok {
		defer c.close()
	}
	// a new screen is small: not the parser's 4 MB buffer for control strings
	if fresh := heapMB() - before; fresh > 2 {
		t.Fatalf("a new screen holds %.1f MB", fresh)
	}
	if alt {
		s.Write([]byte("\x1b[?1049h"))
	}
	line := []byte(strings.Repeat("x", 118) + "\r\n")
	for round := 1; round <= 4; round++ {
		for i := 0; i < 5000; i++ {
			s.Write(line)
		}
		t.Logf("%d lines: heap +%.1f MB", round*5000, heapMB()-before)
	}
	if grown := heapMB() - before; grown > 8 {
		t.Fatal(fmt.Sprintf("one screen holds %.1f MB", grown))
	}
	if l := s.Lines(); !strings.Contains(strings.Join(l, ""), "xxxx") {
		t.Fatalf("the screen must still show its lines: %q", l)
	}
	runtime.KeepAlive(s)
}

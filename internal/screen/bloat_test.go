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
// screen needs in the hub's memory.
func TestScreenMemoryIsBounded(t *testing.T) {
	before := heapMB()
	s := NewModel(120, 32, func([]byte) {})
	if c, ok := s.(interface{ close() }); ok {
		defer c.close()
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

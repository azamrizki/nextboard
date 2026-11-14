package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestRingNoWrap(t *testing.T) {
	r := NewRing(16)
	r.Write([]byte("hello "))
	r.Write([]byte("world"))
	if got := string(r.Bytes()); got != "hello world" {
		t.Fatalf("got %q", got)
	}
}

func TestRingWrapKeepsNewest(t *testing.T) {
	r := NewRing(10)
	r.Write([]byte("aaaa\nbbbb\ncccc"))
	got := r.Bytes()
	// newest 10 bytes are "bbb\ncccc" plus one more; cut at first newline
	if !bytes.HasSuffix(got, []byte("cccc")) || bytes.Contains(got, []byte("aaaa")) {
		t.Fatalf("got %q", got)
	}
	r2 := NewRing(8)
	for i := 0; i < 5; i++ {
		r2.Write([]byte("xy\n"))
	}
	if r2.Len() != 8 {
		t.Fatalf("len %d", r2.Len())
	}
	if got := string(r2.Bytes()); got != "xy\nxy\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRingExactFill(t *testing.T) {
	r := NewRing(4)
	r.Write([]byte("ab"))
	r.Write([]byte("cd"))
	if got := string(r.Bytes()); got != "abcd" {
		t.Fatalf("got %q", got)
	}
	r.Write([]byte("e"))
	if got := string(r.Bytes()); got != "bcde" {
		t.Fatalf("got %q", got)
	}
}

// A ring takes its size as output arrives, and holds the same bytes as one that had it at once.
func TestRingGrowsWithOutput(t *testing.T) {
	const size = 64 << 10
	r := NewRing(size)
	if len(r.buf) != 0 {
		t.Fatalf("an empty ring holds %d bytes", len(r.buf))
	}
	r.Write([]byte("first\n"))
	if len(r.buf) > ringStart {
		t.Fatalf("a ring with one line holds %d bytes", len(r.buf))
	}
	var all []byte
	all = append(all, "first\n"...)
	for i := 0; len(all) < 3*size; i++ {
		line := []byte(strings.Repeat(string(rune('a'+i%26)), 1+i%300) + "\n")
		r.Write(line)
		all = append(all, line...)
		if len(r.buf) > size {
			t.Fatalf("the ring grew to %d, over its %d", len(r.buf), size)
		}
		want := all
		if len(want) > size {
			want = want[len(want)-size:]
			if j := bytes.IndexByte(want, '\n'); j >= 0 {
				want = want[j+1:]
			}
		}
		if got := r.Bytes(); !bytes.Equal(got, want) {
			t.Fatalf("after %d bytes the ring holds %d bytes, want %d; tail %q, want %q", len(all), len(got), len(want), tail(got), tail(want))
		}
	}
	if r.Len() != size {
		t.Fatalf("len %d", r.Len())
	}
}

func tail(b []byte) []byte { return b[max(0, len(b)-20):] }

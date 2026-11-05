package session

import (
	"bytes"
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

package machine

import (
	"net"
	"slices"
	"testing"
	"time"
)

// The hook forward tries the port it had, then 65500–65535 in order, each once.
func TestForwardPorts(t *testing.T) {
	var all []int
	for p := 65500; p <= 65535; p++ {
		all = append(all, p)
	}
	if got := forwardPorts(0); !slices.Equal(got, all) {
		t.Errorf("fresh: %v", got)
	}
	if got := forwardPorts(65510); got[0] != 65510 || len(got) != 36 || slices.Index(got[1:], 65510) >= 0 {
		t.Errorf("last in range: %v", got)
	}
	// a port from before the range (an older hub): asked for first, then the range
	if got := forwardPorts(41234); got[0] != 41234 || !slices.Equal(got[1:], all) {
		t.Errorf("last out of range: %v", got)
	}
}

// At most n hook connections at a time; a closed one frees its place, once.
func TestLimitListener(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := limitListener(inner, 2)
	defer ln.Close()
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", inner.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	for range 3 {
		defer dial().Close()
	}
	wait := func(want int) []net.Conn {
		var got []net.Conn
		deadline := time.After(300 * time.Millisecond)
		for {
			select {
			case c := <-accepted:
				got = append(got, c)
			case <-deadline:
				if len(got) != want {
					t.Fatalf("accepted %d, want %d", len(got), want)
				}
				return got
			}
		}
	}
	first := wait(2)
	first[0].Close()
	first[0].Close() // twice: frees one place, not two
	wait(1)
}

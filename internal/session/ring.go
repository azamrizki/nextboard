// Package session runs terminals (local or remote), keeps their scrollback
// in a ring buffer and fans output out to any number of subscribers.
package session

import "bytes"

// ringStart is what a ring holds before its first growth.
const ringStart = 4 << 10

// Ring is a byte ring buffer holding the newest output, up to a fixed size. It takes that
// size only as output arrives: a console that printed little holds little.
type Ring struct {
	buf  []byte // what is allocated so far; its length is max once the ring has wrapped
	max  int
	w    int
	full bool
}

// NewRing returns a ring that keeps up to size bytes.
func NewRing(size int) *Ring { return &Ring{max: size} }

// grow makes room for n bytes. Only before the ring has wrapped: its contents are buf[:w].
func (r *Ring) grow(n int) {
	if n <= len(r.buf) {
		return
	}
	nb := make([]byte, n)
	copy(nb, r.buf[:r.w])
	r.buf = nb
}

// Write appends p, overwriting the oldest bytes when full.
func (r *Ring) Write(p []byte) {
	if len(p) == 0 || r.max <= 0 {
		return
	}
	if len(p) >= r.max {
		r.grow(r.max)
		copy(r.buf, p[len(p)-r.max:])
		r.w, r.full = 0, true
		return
	}
	if need := r.w + len(p); !r.full && need > len(r.buf) {
		r.grow(min(r.max, max(need, 2*len(r.buf), ringStart)))
	}
	k := copy(r.buf[r.w:], p)
	if k < len(p) {
		copy(r.buf, p[k:])
	}
	r.w += len(p)
	if r.w >= r.max { // the buffer has its full size by now
		r.w -= r.max
		r.full = true
	}
}

// Bytes returns the buffered output, oldest first. When the ring has
// wrapped, output before the first newline is dropped so the snapshot does
// not start in the middle of an escape sequence or UTF-8 rune.
func (r *Ring) Bytes() []byte {
	if !r.full {
		return append([]byte(nil), r.buf[:r.w]...)
	}
	out := make([]byte, 0, len(r.buf))
	out = append(out, r.buf[r.w:]...)
	out = append(out, r.buf[:r.w]...)
	if i := bytes.IndexByte(out, '\n'); i >= 0 && i < 64*1024 {
		out = out[i+1:]
	}
	return out
}

// Len returns the number of buffered bytes.
func (r *Ring) Len() int {
	if r.full {
		return len(r.buf)
	}
	return r.w
}

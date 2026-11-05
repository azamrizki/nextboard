// Package session runs terminals (local or remote), keeps their scrollback
// in a ring buffer and fans output out to any number of subscribers.
package session

import "bytes"

// Ring is a fixed-size byte ring buffer holding the newest output.
type Ring struct {
	buf  []byte
	w    int
	full bool
}

// NewRing allocates a ring of size bytes.
func NewRing(size int) *Ring { return &Ring{buf: make([]byte, size)} }

// Write appends p, overwriting the oldest bytes when full.
func (r *Ring) Write(p []byte) {
	n := len(r.buf)
	if len(p) >= n {
		copy(r.buf, p[len(p)-n:])
		r.w, r.full = 0, true
		return
	}
	k := copy(r.buf[r.w:], p)
	if k < len(p) {
		copy(r.buf, p[k:])
		r.full = true
	}
	r.w = (r.w + len(p)) % n
	if r.w == 0 && len(p) > 0 {
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

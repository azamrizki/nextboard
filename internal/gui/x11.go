package gui

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// xconn is the small part of the X11 protocol the monitor needs: which top
// level windows exist on a display and what the active one is called. The
// hub builds without cgo, and one synchronous request at a time is enough.
type xconn struct {
	c       net.Conn
	root    uint32
	ridBase uint32
	atoms   map[string]uint32
}

const xTimeout = 5 * time.Second

// xError is an error reply, as opposed to a broken connection.
type xError struct{ code byte }

func (e *xError) Error() string { return fmt.Sprintf("x11 error %d", e.code) }

var le = binary.LittleEndian

func pad4(n int) int { return (4 - n%4) % 4 }

func dialX(display int, cookie []byte) (*xconn, error) {
	c, err := net.DialTimeout("unix", fmt.Sprintf("/tmp/.X11-unix/X%d", display), 2*time.Second)
	if err != nil {
		return nil, err
	}
	x := &xconn{c: c, atoms: map[string]uint32{}}
	if err := x.setup(cookie); err != nil {
		c.Close()
		return nil, err
	}
	return x, nil
}

func (x *xconn) close() { x.c.Close() }

func (x *xconn) setup(cookie []byte) error {
	name := "MIT-MAGIC-COOKIE-1"
	if len(cookie) == 0 {
		name = "" // Xwayland takes no cookie
	}
	req := make([]byte, 12)
	req[0] = 'l'
	le.PutUint16(req[2:], 11)
	le.PutUint16(req[6:], uint16(len(name)))
	le.PutUint16(req[8:], uint16(len(cookie)))
	req = append(req, name...)
	req = append(req, make([]byte, pad4(len(name)))...)
	req = append(req, cookie...)
	req = append(req, make([]byte, pad4(len(cookie)))...)
	_ = x.c.SetDeadline(time.Now().Add(xTimeout))
	if _, err := x.c.Write(req); err != nil {
		return err
	}
	head := make([]byte, 8)
	if _, err := io.ReadFull(x.c, head); err != nil {
		return err
	}
	body := make([]byte, int(le.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(x.c, body); err != nil {
		return err
	}
	if head[0] != 1 {
		reason := string(body)
		if n := int(head[1]); head[0] == 0 && n <= len(body) {
			reason = string(body[:n])
		}
		return fmt.Errorf("x11 setup ditolak: %s", strings.TrimRight(reason, "\x00\n "))
	}
	// Offsets below are the setup reply's, minus the 8 byte head.
	if len(body) < 32 {
		return errors.New("x11 setup: balasan terlalu pendek")
	}
	vendor := int(le.Uint16(body[16:]))
	formats := int(body[21])
	off := 32 + vendor + pad4(vendor) + 8*formats
	if len(body) < off+4 {
		return errors.New("x11 setup: balasan terlalu pendek")
	}
	x.ridBase = le.Uint32(body[4:])
	x.root = le.Uint32(body[off:])
	return nil
}

// request sends one request and returns its reply.
func (x *xconn) request(req []byte) ([]byte, error) {
	_ = x.c.SetDeadline(time.Now().Add(xTimeout))
	if _, err := x.c.Write(req); err != nil {
		return nil, err
	}
	for {
		head := make([]byte, 32)
		if _, err := io.ReadFull(x.c, head); err != nil {
			return nil, err
		}
		switch head[0] {
		case 0:
			return nil, &xError{code: head[1]}
		case 1:
			extra := make([]byte, int(le.Uint32(head[4:]))*4)
			if _, err := io.ReadFull(x.c, extra); err != nil {
				return nil, err
			}
			return append(head, extra...), nil
		}
		// An event: none are selected, but the protocol allows them anyway.
	}
}

func (x *xconn) atom(name string) (uint32, error) {
	if a, ok := x.atoms[name]; ok {
		return a, nil
	}
	req := make([]byte, 8+len(name)+pad4(len(name)))
	req[0] = 16 // InternAtom
	le.PutUint16(req[2:], uint16(len(req)/4))
	le.PutUint16(req[4:], uint16(len(name)))
	copy(req[8:], name)
	r, err := x.request(req)
	if err != nil {
		return 0, err
	}
	a := le.Uint32(r[8:])
	x.atoms[name] = a
	return a, nil
}

// property reads up to maxLen 32-bit units of a window property.
func (x *xconn) property(win uint32, name string, maxLen uint32) ([]byte, byte, error) {
	a, err := x.atom(name)
	if err != nil {
		return nil, 0, err
	}
	req := make([]byte, 24)
	req[0] = 20 // GetProperty
	le.PutUint16(req[2:], 6)
	le.PutUint32(req[4:], win)
	le.PutUint32(req[8:], a)
	le.PutUint32(req[20:], maxLen)
	r, err := x.request(req)
	if err != nil {
		return nil, 0, err
	}
	format := r[1]
	n := int(le.Uint32(r[16:])) * int(format) / 8
	n = min(n, len(r)-32)
	return r[32 : 32+n], format, nil
}

func (x *xconn) windowList(win uint32, name string) ([]uint32, error) {
	v, format, err := x.property(win, name, 1024)
	if err != nil {
		return nil, err
	}
	if format != 32 {
		return nil, nil
	}
	out := make([]uint32, len(v)/4)
	for i := range out {
		out[i] = le.Uint32(v[i*4:])
	}
	return out, nil
}

// restrictToUser leaves only this user's local connections on the access list.
func (x *xconn) restrictToUser(name string) error {
	const (
		changeHosts      = 109
		setAccessControl = 111
		insert           = 0
		remove           = 1
		serverInterp     = 5
		localHost        = 252
	)
	hosts := func(mode, family byte, addr string) []byte {
		req := make([]byte, 8+len(addr)+pad4(len(addr)))
		req[0] = changeHosts
		req[1] = mode
		le.PutUint16(req[2:], uint16(len(req)/4))
		req[4] = family
		le.PutUint16(req[6:], uint16(len(addr)))
		copy(req[8:], addr)
		return req
	}
	// First allow the user, so losing "local" can't lock the hub out.
	reqs := hosts(insert, serverInterp, "localuser\x00"+name)
	reqs = append(reqs, hosts(remove, localHost, "")...)
	reqs = append(reqs, setAccessControl, 1, 1, 0)
	_ = x.c.SetDeadline(time.Now().Add(xTimeout))
	if _, err := x.c.Write(reqs); err != nil {
		return err
	}
	// These have no reply: a request that has one flushes out any error they caused.
	_, err := x.windowList(x.root, "_NET_CLIENT_LIST")
	return err
}

// windows implements displayWatch.
func (x *xconn) windows() (int, string, error) {
	clients, err := x.clients()
	if err != nil {
		return 0, "", err
	}
	if len(clients) == 0 {
		return 0, "", nil
	}
	title, err := x.activeTitle(clients)
	if err != nil {
		return len(clients), "", nil // the next call sees a dead display
	}
	return len(clients), title, nil
}

// hasWM reports whether a window manager announced itself on the display.
func (x *xconn) hasWM() (bool, error) {
	l, err := x.windowList(x.root, "_NET_SUPPORTING_WM_CHECK")
	return len(l) > 0, err
}

// clients lists the windows the window manager manages.
func (x *xconn) clients() ([]uint32, error) { return x.windowList(x.root, "_NET_CLIENT_LIST") }

// activeTitle names the focused window, or the newest one without focus. A
// window that vanished between two requests is not an error.
func (x *xconn) activeTitle(clients []uint32) (string, error) {
	active, err := x.windowList(x.root, "_NET_ACTIVE_WINDOW")
	if err != nil {
		return "", err
	}
	win := uint32(0)
	if len(active) > 0 {
		win = active[0]
	}
	if win == 0 && len(clients) > 0 {
		win = clients[len(clients)-1]
	}
	if win == 0 {
		return "", nil
	}
	for _, prop := range []string{"_NET_WM_NAME", "WM_NAME"} {
		v, _, err := x.property(win, prop, 256)
		var xe *xError
		if errors.As(err, &xe) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		if len(v) > 0 {
			return strings.ToValidUTF8(string(v), ""), nil
		}
	}
	return "", nil
}

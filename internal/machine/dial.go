package machine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// ErrForwardingDisabled: the SSH server refuses direct-tcpip channels
// (AllowTcpForwarding no).
var ErrForwardingDisabled = errors.New("sshd refuses port forwarding (AllowTcpForwarding)")

// ErrPortClosed: nothing listens on the loopback port.
var ErrPortClosed = errors.New("port closed")

// DialLoopback opens a TCP connection to 127.0.0.1:port, then [::1]:port,
// on machine h: a local dial on This PC, a direct-tcpip channel over the
// existing SSH connection on remote machines (docs/API-studio.md §4.4).
// Only loopback addresses are ever dialled.
func DialLoopback(ctx context.Context, h Host, port int) (net.Conn, error) {
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", port)
	}
	var dial func(ctx context.Context, addr string) (net.Conn, error)
	switch m := h.(type) {
	case *Local:
		var d net.Dialer
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return d.DialContext(ctx, "tcp", addr) }
	case *SSH:
		c := m.currentClient()
		if c == nil {
			return nil, ErrNotConnected
		}
		dial = func(ctx context.Context, addr string) (net.Conn, error) { return c.DialContext(ctx, "tcp", addr) }
	default:
		return nil, errors.New("unsupported machine")
	}
	p := strconv.Itoa(port)
	conn, err := dial(ctx, net.JoinHostPort("127.0.0.1", p))
	if err == nil {
		return conn, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if prohibited(err) {
		return nil, ErrForwardingDisabled
	}
	conn, err6 := dial(ctx, net.JoinHostPort("::1", p))
	if err6 == nil {
		return conn, nil
	}
	if prohibited(err6) {
		return nil, ErrForwardingDisabled
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, fmt.Errorf("%w: %v", ErrPortClosed, err)
}

// prohibited recognises sshd's "administratively prohibited" refusal.
func prohibited(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "administratively prohibited")
}

// ProbePort reports whether a loopback port of h accepts TCP within
// timeout.
func ProbePort(ctx context.Context, h Host, port int, timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	c, err := DialLoopback(ctx, h, port)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Dial opens a TCP connection to addr as machine h sees it: a direct-tcpip channel over
// the existing SSH connection on a server, so that nothing has to be installed there.
func Dial(ctx context.Context, h Host, addr string) (net.Conn, error) {
	switch m := h.(type) {
	case *Local:
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	case *SSH:
		c := m.currentClient()
		if c == nil {
			return nil, ErrNotConnected
		}
		conn, err := c.DialContext(ctx, "tcp", addr)
		if err != nil && prohibited(err) {
			return nil, ErrForwardingDisabled
		}
		return conn, err
	}
	return nil, errors.New("unsupported machine")
}

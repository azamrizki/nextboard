package machine

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"nextboard/internal/connlog"
)

// flakyLink sits between the hub and a server. cut closes everything, like a
// connection reset. stall keeps the sockets open and carries nothing, like a
// pulled cable or a NAT that forgot the connection, where no error ever arrives.
type flakyLink struct {
	t      *testing.T
	port   int
	target string
	mu     sync.Mutex
	ln     net.Listener
	conns  []net.Conn
	frozen chan struct{}
}

func newFlakyLink(t *testing.T, target string) *flakyLink {
	t.Helper()
	l := &flakyLink{t: t, target: target}
	l.up()
	t.Cleanup(l.cut)
	return l
}

func (l *flakyLink) up() {
	l.t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(l.port)))
	if err != nil {
		l.t.Fatal(err)
	}
	l.port = ln.Addr().(*net.TCPAddr).Port
	gate := make(chan struct{})
	close(gate)
	l.mu.Lock()
	l.ln, l.frozen = ln, gate
	l.mu.Unlock()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", l.target)
			if err != nil {
				c.Close()
				continue
			}
			l.mu.Lock()
			l.conns = append(l.conns, c, u)
			l.mu.Unlock()
			go l.pump(u, c)
			go l.pump(c, u)
		}
	}()
}

func (l *flakyLink) pump(dst io.WriteCloser, src io.Reader) {
	defer dst.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if err != nil {
			return
		}
		l.mu.Lock()
		gate := l.frozen
		l.mu.Unlock()
		<-gate // a stalled link holds the bytes back
		if _, err := dst.Write(buf[:n]); err != nil {
			return
		}
	}
}

// stall stops carrying data; the sockets stay open.
func (l *flakyLink) stall() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.frozen = make(chan struct{})
}

func (l *flakyLink) cut() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ln != nil {
		l.ln.Close()
		l.ln = nil
	}
	for _, c := range l.conns {
		c.Close()
	}
	l.conns = nil
}

type eventSink struct {
	mu sync.Mutex
	l  []connlog.Event
}

func (s *eventSink) add(e connlog.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.l = append(s.l, e)
}

func (s *eventSink) of(kind string) []connlog.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []connlog.Event
	for _, e := range s.l {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func waitFor(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// hungCalls are calls started on a link that has gone quiet.
type hungCalls chan struct {
	name string
	err  error
}

func startHung(calls map[string]func() error) hungCalls {
	h := make(hungCalls, len(calls))
	for name, f := range calls {
		go func() {
			h <- struct {
				name string
				err  error
			}{name, f()}
		}()
	}
	return h
}

func (h hungCalls) wait(t *testing.T) map[string]error {
	t.Helper()
	out := map[string]error{}
	for range cap(h) {
		select {
		case r := <-h:
			out[r.name] = r.err
		case <-time.After(20 * time.Second):
			t.Fatalf("calls still hanging on a dead link, returned so far: %v", out)
		}
	}
	return out
}

// addTestSSH adds a server and accepts its host key, as a user would.
func addTestSSH(t *testing.T, r *Registry, port int, key string) *SSH {
	t.Helper()
	m, err := r.Add(AddRequest{Name: "srv", Host: "127.0.0.1", Port: port, AuthMethod: "key", PrivateKey: key, PersistMode: "direct"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var unk *errHostKeyUnknown
	if err := m.Connect(ctx); !errors.As(err, &unk) {
		t.Fatalf("expected an unknown host key, got %v", err)
	}
	if err := m.AcceptHostKey(ctx, m.Info().PendingHostKey.Fingerprint); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestLinkEvents: what the log records for the ways a real network fails.
func TestLinkEvents(t *testing.T) {
	oldEvery, oldStats := keepaliveEvery, linkStatsEvery
	keepaliveEvery, linkStatsEvery = 150*time.Millisecond, 600*time.Millisecond
	defer func() { keepaliveEvery, linkStatsEvery = oldEvery, oldStats }()

	sshd := startSSHD(t, "Subsystem sftp internal-sftp")
	link := newFlakyLink(t, "127.0.0.1:"+sshd.port)
	r := newTestRegistry(t)
	sink := &eventSink{}
	r.Log = sink.add
	m := addTestSSH(t, r, link.port, sshd.userKey)
	defer m.Close()

	// An unknown host key is on record, and connecting after accepting it.
	first := sink.of(connlog.Connected)
	if len(first) != 1 || first[0].Machine != "srv" || first[0].MachineID == "" || first[0].DurationMs < 0 || first[0].DowntimeMs != 0 {
		t.Fatalf("first connect %+v", first)
	}
	if f := sink.of(connlog.ConnectFailed); len(f) == 0 || f[0].Reason != "hostkey_unknown" {
		t.Fatalf("host key %+v", f)
	}

	// A healthy link reports its latency.
	waitFor(t, "link stats", 10*time.Second, func() bool { return len(sink.of(connlog.LinkStats)) > 0 })
	if s := sink.of(connlog.LinkStats)[0]; s.RTTMaxMs < 0 || s.UptimeMs <= 0 {
		t.Fatalf("stats %+v", s)
	}
	m.NoteCall(false)
	m.NoteCall(true)

	// Silence: no error ever arrives, only keepalives stop being answered. Calls
	// caught in it must come back instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := m.ReadFile(ctx, "/etc/hostname", 1<<10); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err) // opens the file channel before the link goes quiet
	}
	link.stall()
	hung := startHung(map[string]func() error{
		"exec": func() error { _, err := m.Exec(ctx, "sleep 60"); return err },
		"read": func() error { _, err := m.ReadFile(ctx, "/etc/hostname", 1<<10); return err },
	})
	waitFor(t, "silence noticed", 20*time.Second, func() bool { return len(sink.of(connlog.Disconnected)) == 1 })
	gone := sink.of(connlog.Disconnected)[0]
	if gone.Reason != connlog.ReasonKeepalive || gone.UptimeMs <= 0 {
		t.Fatalf("after silence %+v", gone)
	}
	calls, failures := gone.Calls, gone.Failures // counted since the last report
	for _, e := range sink.of(connlog.LinkStats) {
		calls, failures = calls+e.Calls, failures+e.Failures
	}
	if calls != 2 || failures != 1 {
		t.Fatalf("calls %d failures %d", calls, failures)
	}
	for name, err := range hung.wait(t) {
		if !LinkLost(err) {
			t.Fatalf("%s during silence should fail as a lost link, got %v", name, err)
		}
	}
	if n := len(sink.of(connlog.KeepaliveMiss)); n != keepaliveMiss {
		t.Fatalf("%d misses, want exactly %d", n, keepaliveMiss)
	}
	link.cut()

	// While it is away: attempts are on record, then the way back with how long it took.
	waitFor(t, "failed attempts", 20*time.Second, func() bool { return len(sink.of(connlog.ConnectFailed)) >= 2 })
	before := len(sink.of(connlog.Connected))
	link.up()
	waitFor(t, "reconnected", 45*time.Second, func() bool { return len(sink.of(connlog.Connected)) > before })
	back := sink.of(connlog.Connected)[before]
	if back.DowntimeMs <= 0 || back.Attempt < 2 {
		t.Fatalf("back %+v", back)
	}

	// A reset: the other way to lose a connection.
	link.cut()
	waitFor(t, "reset noticed", 10*time.Second, func() bool { return len(sink.of(connlog.Disconnected)) == 2 })
	if d := sink.of(connlog.Disconnected)[1]; d.Reason != connlog.ReasonLost {
		t.Fatalf("after reset %+v", d)
	}

	// Stopping it on purpose is not a network problem.
	link.up()
	waitFor(t, "connected again", 45*time.Second, func() bool { return m.Info().Status == StatusConnected })
	m.Close()
	waitFor(t, "closed", 5*time.Second, func() bool { return len(sink.of(connlog.Disconnected)) == 3 })
	if d := sink.of(connlog.Disconnected)[2]; d.Reason != connlog.ReasonClosed {
		t.Fatalf("after close %+v", d)
	}
}

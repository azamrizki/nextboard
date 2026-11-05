package machine

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSSHTmuxPersistence needs a disposable sshd with tmux, e.g.
//
//	NEXTBOARD_TMUX_SSH=127.0.0.1:2299 NEXTBOARD_TMUX_USER=root NEXTBOARD_TMUX_KEY=/path/key
func TestSSHTmuxPersistence(t *testing.T) {
	addr := os.Getenv("NEXTBOARD_TMUX_SSH")
	if addr == "" {
		t.Skip("set NEXTBOARD_TMUX_SSH/USER/KEY untuk uji tmux")
	}
	host, portS, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portS)
	key, err := os.ReadFile(os.Getenv("NEXTBOARD_TMUX_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	reg := newTestRegistry(t)
	m, err := reg.Add(AddRequest{Host: host, Port: port, User: os.Getenv("NEXTBOARD_TMUX_USER"), AuthMethod: "key", PrivateKey: string(key)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_ = m.Connect(ctx)
	if hk := m.Info().PendingHostKey; hk != nil {
		if err := m.AcceptHostKey(ctx, hk.Fingerprint); err != nil {
			t.Fatal(err)
		}
	}
	if !m.Info().Probe.Tmux || m.EffectivePersist() != "tmux" {
		t.Fatalf("tmux not detected: %+v", m.Info().Probe)
	}

	read := func(p Proc) (*syncBuf, chan struct{}) {
		var b syncBuf
		done := make(chan struct{})
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := p.Read(buf)
				b.Write(buf[:n])
				if err != nil {
					close(done)
					return
				}
			}
		}()
		return &b, done
	}
	waitFor := func(b *syncBuf, s string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(b.String(), s) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %q in %q", s, b.String())
	}

	spec := StartSpec{SessionID: "s_tmx", Command: "sh", Args: []string{"-c", "echo FIRST-RUN; while :; do read l; echo got:$l; done"}, Cwd: "~", Cols: 100, Rows: 30, TmuxName: "nb-s_tmx"}
	p, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := read(p)
	waitFor(out, "FIRST-RUN")
	p.Write([]byte("alpha\r"))
	waitFor(out, "got:alpha")

	// Drop the TCP connection: the channel dies, the tmux session lives.
	m.EnsureLoop()
	m.currentClient().Close()
	if _, err := p.Wait(); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("want ErrDisconnected, got %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !m.Connected() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	names, err := m.TmuxSessions(ctx)
	if err != nil || len(names) != 1 || names[0] != "nb-s_tmx" {
		t.Fatalf("tmux sessions %v %v", names, err)
	}

	// Reattach (args differ, as on resume) → same process, no second run.
	spec.Args = []string{"-c", "echo SECOND-RUN; sleep 100"}
	p2, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	out2, done2 := read(p2)
	waitFor(out2, "got:alpha") // tmux repaints the existing screen
	p2.Write([]byte("beta\r"))
	waitFor(out2, "got:beta")
	if strings.Contains(out2.String(), "SECOND-RUN") {
		t.Fatal("tmux session was recreated instead of reattached")
	}

	// Kill in tmux mode = tmux kill-session through exec.
	if err := p2.Signal("HUP"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done2:
	case <-time.After(10 * time.Second):
		t.Fatal("attach client did not exit after kill-session")
	}
	if names, _ := m.TmuxSessions(ctx); len(names) != 0 {
		t.Fatalf("still running: %v", names)
	}
	m.Close()
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) {
	s.mu.Lock()
	s.b.Write(p)
	s.mu.Unlock()
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

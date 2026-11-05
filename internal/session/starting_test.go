package session

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/secret"
	"github.com/azamrizki/nextboard/internal/store"
)

// startingShell is a console in the moment a client can already reach it: its process
// does not exist yet. A plain sh, so that no one's shell configuration is involved.
func startingShell(t *testing.T) *Session {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	box, err := secret.Open(st.Path("secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := machine.OpenRegistry(st, box)
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, reg)
	s := newSession(m, Spec{ID: "s_starting01", Name: "t", Handle: "T", MachineID: machine.LocalID, Agent: "custom-sh", AgentKind: agent.KindShell,
		Command: "/bin/sh", Cols: 100, Rows: 30, Cwd: t.TempDir()})
	t.Cleanup(func() {
		s.launchMu.Lock()
		s.stop()
		s.launchMu.Unlock()
	})
	return s
}

func start(s *Session) {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	s.launch(agent.ModeShell)
}

func screenOf(s *Session) []byte {
	b, sub := s.Subscribe()
	s.Unsubscribe(sub)
	return b
}

func waitFor(t *testing.T, s *Session, want ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		out := screenOf(s)
		all := true
		for _, w := range want {
			all = all && bytes.Contains(out, []byte(w))
		}
		if all {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%q not in %q", want, screenOf(s))
}

// A client opens a console and sends its size and first keys at once. The process may
// not exist yet; none of it may be lost.
func TestStartKeepsWhatClientsSent(t *testing.T) {
	s := startingShell(t)
	s.Resize(90, 25)
	if err := s.Input([]byte("echo nb-$((40+2))-ok; stty size\r")); err != nil {
		t.Fatal(err)
	}
	start(s)
	waitFor(t, s, "nb-42-ok", "25 90")
	if !s.lastInput.After(time.Time{}) {
		t.Fatal("typed input does not count as input")
	}
}

// Keys typed across the start of the process keep their order.
func TestStartKeepsTheOrderOfInput(t *testing.T) {
	s := startingShell(t)
	const lines = 60
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range lines {
			_ = s.Input([]byte("echo L$((" + strconv.Itoa(i) + "+0))\r"))
			time.Sleep(time.Millisecond)
		}
	}()
	start(s)
	<-done
	waitFor(t, s, "L"+strconv.Itoa(lines-1))
	var got []int
	for _, m := range regexp.MustCompile(`L(\d+)`).FindAllSubmatch(screenOf(s), -1) {
		n, _ := strconv.Atoi(string(m[1]))
		got = append(got, n)
	}
	if len(got) != lines {
		t.Fatalf("%d of %d lines arrived: %v", len(got), lines, got)
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("out of order: %v", got)
		}
	}
}

// What was typed during a start that failed must not come back in a later process.
func TestFailedStartForgetsInput(t *testing.T) {
	s := startingShell(t)
	s.spec.Command = "/nonexistent/nextboard-test"
	if err := s.Input([]byte("echo stale-$((1+1))\r")); err != nil {
		t.Fatal(err)
	}
	start(s)
	if s.status != StatusError || len(s.early) != 0 {
		t.Fatalf("status %s, kept %q", s.status, s.early)
	}
	s.spec.Command = "/bin/sh"
	start(s)
	if err := s.Input([]byte("echo fresh-$((1+1))\r")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, s, "fresh-2")
	if bytes.Contains(screenOf(s), []byte("stale-2")) {
		t.Fatalf("an old line ran: %q", screenOf(s))
	}
}

// Only keys are kept, and not without limit; a console that is not starting takes nothing.
func TestEarlyInputLimits(t *testing.T) {
	s := startingShell(t)
	for _, report := range []string{"\x1b[I", "\x1b[?1;2c", "\x1b]11;rgb:0b0b/1010/2020\x07"} {
		if err := s.Input([]byte(report)); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.early) != 0 {
		t.Fatalf("terminal reports kept: %q", s.early)
	}
	if err := s.Input([]byte(strings.Repeat("x", maxEarlyInput+1))); err == nil || len(s.early) != 0 {
		t.Fatalf("too much was kept: %v, %d bytes", err, len(s.early))
	}
	if err := s.Input([]byte("ok")); err != nil || string(s.early) != "ok" {
		t.Fatalf("%v, kept %q", err, s.early)
	}
	s.early, s.status = nil, StatusExited
	if err := s.Input([]byte("late")); err == nil || len(s.early) != 0 {
		t.Fatalf("an exited console took input: %v, kept %q", err, s.early)
	}
}

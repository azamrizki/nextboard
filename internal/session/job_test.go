package session

import (
	"testing"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
)

// waitJob drives the session's ticks until the console says what it runs.
func waitJob(t *testing.T, s *Session, want string, active bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		s.tick(time.Now())
		in := s.m.SessionInfo(s)
		if in.Job == want && in.Active == active && s.jobIsKnown() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	in := s.m.SessionInfo(s)
	t.Fatalf("job %q active %v, want %q %v; output %q", in.Job, in.Active, want, active, screenOf(s))
}

func (s *Session) jobIsKnown() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobKnown
}

// An idle shell is idle; a command makes it active, and its end makes it idle again.
func TestShellIsActiveOnlyWhileACommandRuns(t *testing.T) {
	s := startingShell(t)
	start(s)
	waitFor(t, s, "$ ")
	waitJob(t, s, "", false)
	if err := s.Input([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	waitJob(t, s, "sleep", true)
	if err := s.Input([]byte{3}); err != nil { // Ctrl-C
		t.Fatal(err)
	}
	waitJob(t, s, "", false)
}

// The status frame carries the same answer, so an open terminal sees it change.
func TestStatusFrameSaysWhatRuns(t *testing.T) {
	s := startingShell(t)
	start(s)
	waitFor(t, s, "$ ")
	waitJob(t, s, "", false)
	_, sub := s.Subscribe()
	defer s.Unsubscribe(sub)
	if err := s.Input([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(15 * time.Second)
	for {
		select {
		case m := <-sub.C:
			if m.Status != nil && m.Status.Job == "sleep" && m.Status.Active {
				_ = s.Input([]byte{3})
				return
			}
		case <-time.After(50 * time.Millisecond):
			s.tick(time.Now())
		case <-deadline:
			t.Fatal("no status frame with the command")
		}
	}
}

// Without a pid to ask about, printing is the only sign left.
func TestActiveWithoutAPidToAsk(t *testing.T) {
	s := startingShell(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = StatusRunning
	s.eng.SetProc(StatusRunning, time.Now())
	if st := s.eng.Snapshot().AgentState; st != agentstate.Shell {
		t.Fatalf("state %s", st)
	}
	s.jobKnown, s.busy = false, false
	if s.activeLocked() {
		t.Fatal("quiet and unknown counted as active")
	}
	s.busy = true
	if !s.activeLocked() {
		t.Fatal("printing and unknown did not count")
	}
	s.jobKnown = true // known beats printing: a prompt redraw is no work
	if s.activeLocked() {
		t.Fatal("output at a known prompt counted as active")
	}
	s.job = "npm"
	if !s.activeLocked() || s.jobLocked() != "npm" {
		t.Fatal("a known command did not count")
	}
	s.spec.Paused = true
	if s.activeLocked() {
		t.Fatal("a paused console counted as active")
	}
	s.spec.Paused = false
	s.status = StatusExited
	if s.activeLocked() {
		t.Fatal("an ended console counted as active")
	}
}

// An SSH console asks rarely while nothing happens; a local one has nothing to save.
func TestJobChecksKeepOffAQuietServer(t *testing.T) {
	s := startingShell(t)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobAt = now
	if s.jobDueLocked(now.Add(jobCheckLocal - time.Millisecond)) {
		t.Fatal("local: too soon")
	}
	if !s.jobDueLocked(now.Add(jobCheckLocal)) {
		t.Fatal("local: not due")
	}
	s.spec.MachineID = "m_remote01"
	s.lastOutput, s.lastInput = now.Add(-time.Hour), now.Add(-time.Hour)
	if s.jobDueLocked(now.Add(jobCheckRemote)) {
		t.Fatal("remote: asked a quiet server early")
	}
	if !s.jobDueLocked(now.Add(jobCheckQuiet)) {
		t.Fatal("remote: never asks")
	}
	s.lastOutput = now.Add(time.Second)
	if !s.jobDueLocked(now.Add(jobCheckRemote)) {
		t.Fatal("remote: did not follow up on output")
	}
}

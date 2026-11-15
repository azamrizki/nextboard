package session

import (
	"sync"
	"sync/atomic"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/machine"
)

// Consoles of an archived project (docs/API-board.md §1). Archiving stops them the way sleep
// does for agents: an agent of This PC whose conversation is known ends with its conversation
// kept and is asleep, whatever it was doing; any other console (a shell, an agent on a server
// or started by hand) is stopped. While the project is archived nothing of it starts, relaunches
// or wakes (Manager.Frozen); restored, its agents wake as asleep ones always do.

const archivedNote = "\r\n\x1b[90m── project archived · restore it to use this console again ──\x1b[0m\r\n"

func (m *Manager) frozen(projectID string) bool {
	return projectID != "" && m.Frozen != nil && m.Frozen(projectID)
}

// Freeze ends the processes of the consoles that match, in parallel (each stop may wait for the
// HUP→KILL grace period). It returns how many had one.
func (m *Manager) Freeze(match func(Spec) bool) int {
	var wg sync.WaitGroup
	var n atomic.Int32
	for _, s := range m.all() {
		if !match(s.Spec()) {
			continue
		}
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			if s.freeze() {
				n.Add(1)
			}
		}(s)
	}
	wg.Wait()
	if n.Load() > 0 {
		m.saveNow()
	}
	return int(n.Load())
}

// resumable reports whether s comes back with its conversation when it wakes. Caller holds mu.
func (s *Session) resumableLocked() bool {
	return s.ag.Kind == agent.KindAgent && !s.eng.Snapshot().ManualAgent && s.spec.MachineID == machine.LocalID &&
		validSessionID(s.spec.AgentSessionID)
}

// freeze stops s for its archived project; it reports whether a process was running.
func (s *Session) freeze() bool {
	s.launchMu.Lock()
	s.mu.Lock()
	if s.deleted || s.spec.Asleep || s.sleeping {
		s.mu.Unlock()
		s.launchMu.Unlock()
		return false
	}
	running := s.cur != nil
	if running && s.resumableLocked() {
		draft, _ := s.draft.get() // a draft it cannot follow exactly is not typed back
		s.priv.Draft, s.priv.DraftFailed = draft, false
		s.sleeping = true
		s.mu.Unlock()
		s.stop() // exited() sees sleeping: asleep, and it cannot wake while archived
		s.launchMu.Unlock()
		return true
	}
	idle := !running && (s.status == StatusExited || s.status == StatusError)
	if running || !idle {
		s.writeLocked(archivedNote)
	}
	s.mu.Unlock()
	s.launchMu.Unlock()
	if idle {
		return false
	}
	_, _ = s.m.Kill(s.spec.ID)
	return running
}

// restFrozen leaves a console of an archived project without a process when the hub starts:
// an agent that can resume is asleep, anything else has exited.
func (m *Manager) restFrozen(s *Session) {
	s.mu.Lock()
	if s.spec.Asleep || s.resumableLocked() {
		s.asleepLocked(restartNote)
		s.mu.Unlock()
		s.setStatus(StatusAsleep, 0, "")
		return
	}
	s.writeLocked(archivedNote)
	s.mu.Unlock()
	s.setStatus(StatusExited, 0, "")
}

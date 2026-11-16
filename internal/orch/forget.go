package orch

import "os"

// ForgetProject removes what the orchestration keeps of a project deleted for good
// (docs/API-board.md §1): its loops with their history, its orders and handoffs, its lead's
// thread and the threads of the orchestrator cards on its boards (cards), its memory with the
// agents' notes and sub-memories, and its list of blocked commands. Its folders are not touched.
func (s *Service) ForgetProject(pid string, cards []string) {
	if !idRe.MatchString(pid) {
		return
	}
	s.Loops.DeleteProject(pid)
	s.Orders.DeleteProject(pid, s.Now())
	s.Missions.DeleteProject(pid, s.Now())
	s.mu.Lock()
	gone := 0
	for id, h := range s.handoffs {
		if h.projectID == pid {
			delete(s.handoffs, id)
			gone++
		}
	}
	s.mu.Unlock()
	if gone > 0 {
		s.saveHandoffs()
	}
	for _, tid := range append([]string{pid}, cards...) {
		if ValidThreadID(tid) {
			s.Threads.Clear(tid)
		}
	}
	s.Memory.RemoveProject(pid)
	s.blocked.mu.Lock()
	if _, ok := s.blocked.lists[pid]; ok {
		delete(s.blocked.lists, pid)
		_ = s.blocked.st.Save(blockedFile, s.blocked.lists)
	}
	s.blocked.mu.Unlock()
}

// RemoveProject deletes a project's memory folder (MEMORY.md and the agents' notes), the file
// of an older hub and its sub-memories.
func (m *Memory) RemoveProject(pid string) {
	if !idRe.MatchString(pid) {
		return
	}
	_ = os.RemoveAll(m.st.Path("memory", pid))
	_ = os.Remove(m.st.Path("memory", pid+".md"))
	if owner, err := subOwner(pid); err == nil && owner != globalOwner {
		_ = os.RemoveAll(m.st.Path("memory", "subs", owner))
	}
}

package server

import (
	"log"
	"sync"

	"nextboard/internal/session"
)

// maxMirrorPending bounds what waits for one peer that stopped reading.
const maxMirrorPending = 1 << 20

// inputMirror copies typed input to the other consoles of a linked group
// (docs/API-orchestration.md §5). Every peer has its own ordered queue: a peer
// whose terminal stopped reading must not stall the console being typed in.
type inputMirror struct {
	mu    sync.Mutex
	peers map[string]*mirrorQueue
}

type mirrorQueue struct {
	chunks [][]byte
	size   int
}

func (m *inputMirror) send(id string, peer *session.Session, p []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.peers[id]
	if q == nil {
		if m.peers == nil {
			m.peers = map[string]*mirrorQueue{}
		}
		q = &mirrorQueue{}
		m.peers[id] = q
		go m.drain(id, peer, q)
	}
	if q.size+len(p) > maxMirrorPending {
		log.Printf("input tertaut: console %s tidak membaca input; %d byte dibuang", id, len(p))
		return
	}
	q.chunks = append(q.chunks, p)
	q.size += len(p)
}

// drain writes a peer's queue in order and ends once it is empty.
func (m *inputMirror) drain(id string, peer *session.Session, q *mirrorQueue) {
	for {
		m.mu.Lock()
		if len(q.chunks) == 0 {
			delete(m.peers, id)
			m.mu.Unlock()
			return
		}
		p := q.chunks[0]
		q.chunks = q.chunks[1:]
		q.size -= len(p)
		m.mu.Unlock()
		_ = peer.Input(p)
	}
}

// sessionInput writes keyboard input to a console and, when its group links
// input, to the rest of the group. Orders from the command bar do not pass
// here: the delivery engine types them into each console itself.
func (s *Server) sessionInput(id string, sess *session.Session, p []byte) error {
	if err := sess.Input(p); err != nil {
		return err
	}
	if s.orch == nil {
		return nil
	}
	typed := session.TypedInput(p)
	if len(typed) == 0 {
		return nil
	}
	for _, pid := range s.orch.SyncPeers(id) {
		if peer, ok := s.sessions.Get(pid); ok {
			s.mirror.send(pid, peer, typed)
		}
	}
	return nil
}

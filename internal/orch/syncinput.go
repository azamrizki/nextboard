package orch

import (
	"slices"

	"nextboard/internal/board"
	"nextboard/internal/session"
)

// SyncPeers lists the consoles that also get what is typed in sid: the other
// running, unpaused members of its group when that group links input (§5).
func (s *Service) SyncPeers(sid string) []string {
	in, ok := s.hub.SessionInfo(sid)
	if !ok || in.WorkspaceID == "" {
		return nil
	}
	b, err := s.hub.Board(in.WorkspaceID)
	if err != nil {
		return nil
	}
	sessionOf := map[string]string{}
	self := ""
	for _, n := range b.Nodes {
		if n.Kind == board.KindTerminal {
			sessionOf[n.ID] = n.SessionID()
			if n.SessionID() == sid {
				self = n.ID
			}
		}
	}
	if self == "" {
		return nil
	}
	for _, n := range b.Nodes {
		if n.Kind != KindGroup {
			continue
		}
		g := GroupOf(n)
		if !g.SyncInput || !slices.Contains(g.Members, self) {
			continue
		}
		var peers []string
		for _, m := range g.Members {
			peer := sessionOf[m]
			if peer == "" || peer == sid {
				continue
			}
			if p, ok := s.hub.SessionInfo(peer); ok && p.Status == session.StatusRunning && !p.Paused {
				peers = append(peers, peer)
			}
		}
		return peers
	}
	return nil
}

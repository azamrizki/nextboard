package orch

import "github.com/azamrizki/nextboard/internal/delivery"

// SourceChatContext marks what a chat that resumes is told changed since it last ran
// (docs/API-agents.md §29): context, not work.
const SourceChatContext = "context"

// TellContext types context into one agent console: no order, no task card, and the turn that
// answers it is no result to report or forward (contextTurn), like memory or a tile.
func (s *Service) TellContext(sid, text string) (delivery.Delivery, error) {
	d, err := s.hub.Deliver(sid, delivery.Request{Text: text, Source: SourceChatContext})
	if err != nil {
		return d, err
	}
	s.mu.Lock()
	s.turnContext[sid] = append(s.turnContext[sid], d.ID)
	s.mu.Unlock()
	return d, nil
}

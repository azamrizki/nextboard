package orch

import (
	"strings"
	"time"

	"nextboard/internal/agentstate"
	"nextboard/internal/auth"
	"nextboard/internal/delivery"
	"nextboard/internal/session"
)

// Order tracking (docs/API-orchestration.md §4): delivery changes move
// targets to delivered/failed; agent turns move them to working,
// needsYou and finished; a finished target gets a report message with
// the agent's reply.

const (
	turnSlack = 2 * time.Second
	// replyLateTries: seconds a report keeps waiting for a reply that is slow to come in.
	replyLateTries = 60
)

// OnDelivery follows a delivery change (engine callback).
func (s *Service) OnDelivery(d delivery.Delivery) {
	if d.OrderID == "" {
		return
	}
	now := s.Now()
	o, changed := s.Orders.Mutate(d.OrderID, func(o *Order) bool {
		for i := range o.Targets {
			t := &o.Targets[i]
			if t.DeliveryID != d.ID || TargetFinal(t.State) {
				continue
			}
			fin := now.UTC()
			switch d.State {
			case delivery.Sent, delivery.Confirmed:
				if t.State == TQueued {
					t.State = TDelivered
					t.SentAt = d.SentAt
					t.StartedAt = d.SentAt
					if d.State == delivery.Confirmed && d.ConfirmedAt != nil {
						t.State = TWorking
					}
					return true
				}
				if t.State == TDelivered && d.State == delivery.Confirmed {
					t.State = TWorking
					return true
				}
			case delivery.Failed, delivery.Expired:
				t.State, t.Error, t.FinishedAt = TFailed, deliveryFailure(d), &fin
				return true
			case delivery.Cancelled:
				t.State, t.FinishedAt = TCancelled, &fin
				return true
			}
		}
		return false
	})
	if !changed {
		return
	}
	if d.State == delivery.Sent || d.State == delivery.Confirmed {
		s.mu.Lock()
		s.turnDepth[d.SessionID] = d.ForwardDepth
		s.turnOrder[d.SessionID] = d.OrderID
		s.mu.Unlock()
	}
	s.orderChanged(o)
	for _, t := range o.Targets {
		if t.DeliveryID == d.ID && t.State == TFailed {
			s.Threads.Add(ThreadMessage{ThreadID: o.ThreadID, Role: "hub", Kind: "report", OrderID: o.ID, SessionID: t.SessionID,
				Handle: t.Handle, Text: "@" + t.Handle + " did not get the order: " + t.Error, Report: &Report{State: TFailed}})
		}
	}
}

func (s *Service) orderChanged(o Order) {
	s.hub.Publish(map[string]any{"type": "order", "order": o})
	s.Orders.Save(s.Now())
	s.forwardOrderChanged(o)
	s.syncTasks(o)
}

// OnAgentEvent follows engine events of a console (attention callback).
func (s *Service) OnAgentEvent(in session.Info, ev agentstate.Event) {
	now := s.Now()
	switch ev.Kind {
	case agentstate.EvFinished:
		s.turnFinished(in, ev.Turn, now)
	case agentstate.EvNeedsYou:
		s.setTargets(in.ID, func(t *OrderTarget) bool {
			if t.State == TDelivered || t.State == TWorking {
				t.State = TNeedsYou
				return true
			}
			return false
		})
	case agentstate.EvAgentExit, agentstate.EvError:
		s.setTargets(in.ID, func(t *OrderTarget) bool {
			if t.State == TQueued {
				return false // the delivery reports it
			}
			fin := now.UTC()
			t.State, t.Error, t.FinishedAt = TFailed, "agent exited", &fin
			return true
		})
	}
}

// SessionChanged follows session updates (working observed, gone).
func (s *Service) SessionChanged(in session.Info) {
	switch in.AgentState {
	case agentstate.Working:
		s.setTargets(in.ID, func(t *OrderTarget) bool {
			if (t.State == TDelivered || t.State == TNeedsYou) && t.SentAt != nil {
				t.State = TWorking
				return true
			}
			return false
		})
	case agentstate.Exited, agentstate.Error:
		now := s.Now().UTC()
		s.setTargets(in.ID, func(t *OrderTarget) bool {
			if t.State == TQueued {
				return false
			}
			t.State, t.Error, t.FinishedAt = TFailed, "console "+in.AgentState, &now
			return true
		})
	case agentstate.Ready:
		s.autoPush(in)
	}
}

// SessionRemoved fails targets of a deleted console.
func (s *Service) SessionRemoved(id string) {
	s.mu.Lock()
	delete(s.briefed, id)
	delete(s.turnContext, id)
	s.mu.Unlock()
	now := s.Now().UTC()
	s.setTargets(id, func(t *OrderTarget) bool {
		t.State, t.Error, t.FinishedAt = TFailed, "console deleted", &now
		return true
	})
}

// setTargets applies f to every non-final target of a session.
func (s *Service) setTargets(sid string, f func(t *OrderTarget) bool) {
	for _, o := range s.Orders.List(func(o Order) bool { return o.Active() }) {
		x, changed := s.Orders.Mutate(o.ID, func(o *Order) bool {
			ch := false
			for i := range o.Targets {
				if o.Targets[i].SessionID == sid && !TargetFinal(o.Targets[i].State) && f(&o.Targets[i]) {
					ch = true
				}
			}
			return ch
		})
		if changed {
			s.orderChanged(x)
		}
	}
}

// turnFinished closes targets whose turn ended (working → ready after the
// delivery) and posts their reports.
func (s *Service) turnFinished(in session.Info, turn agentstate.LastTurn, now time.Time) {
	var done []struct {
		o Order
		t OrderTarget
	}
	for _, o := range s.Orders.List(func(o Order) bool { return o.Active() }) {
		x, changed := s.Orders.Mutate(o.ID, func(o *Order) bool {
			ch := false
			for i := range o.Targets {
				t := &o.Targets[i]
				if t.SessionID != in.ID || TargetFinal(t.State) || t.State == TQueued || t.SentAt == nil {
					continue
				}
				if turn.EndedAt != nil && turn.EndedAt.Before(t.SentAt.Add(-turnSlack)) {
					continue // an older turn
				}
				fin := now.UTC()
				t.State, t.FinishedAt = TFinished, &fin
				ch = true
			}
			return ch
		})
		if !changed {
			continue
		}
		s.orderChanged(x)
		for _, t := range x.Targets {
			if t.SessionID == in.ID && t.State == TFinished && t.FinishedAt != nil && !t.FinishedAt.Before(now.UTC()) {
				done = append(done, struct {
					o Order
					t OrderTarget
				}{x, t})
			}
		}
	}
	prevReply := ""
	if r, ok := s.hub.Reply(in.ID); ok {
		prevReply = r.MessageID + r.At.String() + itoa(r.Chars)
	}
	for _, d := range done {
		go s.report(d.o, d.t, turn, prevReply)
	}
	// The next turn starts at depth 0 unless a delivery says otherwise.
	s.mu.Lock()
	depth, orderID := s.turnDepth[in.ID], s.turnOrder[in.ID]
	delete(s.turnDepth, in.ID)
	delete(s.turnOrder, in.ID)
	s.mu.Unlock()
	// Context is not work: an agent's answer to it is no result to pass on.
	if s.contextTurn(in.ID, turn) && orderID == "" {
		s.Loops.TurnFinished(in.ID, now)
		return
	}
	// The hub refreshes the reply asynchronously after the turn ends; a
	// forward must carry this turn's reply, not the previous one.
	// Past the depth limit nothing is sent, so there is nothing to wait for.
	if depth >= MaxForwardDepth || s.replyFresh(in.ID, turn, prevReply) {
		s.edgeForwards(in, now, depth, orderID)
	} else {
		go func() {
			s.waitFreshReply(in.ID, turn, prevReply)
			s.edgeForwards(in, now, depth, orderID)
		}()
	}
	s.Loops.TurnFinished(in.ID, now)
}

// replyFresh reports whether the cached reply of sid was refreshed since
// prev (its signature at turn end) or already belongs to turn.
func (s *Service) replyFresh(sid string, turn agentstate.LastTurn, prev string) bool {
	r, ok := s.hub.Reply(sid)
	return ok && (r.MessageID+r.At.String()+itoa(r.Chars) != prev || !r.At.Before(turn.StartedAt))
}

// waitFreshReply waits up to ~6 s for the reply of the turn that just
// ended; after that callers use whatever reply exists.
func (s *Service) waitFreshReply(sid string, turn agentstate.LastTurn, prev string) {
	for i := 0; i < 30 && !s.replyFresh(sid, turn, prev); i++ {
		time.Sleep(200 * time.Millisecond)
	}
}

// report waits briefly for the refreshed reply, then posts it (§4). The message keeps
// its own copy: the console's last reply changes with its next turn. A reply that is
// not there yet fills the message when it comes, it is never the previous turn's.
func (s *Service) report(o Order, t OrderTarget, turn agentstate.LastTurn, prev string) {
	s.waitFreshReply(t.SessionID, turn, prev)
	id := auth.RandomID("tm_")
	if s.replyFresh(t.SessionID, turn, prev) {
		s.Threads.Add(s.reportMessage(id, o, t, turn, true))
		return
	}
	s.Threads.Add(s.reportMessage(id, o, t, turn, false))
	for range replyLateTries {
		select {
		case <-s.stop:
			return
		case <-time.After(time.Second):
		}
		if s.replyFresh(t.SessionID, turn, prev) {
			s.Threads.Update(s.reportMessage(id, o, t, turn, true))
			return
		}
	}
}

// reportMessage builds a report and keeps the reply it stands for. Without fresh, the
// console's reply is still that of an earlier turn and stays out of it.
func (s *Service) reportMessage(id string, o Order, t OrderTarget, turn agentstate.LastTurn, fresh bool) ThreadMessage {
	var text string
	if r, ok := s.hub.Reply(t.SessionID); ok && fresh {
		text = r.Text
		if strings.TrimSpace(text) != "" {
			s.Threads.SaveOutput(ReplyOutput{MessageID: id, ThreadID: o.ThreadID, ProjectID: o.ProjectID, OrderID: o.ID, SessionID: t.SessionID, Handle: t.Handle,
				Source: r.Source, Text: text, At: r.At, Chars: r.Chars, Truncated: r.Truncated})
		}
	}
	rep := &Report{State: TFinished, DurationMs: turn.DurationMs, Chars: len([]rune(text)), Excerpt: clipText(text, maxExcerpt)}
	body := text
	if body == "" {
		body = "@" + t.Handle + " finished (no reply text)."
	}
	return ThreadMessage{ID: id, ThreadID: o.ThreadID, Role: "agent", Kind: "report", OrderID: o.ID, SessionID: t.SessionID,
		Handle: t.Handle, Text: body, Report: rep}
}

// ordersTick applies the report timeout (settings.commands.reportTimeoutMin).
func (s *Service) ordersTick(now time.Time) {
	lim := time.Duration(s.hub.Settings().Commands.ReportTimeoutMin) * time.Minute
	for _, o := range s.Orders.List(func(o Order) bool { return o.Active() }) {
		if now.Sub(o.CreatedAt) < lim {
			continue
		}
		var timedOut []OrderTarget
		x, changed := s.Orders.Mutate(o.ID, func(o *Order) bool {
			ch := false
			for i := range o.Targets {
				t := &o.Targets[i]
				if TargetFinal(t.State) || t.State == TQueued {
					continue
				}
				fin := now.UTC()
				t.State, t.FinishedAt = TTimeout, &fin
				timedOut = append(timedOut, *t)
				ch = true
			}
			return ch
		})
		if !changed {
			continue
		}
		s.orderChanged(x)
		for _, t := range timedOut {
			s.Threads.Add(ThreadMessage{ThreadID: x.ThreadID, Role: "hub", Kind: "report", OrderID: x.ID, SessionID: t.SessionID, Handle: t.Handle,
				Text: "No reply from @" + t.Handle + " after " + itoa(int(lim/time.Minute)) + " min.", Report: &Report{State: TTimeout}})
		}
	}
}

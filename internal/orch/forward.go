package orch

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/agent"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/delivery"
	"github.com/azamrizki/nextboard/internal/session"
)

// Forwards (docs/API-orchestration.md §10) pass replies between agents:
// persistent forward edges on the board and one-shot forwards created by
// a command. Loop protection: forward edges cannot form cycles, a
// forwarded turn carries forwardDepth (≤ 3), an edge fires at most once
// per 60 s (what ends inside that minute waits for it, nothing is dropped),
// one-shot forwards fire once and expire.

const (
	forwardsFile      = "forwards.json"
	edgeQueueFile     = "forwards-queued.json"
	maxReplyForward   = 20000
	maxDigest         = delivery.MaxText
	maxEdgeQueued     = 12 // results one edge may hold for its next send
	forwardFreshFor   = 30 * time.Minute
	forwardWaitOrder  = 4 * time.Hour
	forwardAfterOrder = 30 * time.Minute
	forwardEdgeEvery  = 60 * time.Second
	MaxForwardDepth   = 3
	truncMark         = "\n[… truncated by NextBoard …]"
)

var ErrForwardCycle = errors.New("forward tidak boleh meneruskan ke sumbernya sendiri")

// Forward is a one-shot forward.
type Forward struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	ThreadID  string    `json:"threadId"`
	Sources   []string  `json:"sources"`
	Targets   []string  `json:"targets"`
	Mode      string    `json:"mode"`
	OrderID   string    `json:"orderId,omitempty"`
	State     string    `json:"state"` // waiting | collecting | sent | expired | cancelled
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	// Done lists sources whose order target finished (or failed).
	Done []string `json:"done,omitempty"`
}

// edgeQueued is what a forward edge still owes its targets: results of turns
// that ended inside its minute. They go out together when the minute is up.
type edgeQueued struct {
	EdgeID      string      `json:"edgeId"`
	WorkspaceID string      `json:"workspaceId"`
	ProjectID   string      `json:"projectId"`
	Sources     []string    `json:"sources"`
	Parts       []ReplyPart `json:"parts"`
	Depth       int         `json:"depth"`
	Due         time.Time   `json:"due"`
}

func (s *Service) loadForwards() {
	var list []*Forward
	if _, err := s.st.Load(forwardsFile, &list); err == nil {
		for _, f := range list {
			if f != nil && f.ID != "" {
				s.forwards[f.ID] = f
			}
		}
	}
	var queued []*edgeQueued
	if _, err := s.st.Load(edgeQueueFile, &queued); err == nil {
		for _, q := range queued {
			if q != nil && q.EdgeID != "" {
				s.edgeQueue[q.EdgeID] = q
			}
		}
	}
}

func (s *Service) saveEdgeQueue() {
	s.mu.Lock()
	list := []*edgeQueued{}
	for _, q := range s.edgeQueue {
		c := *q
		list = append(list, &c)
	}
	s.mu.Unlock()
	_ = s.st.Save(edgeQueueFile, list)
}

func (s *Service) saveForwards() {
	s.mu.Lock()
	list := []*Forward{}
	for _, f := range s.forwards {
		if f.State == "waiting" || f.State == "collecting" {
			c := *f
			list = append(list, &c)
		}
	}
	s.mu.Unlock()
	_ = s.st.Save(forwardsFile, list)
}

func (s *Service) publishForward(f Forward) {
	s.hub.Publish(map[string]any{"type": "forward", "forward": f})
}

// Forwards lists forwards of a project.
func (s *Service) Forwards(projectID string) []Forward {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Forward{}
	for _, f := range s.forwards {
		if projectID == "" || f.ProjectID == projectID {
			out = append(out, *f)
		}
	}
	slices.SortFunc(out, func(a, b Forward) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// CancelForward is DELETE /api/forwards/{id}.
func (s *Service) CancelForward(id string) error {
	s.mu.Lock()
	f, ok := s.forwards[id]
	if !ok {
		s.mu.Unlock()
		return errors.New("forward tidak ditemukan")
	}
	if f.State == "waiting" || f.State == "collecting" {
		f.State = "cancelled"
	}
	c := *f
	s.mu.Unlock()
	s.saveForwards()
	s.publishForward(c)
	return nil
}

// CreateForward registers "when X finishes, pass it to Y".
func (s *Service) CreateForward(c *execCtx, sources, targets []string, mode string) (Forward, error) {
	for _, a := range sources {
		if slices.Contains(targets, a) {
			return Forward{}, ErrForwardCycle
		}
	}
	now := s.Now()
	f := &Forward{ID: auth.RandomID("fw_"), ProjectID: c.projectID, ThreadID: c.threadID, Sources: uniq(sources), Targets: uniq(targets),
		Mode: mode, State: "waiting", CreatedAt: now.UTC(), ExpiresAt: now.Add(forwardWaitOrder).UTC()}
	// Attach to the sources' active order, if any.
	for _, o := range s.Orders.List(func(o Order) bool { return o.Active() }) {
		if orderTargets(o, f.Sources) {
			f.OrderID, f.State = o.ID, "collecting"
			f.ExpiresAt = o.CreatedAt.Add(forwardAfterOrder).UTC()
			if f.ExpiresAt.Before(now) {
				f.ExpiresAt = now.Add(forwardAfterOrder).UTC()
			}
			for _, t := range o.Targets {
				if slices.Contains(f.Sources, t.SessionID) && TargetFinal(t.State) {
					f.Done = append(f.Done, t.SessionID)
				}
			}
			break
		}
	}
	s.mu.Lock()
	s.forwards[f.ID] = f
	cp := *f
	s.mu.Unlock()
	s.saveForwards()
	s.publishForward(cp)
	s.checkForward(cp.ID, now)
	return cp, nil
}

func orderTargets(o Order, sids []string) bool {
	for _, t := range o.Targets {
		if slices.Contains(sids, t.SessionID) {
			return true
		}
	}
	return false
}

// attachForwards gives waiting forwards the new order of their sources.
func (s *Service) attachForwards(o Order) {
	var changed []Forward
	s.mu.Lock()
	for _, f := range s.forwards {
		if f.State == "waiting" && f.ProjectID == o.ProjectID && orderTargets(o, f.Sources) {
			f.OrderID, f.State = o.ID, "collecting"
			f.ExpiresAt = o.CreatedAt.Add(forwardAfterOrder).UTC()
			changed = append(changed, *f)
		}
	}
	s.mu.Unlock()
	if len(changed) > 0 {
		s.saveForwards()
	}
	for _, f := range changed {
		s.publishForward(f)
	}
}

// forwardOrderChanged records finished sources of collecting forwards.
func (s *Service) forwardOrderChanged(o Order) {
	var check []string
	s.mu.Lock()
	for _, f := range s.forwards {
		if f.State != "collecting" || f.OrderID != o.ID {
			continue
		}
		for _, t := range o.Targets {
			if slices.Contains(f.Sources, t.SessionID) && TargetFinal(t.State) && !slices.Contains(f.Done, t.SessionID) {
				f.Done = append(f.Done, t.SessionID)
			}
		}
		check = append(check, f.ID)
	}
	s.mu.Unlock()
	for _, id := range check {
		s.checkForward(id, s.Now())
	}
}

// checkForward sends a collecting forward once every source is done, or
// what exists when its time is up.
func (s *Service) checkForward(id string, now time.Time) {
	s.mu.Lock()
	f, ok := s.forwards[id]
	if !ok || f.State != "collecting" {
		s.mu.Unlock()
		return
	}
	all := true
	for _, src := range f.Sources {
		if !slices.Contains(f.Done, src) {
			all = false
		}
	}
	late := !now.Before(f.ExpiresAt)
	if !all && !late {
		s.mu.Unlock()
		return
	}
	f.State = "sent"
	cp := *f
	s.mu.Unlock()
	s.saveForwards()
	s.publishForward(cp)
	depth := 0
	for _, src := range cp.Sources {
		depth = max(depth, s.depthOf(src))
	}
	c := &execCtx{threadID: cp.ThreadID, projectID: cp.ProjectID, commandID: "fwd_" + cp.ID}
	s.sendDigest(c, cp.Sources, cp.Targets, cp.Mode, depth+1, "")
}

func (s *Service) depthOf(sid string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.turnDepth[sid]
}

// forwardsTick expires forwards that never got an order (4 h) and sends
// collecting ones whose 30 minutes are up.
func (s *Service) forwardsTick(now time.Time) {
	var expired []Forward
	var check []string
	s.mu.Lock()
	for _, f := range s.forwards {
		switch {
		case f.State == "waiting" && !now.Before(f.ExpiresAt):
			f.State = "expired"
			expired = append(expired, *f)
		case f.State == "collecting" && !now.Before(f.ExpiresAt):
			check = append(check, f.ID)
		}
		if (f.State == "sent" || f.State == "expired" || f.State == "cancelled") && now.Sub(f.CreatedAt) > 24*time.Hour {
			delete(s.forwards, f.ID)
		}
	}
	s.mu.Unlock()
	for _, f := range expired {
		s.publishForward(f)
		s.Threads.Add(ThreadMessage{ThreadID: f.ThreadID, Role: "hub", Kind: "note", Text: "Forward expired: no order reached " + s.handlesOf(f.Sources) + " within 4 hours."})
	}
	if len(expired) > 0 {
		s.saveForwards()
	}
	for _, id := range check {
		s.checkForward(id, now)
	}
	s.edgeQueueTick(now)
}

// edgeQueueTick sends what edges held back, once their minute is up.
func (s *Service) edgeQueueTick(now time.Time) {
	var due []*edgeQueued
	s.mu.Lock()
	for id, q := range s.edgeQueue {
		if !now.Before(q.Due) {
			due = append(due, q)
			delete(s.edgeQueue, id)
			s.edgeFired[id] = now
		}
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return
	}
	s.saveEdgeQueue()
	for _, q := range due {
		s.sendQueued(q)
	}
}

// sendQueued looks the edge up again: it may have been removed, or its group may
// have changed, during the wait.
func (s *Service) sendQueued(q *edgeQueued) {
	c := &execCtx{threadID: q.ProjectID, projectID: q.ProjectID, commandID: "fwe_" + q.EdgeID}
	b, err := s.hub.Board(q.WorkspaceID)
	if err != nil {
		s.note(c, "Forward not sent: its board is gone.")
		return
	}
	for _, e := range b.Edges {
		if e.ID != q.EdgeID || e.Kind != EdgeForward {
			continue
		}
		var targets []string
		for _, sid := range nodeSessionIDs(b, e.Target) {
			if !slices.Contains(q.Sources, sid) {
				targets = append(targets, sid)
			}
		}
		s.sendParts(c, q.Sources, q.Parts, targets, ForwardOf(e).Mode, q.Depth)
		return
	}
	s.note(c, "Forward from "+s.handlesOf(q.Sources)+" not sent: its line was removed.")
}

// queueEdge holds a turn's result for the edge's next send.
func (s *Service) queueEdge(c *execCtx, e *board.Edge, in session.Info, sources []string, depth int, due time.Time) {
	if depth > MaxForwardDepth {
		s.note(c, fmt.Sprintf("Forward from %s not sent: forward depth limit (%d) reached.", s.handlesOf(sources), MaxForwardDepth))
		return
	}
	parts := s.replyParts(sources, s.Now())
	if len(parts) == 0 {
		return
	}
	dropped := 0
	s.mu.Lock()
	q := s.edgeQueue[e.ID]
	if q == nil {
		q = &edgeQueued{EdgeID: e.ID, WorkspaceID: in.WorkspaceID, ProjectID: in.ProjectID, Due: due}
		s.edgeQueue[e.ID] = q
	}
	q.Depth = max(q.Depth, depth)
	q.Sources = uniq(append(q.Sources, sources...))
	for _, p := range parts {
		if !slices.Contains(q.Parts, p) {
			q.Parts = append(q.Parts, p)
		}
	}
	if n := len(q.Parts) - maxEdgeQueued; n > 0 {
		q.Parts, dropped = q.Parts[n:], n
	}
	wait := q.Due.Sub(s.Now())
	s.mu.Unlock()
	s.saveEdgeQueue()
	s.note(c, fmt.Sprintf("Forward from @%s waits %d s: an edge forwards at most once per minute.", in.Handle, max(int(wait.Seconds()), 1)))
	if dropped > 0 {
		s.note(c, fmt.Sprintf("Forward from @%s dropped %d older result(s): more than %d were waiting.", in.Handle, dropped, maxEdgeQueued))
	}
}

// edgeForwards fires forward edges whose source just finished a turn.
// depth and orderID describe the delivery that started that turn.
func (s *Service) edgeForwards(in session.Info, now time.Time, depth int, orderID string) {
	if in.WorkspaceID == "" {
		return
	}
	b, err := s.hub.Board(in.WorkspaceID)
	if err != nil {
		return
	}
	term := ""
	for _, n := range b.Nodes {
		if n.Kind == board.KindTerminal && n.SessionID() == in.ID {
			term = n.ID
		}
	}
	if term == "" {
		return
	}
	for _, e := range b.Edges {
		if e.Kind != EdgeForward {
			continue
		}
		var src *board.Node
		for _, n := range b.Nodes {
			if n.ID == e.Source {
				src = n
			}
		}
		if src == nil || (src.ID != term && !(src.Kind == KindGroup && slices.Contains(GroupOf(src).Members, term))) {
			continue
		}
		fd := ForwardOf(e)
		if fd.Trigger == "order" && orderID == "" {
			continue // a turn the user started by hand
		}
		sources := []string{in.ID}
		if src.Kind == KindGroup && orderID != "" {
			// A group waits for every member that got the same order.
			o, ok := s.Orders.Get(orderID)
			if ok {
				var members []string
				for _, m := range GroupOf(src).Members {
					for _, n := range b.Nodes {
						if n.ID == m {
							members = append(members, n.SessionID())
						}
					}
				}
				sources = nil
				for _, t := range o.Targets {
					if slices.Contains(members, t.SessionID) {
						if !TargetFinal(t.State) {
							sources = nil
							break
						}
						sources = append(sources, t.SessionID)
					}
				}
				if len(sources) == 0 {
					continue // other members still work
				}
			}
		}
		var targets []string
		for _, sid := range nodeSessionIDs(b, e.Target) {
			if !slices.Contains(sources, sid) {
				targets = append(targets, sid)
			}
		}
		if len(targets) == 0 {
			continue
		}
		// each edge has its own minute: a console may forward to several places at once
		s.mu.Lock()
		last := s.edgeFired[e.ID]
		_, waiting := s.edgeQueue[e.ID]
		fire := !waiting && now.Sub(last) >= forwardEdgeEvery
		if fire {
			s.edgeFired[e.ID] = now
		}
		s.mu.Unlock()
		c := &execCtx{threadID: in.ProjectID, projectID: in.ProjectID, commandID: "fwe_" + e.ID}
		if !fire {
			s.queueEdge(c, e, in, sources, depth+1, last.Add(forwardEdgeEvery))
			continue
		}
		s.sendDigest(c, sources, targets, fd.Mode, depth+1, e.ID)
	}
}

func nodeSessionIDs(b board.Board, nodeID string) []string {
	for _, n := range b.Nodes {
		if n.ID != nodeID {
			continue
		}
		if n.Kind == board.KindTerminal {
			return []string{n.SessionID()}
		}
		if n.Kind == KindGroup {
			var out []string
			for _, m := range GroupOf(n).Members {
				for _, t := range b.Nodes {
					if t.ID == m {
						out = append(out, t.SessionID())
					}
				}
			}
			return out
		}
	}
	return nil
}

// ReplyPart is one source of a digest.
type ReplyPart struct {
	Handle string `json:"handle"`
	Agent  string `json:"agent,omitempty"`
	Text   string `json:"text"`
}

// Digest builds the forwarded text (§10): labelled per source, ≤ 20 000
// characters per reply and no more than a delivery can carry in total,
// truncated with a marker.
func Digest(parts []ReplyPart) string {
	var names []string
	for _, p := range parts {
		n := "@" + p.Handle
		if p.Agent != "" {
			n += " (" + p.Agent + ")"
		}
		names = append(names, n)
	}
	var b strings.Builder
	b.WriteString("Results forwarded by NextBoard from " + joinAnd(names) + ":\n")
	foot := "\n\nUse these results for your next step."
	budget := maxDigest - len([]rune(foot))
	used := len([]rune(b.String()))
	for _, p := range parts {
		text := truncRunes(strings.TrimSpace(p.Text), maxReplyForward)
		sec := "\n## @" + p.Handle + "\n" + text + "\n"
		if used+len([]rune(sec)) > budget {
			room := budget - used - len([]rune(truncMark)) - 1
			if room > 0 {
				sec = string([]rune(sec)[:room]) + truncMark + "\n"
			} else {
				sec = ""
			}
		}
		b.WriteString(sec)
		used += len([]rune(sec))
		if used >= budget {
			break
		}
	}
	return strings.TrimRight(b.String(), "\n") + foot
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-len([]rune(truncMark))]) + truncMark
}

// sendDigest forwards the current replies of sources to targets. Replies
// older than 30 minutes are not forwarded; depth > 3 is never sent.
func (s *Service) sendDigest(c *execCtx, sources, targets []string, mode string, depth int, edgeID string) {
	if depth > MaxForwardDepth {
		s.note(c, fmt.Sprintf("Forward from %s not sent: forward depth limit (%d) reached.", s.handlesOf(sources), MaxForwardDepth))
		return
	}
	parts := s.replyParts(sources, s.Now())
	if len(parts) == 0 {
		s.note(c, "Nothing to forward from "+s.handlesOf(sources)+" (no recent reply).")
		return
	}
	s.sendParts(c, sources, parts, targets, mode, depth)
}

// replyParts are the current replies of sources, without those too old to forward.
func (s *Service) replyParts(sources []string, now time.Time) []ReplyPart {
	var parts []ReplyPart
	for _, sid := range sources {
		in, ok := s.hub.SessionInfo(sid)
		if !ok {
			continue
		}
		r, ok := s.hub.Reply(sid)
		if !ok || strings.TrimSpace(r.Text) == "" {
			continue
		}
		if in.LastTurn != nil && in.LastTurn.EndedAt != nil && now.Sub(*in.LastTurn.EndedAt) > forwardFreshFor {
			continue
		}
		parts = append(parts, ReplyPart{Handle: in.Handle, Agent: agentName(s.hub.Agents(), in.Agent), Text: r.Text})
	}
	return parts
}

// sendParts delivers a digest of parts to the agents among targets.
func (s *Service) sendParts(c *execCtx, sources []string, parts []ReplyPart, targets []string, mode string, depth int) {
	text := Digest(parts)
	var send []string
	for _, sid := range targets {
		if in, ok := s.hub.SessionInfo(sid); ok && isAgent(in) {
			send = append(send, sid)
		}
	}
	if len(send) == 0 {
		s.note(c, "Forward not sent: no agent among the targets.")
		return
	}
	if mode == "split" && len(send) > 1 {
		// Without the planner the whole digest goes to the first target.
		send = send[:1]
	}
	o := s.sendOrder(c, send, text, "forward", depth)
	_ = o
	s.note(c, "Forwarded "+s.handlesOf(sources)+"'s result to "+s.handlesOf(send)+".")
}

func agentName(l []agent.Agent, id string) string {
	if a, ok := agentByID(l, id); ok {
		return a.Name
	}
	return id
}

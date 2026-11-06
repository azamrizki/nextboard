package server

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/agentstate"
	"github.com/azamrizki/nextboard/internal/auth"
	"github.com/azamrizki/nextboard/internal/session"
)

// attentionRingSize is how many attention events are kept (§12, §15).
const attentionRingSize = 200

// Attention is one notification-worthy event (docs/API-agents.md §12).
type Attention struct {
	ID          string    `json:"id"`
	Seq         int64     `json:"seq"`
	Kind        string    `json:"kind"` // needsYou | finished | error | loopHeld | loopEnded | uptimeDown | uptimeUp | certExpiring | credential
	SessionID   string    `json:"sessionId"`
	MonitorID   string    `json:"monitorId,omitempty"` // uptime alerts have no console
	Handle      string    `json:"handle"`
	Agent       string    `json:"agent"`
	MachineName string    `json:"machineName"`
	ProjectID   string    `json:"projectId"`
	WorkspaceID string    `json:"workspaceId"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	At          time.Time `json:"at"`
}

// attentionRing keeps the last events in memory; bootId changes with
// every hub start so clients drop a stale `since`.
type attentionRing struct {
	mu     sync.Mutex
	bootID string
	seq    int64
	items  []Attention
}

func newAttentionRing() *attentionRing { return &attentionRing{bootID: auth.RandomID("b_")} }

func (r *attentionRing) add(a Attention) Attention {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	a.ID, a.Seq = auth.RandomID("at_"), r.seq
	if a.At.IsZero() {
		a.At = time.Now().UTC()
	}
	r.items = append(r.items, a)
	if len(r.items) > attentionRingSize {
		r.items = r.items[len(r.items)-attentionRingSize:]
	}
	return a
}

func (r *attentionRing) since(seq int64, limit int) ([]Attention, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Attention{}
	for _, a := range r.items {
		if a.Seq > seq {
			out = append(out, a)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, r.seq
}

// onAttention turns engine events into attention items, filtered by
// settings.notifications. Dedupe comes from the engine: needsYou is only
// emitted on entering needsYou, finished once per turn, error once per
// occurrence.
func (s *Server) onAttention(in session.Info, ev agentstate.Event) {
	n := s.settings.Get().Notifications
	a := Attention{SessionID: in.ID, Handle: in.Handle, Agent: in.Agent, MachineName: in.MachineName,
		ProjectID: in.ProjectID, WorkspaceID: in.WorkspaceID}
	who := in.Handle
	if who == "" {
		who = in.Name
	}
	switch ev.Kind {
	case agentstate.EvNeedsYou:
		if !n.NeedsYou {
			return
		}
		a.Kind, a.Title, a.Body = "needsYou", who+" needs you", ev.Activity
	case agentstate.EvFinished:
		if !n.Finished || ev.Turn.DurationMs < int64(n.FinishedMinSec)*1000 {
			return
		}
		a.Kind, a.Title = "finished", who+" finished"
		a.Body = fmt.Sprintf("Turn took %s", (time.Duration(ev.Turn.DurationMs) * time.Millisecond).Round(time.Second))
	case agentstate.EvError:
		if !n.Error {
			return
		}
		a.Kind, a.Title, a.Body = "error", who+" hit an error", in.Error
	case agentstate.EvAgentExit:
		if !n.Error || ev.Rc == 0 {
			return
		}
		a.Kind, a.Title, a.Body = "error", who+" exited", fmt.Sprintf("Agent exited with code %d", ev.Rc)
	default:
		return
	}
	a = s.attn.add(a)
	s.events.Publish(map[string]any{"type": "attention", "attention": a})
}

func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request, p *principal) {
	q := r.URL.Query()
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 {
		limit = min(v, attentionRingSize)
	}
	items, seq := s.attn.since(since, limit)
	needs := []map[string]any{}
	for _, in := range s.sessions.List() {
		if in.AgentState == agentstate.NeedsYou && in.NeedsYouSince != nil {
			needs = append(needs, map[string]any{"sessionId": in.ID, "handle": in.Handle, "since": in.NeedsYouSince})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"bootId": s.attn.bootID, "seq": seq, "items": items, "needsYou": needs})
}

package server

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/azamrizki/nextboard/internal/hostres"
)

// This PC's resources (docs/API.md, Host resources): a sampler of internal/hostres, its readings
// published as "host" events while a page is open, GET /api/host/resources for the details with
// the consoles named, and an alert when This PC stays hot.

// hostAlertAfter is how long This PC stays hot before the alert; hostRearm how long it must
// be out of it before another.
const (
	hostAlertAfter = 30 * time.Second
	hostRearm      = 5 * time.Minute
)

// hostConsolesShown is how many consoles the details name.
const hostConsolesShown = 6

type hostAlertState struct {
	mu       sync.Mutex
	hotSince time.Time
	coolAt   time.Time // since when it is no longer hot, after an alert
	alerted  bool
}

func (s *Server) setupHost(dataDir string) {
	h := hostres.NewSampler(dataDir)
	h.Open = func() bool { return s.events.Count() > 0 }
	h.Thresholds = func() hostres.Thresholds {
		v := s.settings.View().Host
		return hostres.Thresholds{WarnTempC: float64(v.WarnTempC), HotTempC: float64(v.HotTempC), WarnMemPct: float64(v.WarnMemPct), HotMemPct: float64(v.HotMemPct)}
	}
	h.OnSample = s.onHostSample
	s.host = h
}

func (s *Server) runHost(ctx context.Context) {
	if s.host != nil {
		s.host.Run(ctx)
	}
}

func (s *Server) onHostSample(snap hostres.Snapshot) {
	s.hostAlert(snap)
	if s.events.Count() == 0 {
		return
	}
	snap.Consoles = nil // the details ask for them, named
	s.events.Publish(map[string]any{"type": "host", "host": snap})
}

// hostAlert tells the owner once This PC has been hot for hostAlertAfter, and again only after
// it was out of it for hostRearm.
func (s *Server) hostAlert(snap hostres.Snapshot) {
	st := &s.hostAlerts
	st.mu.Lock()
	send := false
	switch {
	case snap.Level == "hot":
		st.coolAt = time.Time{}
		if st.hotSince.IsZero() {
			st.hotSince = snap.At
		}
		if !st.alerted && snap.At.Sub(st.hotSince) >= hostAlertAfter {
			st.alerted, send = true, true
		}
	default:
		st.hotSince = time.Time{}
		if st.alerted {
			if st.coolAt.IsZero() {
				st.coolAt = snap.At
			}
			if snap.At.Sub(st.coolAt) >= hostRearm {
				st.alerted, st.coolAt = false, time.Time{}
			}
		}
	}
	st.mu.Unlock()
	if !send || !s.settings.View().Notifications.Host {
		return
	}
	title := "This PC runs hot"
	if snap.Hostname != "" {
		title = snap.Hostname + " runs hot"
	}
	a := s.attn.add(Attention{Kind: "hostHot", MachineName: "This PC", Title: title, Body: snap.Why})
	s.events.Publish(map[string]any{"type": "attention", "attention": a})
}

// hostConsole is a console of the reading with what names it.
type hostConsole struct {
	hostres.Console
	Handle      string `json:"handle"`
	Name        string `json:"name"`
	Agent       string `json:"agent"`
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	WorkspaceID string `json:"workspaceId"`
}

type hostView struct {
	hostres.Snapshot
	Consoles []hostConsole `json:"consoles"`
}

// handleHostResources is the details: someone looks, so the sampler reads every 2 s with the
// consoles for a few seconds. ?history=1 adds the last hour.
func (s *Server) handleHostResources(w http.ResponseWriter, r *http.Request, p *principal) {
	before := s.host.Latest()
	s.host.Watch()
	snap := before
	// the first look in a while: the reading Watch asked for, with the consoles
	if before.Consoles == nil {
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if l := s.host.Latest(); !l.At.Equal(before.At) {
				snap = l
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	out := map[string]any{"host": s.hostView(snap)}
	if r.URL.Query().Get("history") == "1" {
		out["history"] = s.host.History()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) hostView(snap hostres.Snapshot) hostView {
	v := hostView{Snapshot: snap, Consoles: []hostConsole{}}
	for _, c := range snap.Consoles { // heaviest first
		if len(v.Consoles) == hostConsolesShown {
			break
		}
		hc := hostConsole{Console: c}
		if c.SessionID == "hub" {
			hc.Name = "NextBoard"
		} else if in, ok := s.sessions.Info(c.SessionID); ok {
			hc.Handle, hc.Name, hc.Agent, hc.ProjectID, hc.WorkspaceID = in.Handle, in.Name, in.Agent, in.ProjectID, in.WorkspaceID
			if pr, ok := s.projects.Get(in.ProjectID); ok {
				hc.ProjectName = pr.Name
			}
		} else {
			continue // a console of another NextBoard on This PC
		}
		v.Consoles = append(v.Consoles, hc)
	}
	return v
}

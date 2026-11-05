package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/azamrizki/nextboard/internal/uptime"
)

// Uptime monitoring (docs/API-uptime.md). The checks run in internal/uptime, for as long
// as the hub runs; a board tile only shows them.

func (s *Server) setupUptime() error {
	u, err := uptime.New(s.st)
	if err != nil {
		return err
	}
	u.Host = s.machines.Host
	u.Publish = s.events.Publish
	u.Alert = func(al uptime.Alert) {
		a := s.attn.add(Attention{Kind: al.Kind, MonitorID: al.MonitorID, ProjectID: al.ProjectID, Title: al.Title, Body: al.Body})
		s.events.Publish(map[string]any{"type": "attention", "attention": a})
	}
	s.uptime = u
	return nil
}

func uptimeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, uptime.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, uptime.ErrPaused):
		writeErrCode(w, http.StatusConflict, "paused", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

// uptimeProject resolves {id}, answering when it does not exist.
func (s *Server) uptimeProject(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if _, ok := s.projects.Get(id); !ok {
		writeErrCode(w, http.StatusNotFound, "project_not_found", "project tidak ditemukan")
		return "", false
	}
	return id, true
}

func (s *Server) handleUptimeList(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.uptimeProject(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": s.uptime.List(id), "settings": s.uptime.Settings(id)})
}

func (s *Server) handleUptimeCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.uptimeProject(w, r)
	if !ok {
		return
	}
	m := uptime.Monitor{Active: true, Notify: true, Retries: 1}
	if !decode(w, r, &m) {
		return
	}
	m.ProjectID = id
	v, err := s.uptime.Create(m)
	if err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleUptimeUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	// Merged over what is there: what the body leaves out stays.
	v, err := s.uptime.Update(r.PathValue("id"), func(m *uptime.Monitor) error { return json.Unmarshal(raw, m) })
	if err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleUptimeDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.uptime.Delete(r.PathValue("id")); err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleUptimeCheck(w http.ResponseWriter, r *http.Request, p *principal) {
	if err := s.uptime.CheckNow(r.PathValue("id")); err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, okBody)
}

func (s *Server) handleUptimeHistory(w http.ResponseWriter, r *http.Request, p *principal) {
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "24h"
	}
	out, err := s.uptime.History(r.PathValue("id"), name)
	if err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleUptimeEvents(w http.ResponseWriter, r *http.Request, p *principal) {
	limit := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 200 {
		limit = v
	}
	out, err := s.uptime.Events(r.PathValue("id"), limit)
	if err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleUptimeSettings(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.uptimeProject(w, r)
	if !ok {
		return
	}
	var req struct {
		Webhook string `json:"webhook"`
	}
	if !decode(w, r, &req) {
		return
	}
	c, err := s.uptime.SetWebhook(id, req.Webhook)
	if err != nil {
		uptimeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleUptimeTest(w http.ResponseWriter, r *http.Request, p *principal) {
	id, ok := s.uptimeProject(w, r)
	if !ok {
		return
	}
	if err := s.uptime.TestWebhook(r.Context(), id); err != nil {
		writeErrCode(w, http.StatusBadGateway, "webhook_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

// handleUptimePush takes a report from outside, as Uptime Kuma's push URL does:
// ?status=up|down&msg=&ping=. The token only reaches this one monitor's heartbeat.
func (s *Server) handleUptimePush(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ms := -1
	if v, err := strconv.Atoi(q.Get("ping")); err == nil && v >= 0 {
		ms = v
	}
	if !s.uptime.Push(r.PathValue("token"), q.Get("status") == "down", q.Get("msg"), ms) {
		writeErr(w, http.StatusNotFound, "monitor tidak ditemukan atau sedang di-pause")
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

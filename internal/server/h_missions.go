package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/azamrizki/nextboard/internal/orch"
)

// Missions (docs/API-orchestration.md §22): the lead runs work with a team of consoles to
// the end. Approval, limits and the lead's questions go through POST /api/confirmations/{id}.

func (s *Server) missionRoutes(authed func(string, handler)) {
	authed("GET /api/projects/{id}/missions", s.handleMissions)
	authed("POST /api/projects/{id}/missions", s.handleMissionCreate)
	authed("GET /api/missions/{id}", s.handleMissionGet)
	authed("POST /api/missions/{id}/pause", s.handleMissionAction)
	authed("POST /api/missions/{id}/resume", s.handleMissionAction)
	authed("POST /api/missions/{id}/stop", s.handleMissionAction)
}

// missionErr maps mission errors; the others are the orchestration's.
func (s *Server) missionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, orch.ErrMissionNotFound):
		writeErrCode(w, http.StatusNotFound, "mission_not_found", err.Error())
	case errors.Is(err, orch.ErrMissionActive):
		writeErrCode(w, http.StatusConflict, "mission_active", err.Error())
	case errors.Is(err, orch.ErrMissionState):
		writeErrCode(w, http.StatusConflict, "mission_state", err.Error())
	case errors.Is(err, orch.ErrBadMission):
		writeErrCode(w, http.StatusBadRequest, "bad_mission", err.Error())
	default:
		s.orchErr(w, err)
	}
}

func (s *Server) handleMissions(w http.ResponseWriter, r *http.Request, p *principal) {
	pid := r.PathValue("id")
	if _, ok := s.projects.Get(pid); !ok {
		s.orchErr(w, orch.ErrProjectNotFound)
		return
	}
	active := r.URL.Query().Get("active") == "1"
	items := s.orch.Missions.List(func(m orch.MissionRun) bool { return m.ProjectID == pid && (!active || !m.Ended()) })
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleMissionCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req orch.MissionRequest
	if !decode(w, r, &req) {
		return
	}
	req.ProjectID, req.By = r.PathValue("id"), by(p)
	ms, err := s.orch.CreateMission(req)
	if err != nil {
		s.missionErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ms)
}

func (s *Server) handleMissionGet(w http.ResponseWriter, r *http.Request, p *principal) {
	ms, ok := s.orch.Missions.Get(r.PathValue("id"))
	if !ok {
		s.missionErr(w, orch.ErrMissionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

func (s *Server) handleMissionAction(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	var ms orch.MissionRun
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "/pause"):
		ms, err = s.orch.PauseMission(id)
	case strings.HasSuffix(r.URL.Path, "/resume"):
		ms, err = s.orch.ResumeMission(id)
	default:
		ms, err = s.orch.StopMission(id)
	}
	if err != nil {
		s.missionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

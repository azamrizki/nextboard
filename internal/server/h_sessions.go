package server

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/azamrizki/nextboard/internal/board"
	"github.com/azamrizki/nextboard/internal/machine"
	"github.com/azamrizki/nextboard/internal/project"
	"github.com/azamrizki/nextboard/internal/session"
)

func (s *Server) sessionErr(w http.ResponseWriter, err error) {
	var sleepy *session.SleepError
	switch {
	case errors.As(err, &sleepy):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "code": "cannot_sleep", "reason": sleepy.Reason})
	case errors.Is(err, session.ErrNotAsleep):
		writeErrCode(w, http.StatusConflict, "not_asleep", err.Error())
	case errors.Is(err, session.ErrNotFound), errors.Is(err, machine.ErrNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	case errors.Is(err, session.ErrBadHandle):
		writeErrCode(w, http.StatusBadRequest, "bad_handle", err.Error())
	case errors.Is(err, session.ErrHandleTaken):
		writeErrCode(w, http.StatusConflict, "handle_taken", err.Error())
	case errors.Is(err, session.ErrProfileNotFound):
		writeErrCode(w, http.StatusNotFound, "profile_not_found", err.Error())
	case errors.Is(err, session.ErrProfileMismatch):
		writeErrCode(w, http.StatusBadRequest, "profile_mismatch", err.Error())
	case errors.Is(err, session.ErrProviderNotFound):
		writeErrCode(w, http.StatusNotFound, "provider_not_found", err.Error())
	case errors.Is(err, session.ErrProviderMismatch):
		writeErrCode(w, http.StatusBadRequest, "provider_mismatch", err.Error())
	case errors.Is(err, session.ErrProviderBudget):
		writeErrCode(w, http.StatusConflict, "provider_budget", err.Error())
	case errors.Is(err, session.ErrAgentDisabled):
		writeErrCode(w, http.StatusConflict, "agent_disabled", err.Error())
	case errors.Is(err, session.ErrAgentThisPC):
		writeErrCode(w, http.StatusUnprocessableEntity, "agent_this_pc_only", err.Error())
	case errors.Is(err, session.ErrPartReadOnly):
		writeErrCode(w, http.StatusUnprocessableEntity, "part_read_only", err.Error())
	case errors.Is(err, session.ErrUnknownPreset):
		writeErrCode(w, http.StatusBadRequest, "unknown_agent", err.Error())
	case errors.Is(err, session.ErrBadMode):
		writeErrCode(w, http.StatusBadRequest, "bad_mode", err.Error())
	case errors.Is(err, session.ErrNotAgent):
		writeErrCode(w, http.StatusConflict, "not_agent", err.Error())
	case errors.Is(err, session.ErrAgentRunning):
		writeErrCode(w, http.StatusConflict, "agent_running", err.Error())
	case errors.Is(err, session.ErrShellBusy):
		writeErrCode(w, http.StatusConflict, "shell_busy", err.Error())
	case errors.Is(err, session.ErrWrapMissing):
		writeErrCode(w, http.StatusPreconditionFailed, "wrap_missing", err.Error())
	case errors.Is(err, session.ErrOffline):
		writeErrCode(w, http.StatusServiceUnavailable, "machine_offline", err.Error())
	default:
		writeErr(w, http.StatusBadRequest, err.Error())
	}
}

func (s *Server) emitMachine(id string) {
	if in, ok := s.machines.Info(id); ok {
		s.events.Publish(map[string]any{"type": "machine", "machine": in})
	}
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.sessions.List())
}

// handleSessionCreate creates a session in a workspace and places its
// terminal node on that board in the same request. Without workspaceId
// (older clients) the default workspace is used.
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		session.CreateRequest
		Node *board.Rect `json:"node"`
	}
	if !decode(w, r, &req) {
		return
	}
	var proj project.Project
	var ws project.Workspace
	if req.WorkspaceID != "" {
		var ok bool
		if ws, proj, ok = s.projects.Workspace(req.WorkspaceID); !ok {
			writeErr(w, http.StatusNotFound, project.ErrWorkspaceNotFound.Error())
			return
		}
	} else {
		var err error
		if proj, ws, err = s.defaultWorkspace(); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if req.Node != nil {
		if err := req.Node.Validate(); err != nil {
			writeErr(w, http.StatusBadRequest, "node: "+err.Error())
			return
		}
	}
	cr := req.CreateRequest
	if err := placeInPart(proj, &cr); err != nil {
		if errors.Is(err, errPartMachine) {
			writeErrCode(w, http.StatusBadRequest, "part_machine", err.Error())
		} else {
			partErr(w, err)
		}
		return
	}
	cr.ProjectID, cr.WorkspaceID = proj.ID, ws.ID
	in, err := s.sessions.Create(cr)
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	if !cr.Panel {
		if _, err := s.boards.AddTerminal(ws.ID, in.ID, req.Node); err != nil {
			_ = s.sessions.Delete(in.ID)
			s.boardErr(w, err)
			return
		}
	}
	s.emitMachine(in.MachineID)
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request, p *principal) {
	in, ok := s.sessions.Info(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleSessionRename(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name   *string `json:"name"`
		Handle *string `json:"handle"`
		// KeepAwake: never put this agent to sleep (ROADMAP #50).
		KeepAwake *bool `json:"keepAwake"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	var in session.Info
	var err error
	if req.Name != nil || req.Handle != nil || req.KeepAwake == nil {
		if in, err = s.sessions.Update(id, req.Name, req.Handle); err != nil {
			s.sessionErr(w, err)
			return
		}
	}
	if req.KeepAwake != nil {
		if in, err = s.sessions.SetKeepAwake(id, *req.KeepAwake); err != nil {
			s.sessionErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, in)
}

// Sleeping agents (ROADMAP #50, docs/API-agents.md §28).

func (s *Server) handleSessionSleep(w http.ResponseWriter, r *http.Request, p *principal) {
	in, err := s.sessions.Sleep(r.PathValue("id"))
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleSessionWake(w http.ResponseWriter, r *http.Request, p *principal) {
	in, err := s.sessions.Wake(r.PathValue("id"))
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// handleProjectWake starts the asleep agents of a project, or of one of its boards
// (POST /api/projects/{id}/wake {"workspaceId"?}). The web calls it as soon as someone is about
// to open the project (a hover, a press, the page opening), so the CLIs, which take a few seconds
// to come back, start before the board is on screen. Only asleep consoles are touched.
func (s *Server) handleProjectWake(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		WorkspaceID string `json:"workspaceId"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	pid := r.PathValue("id")
	if _, ok := s.projects.Get(pid); !ok {
		writeErr(w, http.StatusNotFound, project.ErrNotFound.Error())
		return
	}
	n := s.sessions.WakeWhere(func(sp session.Spec) bool {
		return sp.ProjectID == pid && (req.WorkspaceID == "" || sp.WorkspaceID == req.WorkspaceID)
	})
	writeJSON(w, http.StatusOK, map[string]int{"woken": n})
}

// recentProjects are the projects whose agents stay awake when idle and start right after a
// restart: the settings.agents.keepRecentProjects opened last, opened within a day (ROADMAP #50).
func (s *Server) recentProjects() []string {
	k := s.settings.View().Agents.KeepRecentProjects
	var out []string
	for _, p := range s.projects.List() { // most recently opened first
		if len(out) >= k || time.Since(p.LastOpenedAt) > recentWarm {
			break
		}
		out = append(out, p.ID)
	}
	return out
}

func (s *Server) recentProject(pid string) bool {
	return slices.Contains(s.recentProjects(), pid)
}

// wakeLastProject starts the asleep agents of the recent projects after a hub restart: that is
// where the owner most likely goes back to. Others wait to be opened.
func (s *Server) wakeLastProject() {
	recent := s.recentProjects()
	s.sessions.WakeWhere(func(sp session.Spec) bool { return slices.Contains(recent, sp.ProjectID) })
}

// recentWarm: a project opened longer ago than this is not kept warm.
const recentWarm = 24 * time.Hour

func (s *Server) handleSessionDraftDismiss(w http.ResponseWriter, r *http.Request, p *principal) {
	in, err := s.sessions.DismissDraft(r.PathValue("id"))
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	id := r.PathValue("id")
	in, ok := s.sessions.Info(id)
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return
	}
	if err := s.sessions.Delete(id); err != nil {
		s.sessionErr(w, err)
		return
	}
	s.emitMachine(in.MachineID) // the terminal node goes in OnRemove
	writeJSON(w, http.StatusOK, okBody)
}

func (s *Server) handleSessionRestart(w http.ResponseWriter, r *http.Request, p *principal) {
	var req session.RestartRequest
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	in, err := s.sessions.Restart(r.PathValue("id"), req)
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleSessionKill(w http.ResponseWriter, r *http.Request, p *principal) {
	in, err := s.sessions.Kill(r.PathValue("id"))
	if err != nil {
		s.sessionErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

func (s *Server) handleSessionInput(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Data string `json:"data"`
	}
	if !decode(w, r, &req) {
		return
	}
	sess, ok := s.sessions.Get(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, session.ErrNotFound.Error())
		return
	}
	if err := s.sessionInput(r.PathValue("id"), sess, []byte(req.Data)); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, okBody)
}

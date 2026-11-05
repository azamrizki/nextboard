package server

import "net/http"

// Project groups (docs/API-board.md §2). Every client holds the whole tree, so
// each change publishes all of it.

func (s *Server) emitProjectGroups() {
	s.events.Publish(map[string]any{"type": "projectGroups", "groups": s.projects.Groups()})
}

func (s *Server) handleProjectGroups(w http.ResponseWriter, r *http.Request, p *principal) {
	writeJSON(w, http.StatusOK, s.projects.Groups())
}

func (s *Server) handleProjectGroupCreate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name     string `json:"name"`
		ParentID string `json:"parentId"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, err := s.projects.CreateGroup(req.Name, req.ParentID)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectGroups()
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) handleProjectGroupUpdate(w http.ResponseWriter, r *http.Request, p *principal) {
	var req struct {
		Name     *string `json:"name"`
		ParentID *string `json:"parentId"`
	}
	if !decode(w, r, &req) {
		return
	}
	g, err := s.projects.UpdateGroup(r.PathValue("id"), req.Name, req.ParentID)
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectGroups()
	writeJSON(w, http.StatusOK, g)
}

// handleProjectGroupDelete removes the group only: what was inside moves up.
func (s *Server) handleProjectGroupDelete(w http.ResponseWriter, r *http.Request, p *principal) {
	moved, err := s.projects.DeleteGroup(r.PathValue("id"))
	if err != nil {
		projectErr(w, err)
		return
	}
	s.emitProjectGroups()
	for _, id := range moved {
		s.emitProject(id)
	}
	writeJSON(w, http.StatusOK, okBody)
}
